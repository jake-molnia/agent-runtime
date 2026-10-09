package hatchetbridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"time"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
	"github.com/jake-molnia/agent-runtime/agentexec"
	"github.com/jake-molnia/agent-runtime/githubreview"
	"github.com/jake-molnia/agent-runtime/messages"
	"github.com/jake-molnia/agent-runtime/workflows"
)

const (
	reviewConcurrencyExpression = "has(input.pull_request) ? string(input.repository.id) + ':' + string(input.number) : string(input.review.repository_id) + ':' + string(input.review.number)"
	reviewIdempotencyExpression = "has(input.pull_request) ? 'github:' + string(input.repository.id) + ':' + string(input.number) + ':' + input.pull_request.head.sha : (input.review.delivery_id.startsWith('github:') ? input.review.delivery_id : 'manual:' + string(input.review.repository_id) + ':' + string(input.review.number) + ':' + input.review.delivery_id)"
)

type ReviewResult struct {
	Configuration string                `json:"configuration"`
	Resolved      githubreview.Resolved `json:"resolved"`
	Publish       bool                  `json:"publish"`
	Output        json.RawMessage       `json:"output,omitempty"`
}

func ReviewLabels(config githubreview.Integration) map[string]*hatchet.DesiredWorkerLabel {
	labels := map[string]*hatchet.DesiredWorkerLabel{}
	for k, v := range config.WorkerLabels {
		labels[k] = &hatchet.DesiredWorkerLabel{Value: v, Required: true}
	}
	return labels
}
func RegisterReview(client *hatchet.Client, config githubreview.Integration, reload func() (githubreview.Integration, error), handler *githubreview.Handler, child *hatchet.Workflow, plan workflows.Snapshot, backend agentexec.Backend, store messages.Store) (*hatchet.Workflow, error) {
	if client == nil || handler == nil || child == nil || reload == nil {
		return nil, errors.New("review dependencies required")
	}
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if err := plan.Verify(); err != nil {
		return nil, err
	}
	if plan.Workflow.Name != config.Workflow {
		return nil, errors.New("review workflow mismatch")
	}
	for _, step := range append(append([]string{}, config.CandidateSteps...), config.WriteupStep) {
		if _, ok := plan.Workflow.Steps[step]; !ok {
			return nil, errors.New("review step missing")
		}
	}
	executor, err := agentexec.New(backend)
	if err != nil {
		return nil, err
	}
	service, err := configuredService(plan, executor, store)
	if err != nil {
		return nil, err
	}
	configJSON, err := json.Marshal(config)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(append(configJSON, []byte(plan.Digest)...))
	revision := hex.EncodeToString(sum[:])
	limit, strategy := int32(1), hatchet.GroupRoundRobin

	options := []hatchet.WorkflowOption{hatchet.WithWorkflowVersion(revision), hatchet.WithWorkflowConcurrency(hatchet.Concurrency{Expression: reviewConcurrencyExpression, MaxRuns: &limit, LimitStrategy: &strategy}), hatchet.WithWorkflowIdempotency(hatchet.IdempotencyConfig{Expression: reviewIdempotencyExpression, TTL: 30 * 24 * time.Hour, Method: hatchet.IdempotencyMethodTTL})}
	if config.NativeEvents {
		events := []string{}
		for _, action := range config.Actions {
			events = append(events, "github:pull_request:"+action)
		}
		options = append(options, hatchet.WithWorkflowEvents(events...), hatchet.WithDefaultFilters(hatchet.DefaultFilter{Expression: "!input.pull_request.draft && input.pull_request.state == 'open'", Scope: config.Name}))
	}
	workflow := client.NewWorkflow(config.Name, options...)
	resolve := workflow.NewTask("resolve", func(ctx hatchet.Context, input map[string]any) (ReviewResult, error) {
		raw, err := json.Marshal(input)
		if err != nil {
			return ReviewResult{}, err
		}
		request, skip, err := config.Normalize(raw)
		if err != nil {
			return ReviewResult{}, err
		}
		if skip {
			return ReviewResult{Resolved: githubreview.Resolved{Skip: true}}, nil
		}
		current, err := reload()
		if err != nil {
			return ReviewResult{}, err
		}
		policy, err := current.Authorize(request.Review)
		if err != nil {
			return ReviewResult{}, err
		}
		resolved, err := handler.Resolve(ctx.GetContext(), request.Review, plan.Digest, ctx.WorkflowRunId())
		if err != nil {
			return ReviewResult{}, err
		}
		return ReviewResult{Resolved: resolved, Publish: request.Publish && policy.Publish, Configuration: revision}, nil
	}, hatchet.WithRetries(0), hatchet.WithExecutionTimeout(time.Minute))
	execute := workflow.NewDurableTask("review", func(ctx hatchet.DurableContext, _ map[string]any) (ReviewResult, error) {
		var result ReviewResult
		if err := ctx.StepOutput("resolve", &result); err != nil {
			return result, err
		}
		resolved := result.Resolved
		if !resolved.Skip && (resolved.Digest != plan.Digest || result.Configuration != revision) {
			return result, errors.New("pinned review configuration changed; original worker required")
		}
		if resolved.Skip {
			return result, nil
		}
		input, err := json.Marshal(struct {
			Review githubreview.Input `json:"review"`
			Prompt string             `json:"prompt"`
		}{resolved.Input, resolved.Prompt})
		if err != nil {
			return result, err
		}
		childResult, err := child.Run(ctx, ConfiguredInput{Digest: plan.Digest, Input: input}, hatchet.WithDesiredWorkerLabels(ReviewLabels(config)))
		if err != nil {
			return result, err
		}
		if err = ctx.GetContext().Err(); err != nil {
			return result, err
		}
		var state ConfiguredState
		if err = childResult.TaskOutput("resolve").Into(&state); err != nil {
			return result, err
		}
		if state.Digest != plan.Digest {
			return result, errors.New("review child digest mismatch")
		}
		read := func(step string) (json.RawMessage, error) {
			var output ConfiguredResult
			if err := childResult.TaskOutput(step).Into(&output); err != nil {
				return nil, err
			}
			if output.Digest != plan.Digest {
				return nil, errors.New("review output digest mismatch")
			}
			return configuredOutput(ctx.GetContext(), service, childResult.RunId, plan.Workflow.Name, state, plan, step, output.Message)
		}
		candidates := []json.RawMessage{}
		for _, step := range config.CandidateSteps {
			data, err := read(step)
			if err != nil {
				return result, err
			}
			candidates = append(candidates, data)
		}
		wording, err := read(config.WriteupStep)
		if err != nil {
			return result, err
		}
		result.Output, err = githubreview.Reduce(candidates, wording)
		if err != nil {
			return result, err
		}
		return result, githubreview.ValidateResolvedOutput(resolved, result.Output)
	}, hatchet.WithParents(resolve), hatchet.WithRetries(0), hatchet.WithExecutionTimeout(24*time.Hour), hatchet.WithScheduleTimeout(time.Hour))
	workflow.NewTask("publish", func(ctx hatchet.Context, input map[string]any) (githubreview.Outcome, error) {
		var result ReviewResult
		if err := ctx.StepOutput("review", &result); err != nil {
			return githubreview.Outcome{}, err
		}
		if !result.Resolved.Skip && (result.Configuration != revision || result.Resolved.Digest != plan.Digest) {
			return githubreview.Outcome{}, errors.New("pinned review configuration changed; original worker required")
		}
		return PublishReview(ctx.GetContext(), reload, handler, result)
	}, hatchet.WithParents(execute), hatchet.WithRetries(2), hatchet.WithExecutionTimeout(2*time.Minute))
	return workflow, nil
}
func PublishReview(ctx context.Context, reload func() (githubreview.Integration, error), handler *githubreview.Handler, result ReviewResult) (githubreview.Outcome, error) {
	if err := ctx.Err(); err != nil {
		return githubreview.Outcome{}, err
	}
	if result.Resolved.Skip {
		return githubreview.Outcome{Status: "skipped"}, nil
	}
	current, err := reload()
	if err != nil {
		return githubreview.Outcome{}, err
	}
	policy, err := current.Authorize(result.Resolved.Input)
	if err != nil {
		return githubreview.Outcome{Status: "disabled"}, nil
	}
	if err = githubreview.ValidateResolvedOutput(result.Resolved, result.Output); err != nil {
		return githubreview.Outcome{}, err
	}
	if !result.Publish || !policy.Publish {
		return githubreview.Outcome{Status: "dry-run"}, nil
	}
	return handler.Publish(ctx, result.Resolved, result.Output)
}
