package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
	"github.com/jake-molnia/agent-runtime/definitions"
	"github.com/jake-molnia/agent-runtime/githubreview"
	"github.com/jake-molnia/agent-runtime/hatchetbridge"
	"github.com/jake-molnia/agent-runtime/orchestration"
)

func pinnedDefinition(root string, spec hatchetbridge.Spec) (orchestration.Definition, error) {
	snapshot, err := definitions.ReadSnapshot(root, spec.Digest)
	if err != nil {
		return orchestration.Definition{}, err
	}
	if snapshot.Agent.Name != spec.Agent {
		return orchestration.Definition{}, errors.New("snapshot agent mismatch")
	}
	return snapshot.Definition()
}

func registerWorkflows(ctx context.Context, client *hatchet.Client, engine *orchestration.Engine, catalog *definitions.Catalog, snapshots string) ([]hatchet.WorkflowBase, map[string]http.Handler, func(), error) {
	definition := func(spec hatchetbridge.Spec) (orchestration.Definition, error) {
		return pinnedDefinition(snapshots, spec)
	}
	manual, err := hatchetbridge.Build(client, engine, hatchetbridge.Lifecycle{
		Name: "agent-run", Definition: definition,
		Structured: func(spec hatchetbridge.Spec) (bool, error) {
			snapshot, err := definitions.ReadSnapshot(snapshots, spec.Digest)
			return len(snapshot.Agent.Schema) > 0, err
		},
		Resolve: func(ctx context.Context, input hatchetbridge.Input, runID string) (hatchetbridge.Spec, error) {
			spec := hatchetbridge.Spec{Agent: input.Agent, Digest: input.Digest, Run: input.Run}
			if _, err := definition(spec); err != nil {
				return spec, err
			}
			if len(input.Review) != 0 || input.Run.Prompt == "" {
				return spec, errors.New("manual agent requires a prompt and no review payload")
			}
			return spec, nil
		},
		Validate: func(spec hatchetbridge.Spec, data json.RawMessage) error {
			snapshot, err := definitions.ReadSnapshot(snapshots, spec.Digest)
			if err != nil {
				return err
			}
			return snapshot.ValidateOutput(data)
		},
	})
	if err != nil {
		return nil, nil, nil, err
	}
	workflows := []hatchet.WorkflowBase{manual}
	ingress := map[string]http.Handler{}
	closeStore := func() {}
	if len(catalog.Automations) == 0 {
		return workflows, ingress, closeStore, nil
	}
	if engine.Artifacts == nil {
		return nil, nil, nil, errors.New("automations require AGENT_ARTIFACT_DIR")
	}
	github, err := githubConnection(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	if os.Getenv("AGENT_REVIEW_DATABASE_URL") == "" {
		return nil, nil, nil, errors.New("automations require AGENT_REVIEW_DATABASE_URL")
	}
	store, err := githubreview.OpenStore(ctx, os.Getenv("AGENT_REVIEW_DATABASE_URL"))
	if err != nil {
		return nil, nil, nil, err
	}
	closeStore = store.Close
	ok := false
	defer func() {
		if !ok {
			closeStore()
		}
	}()
	handler, err := githubreview.NewHandler(github.Client, store)
	if err != nil {
		return nil, nil, nil, err
	}
	for name, automation := range catalog.Automations {
		workflow, err := hatchetbridge.Build(client, engine, hatchetbridge.Lifecycle{
			Name: name, Automation: true, Definition: definition,
			Resolve: func(ctx context.Context, input hatchetbridge.Input, runID string) (hatchetbridge.Spec, error) {
				spec := hatchetbridge.Spec{Agent: automation.Agent, Digest: input.Digest, Run: input.Run}
				if input.Agent != automation.Agent || input.Run.Prompt != "" {
					return spec, errors.New("automation input cannot select behavior")
				}
				if _, err := definition(spec); err != nil {
					return spec, err
				}
				snapshot, err := definitions.ReadSnapshot(snapshots, spec.Digest)
				if err != nil {
					return spec, err
				}
				if len(snapshot.Agent.Schema) == 0 || !slices.Contains(snapshot.Agent.Capabilities, "github.diff") {
					return spec, errors.New("snapshot is not a structured GitHub review agent")
				}
				var review githubreview.Input
				if err := strictJSON(input.Review, &review); err != nil {
					return spec, err
				}
				if input.GroupKey != fmt.Sprintf("%d/%d", review.RepositoryID, review.Number) {
					return spec, errors.New("invalid automation concurrency key")
				}
				resolved, err := handler.Resolve(ctx, review, spec.Digest, runID)
				if err != nil {
					return spec, err
				}
				spec.Skip, spec.Run.Prompt = resolved.Skip, resolved.Prompt
				resolved.Prompt = ""
				spec.Domain, err = json.Marshal(resolved)
				return spec, err
			},
			Validate: func(spec hatchetbridge.Spec, data json.RawMessage) error {
				snapshot, err := definitions.ReadSnapshot(snapshots, spec.Digest)
				if err != nil {
					return err
				}
				if err := snapshot.ValidateOutput(data); err != nil {
					return err
				}
				resolved, err := resolvedReview(spec)
				if err != nil {
					return err
				}
				return githubreview.ValidateResolvedOutput(resolved, data)
			},
			Publish: func(ctx context.Context, spec hatchetbridge.Spec, data json.RawMessage) (json.RawMessage, error) {
				resolved, err := resolvedReview(spec)
				if err != nil {
					return nil, err
				}
				out, err := handler.Publish(ctx, resolved, data)
				if err != nil {
					return nil, err
				}
				return json.Marshal(out)
			},
		})
		if err != nil {
			return nil, nil, nil, err
		}
		workflows = append(workflows, workflow)
		webhook, err := githubreview.NewWebhookHandler(githubreview.WebhookConfig{Secret: github.WebhookSecret, Actions: automation.Trigger.Actions, Allowed: github.Allowed, Submit: func(ctx context.Context, review githubreview.Input) error {
			data, err := json.Marshal(review)
			if err != nil {
				return err
			}
			input, err := invocation(catalog, name, data)
			if err != nil {
				return err
			}
			_, err = hatchetbridge.SubmitInput(ctx, client, name, input)
			return err
		}})
		if err != nil {
			return nil, nil, nil, err
		}
		ingress["/webhooks/"+name] = webhook
	}
	ok = true
	return workflows, ingress, closeStore, nil
}

func resolvedReview(spec hatchetbridge.Spec) (githubreview.Resolved, error) {
	var resolved githubreview.Resolved
	if err := strictJSON(spec.Domain, &resolved); err != nil || resolved.Digest != spec.Digest || resolved.Prompt != "" {
		return resolved, errors.New("invalid resolved review")
	}
	resolved.Prompt = spec.Run.Prompt
	return resolved, nil
}
