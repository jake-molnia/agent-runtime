package hatchetbridge

import (
	"context"
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
	State         ConfiguredState       `json:"state"`
	Output        json.RawMessage       `json:"output,omitempty"`
}

func ReviewLabels(config githubreview.Integration) map[string]*hatchet.DesiredWorkerLabel {
	labels := map[string]*hatchet.DesiredWorkerLabel{}
	for k, v := range config.WorkerLabels {
		labels[k] = &hatchet.DesiredWorkerLabel{Value: v, Required: true}
	}
	return labels
}

func RegisterReview(client *hatchet.Client, config githubreview.Integration, reload func() (githubreview.Integration, error), handler *githubreview.Handler, root string, plan workflows.Snapshot, backend agentexec.Backend, store messages.Store, hooks ...ConfiguredHooks) (*hatchet.Workflow, error) {
	if client == nil || handler == nil || reload == nil || root == "" || store == nil {
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
	if plan.Workflow.Notebook {
		return nil, errors.New("GitHub review workflows cannot use notebooks")
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
	revision, err := ReviewRevision(config, plan.Digest)
	if err != nil {
		return nil, err
	}
	load := func(digest string) (workflows.Snapshot, error) {
		if digest != plan.Digest {
			return workflows.Snapshot{}, errors.New("pinned review configuration changed; original worker required")
		}
		pinned, err := workflows.Read(root, digest)
		if err != nil {
			return pinned, err
		}
		if pinned.Workflow.Name != config.Workflow {
			return pinned, errors.New("review workflow mismatch")
		}
		return pinned, nil
	}
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
	resolveName := ConfiguredWorkflowName("resolve", revision)
	resolve := workflow.NewTask(resolveName, func(ctx hatchet.Context, input map[string]any) (ReviewResult, error) {
		result := ReviewResult{Configuration: revision, State: ConfiguredState{Digest: plan.Digest}}
		raw, err := json.Marshal(input)
		if err != nil {
			return result, err
		}
		request, skip, err := config.Normalize(raw)
		if err != nil {
			return result, err
		}
		if skip {
			result.Resolved.Skip = true
			result.State.Skip = true
			return result, nil
		}
		current, err := reload()
		if err != nil {
			return result, err
		}
		policy, err := current.Authorize(request.Review)
		if err != nil {
			return result, err
		}
		result.Resolved, err = handler.Resolve(ctx.GetContext(), request.Review, plan.Digest, ctx.WorkflowRunId())
		if err != nil {
			return result, err
		}
		result.Publish = request.Publish && policy.Publish
		result.State.Skip = result.Resolved.Skip
		if result.State.Skip {
			return result, nil
		}
		if _, err = load(result.State.Digest); err != nil {
			return result, err
		}
		value, err := json.Marshal(struct {
			Review githubreview.Input `json:"review"`
			Prompt string             `json:"prompt"`
		}{result.Resolved.Input, result.Resolved.Prompt})
		if err != nil {
			return result, err
		}
		result.State.Input, err = service.Publish(ctx.GetContext(), ctx.WorkflowRunId(), messages.Message{Version: messages.Version, ID: "workflow-input", ContextID: ctx.WorkflowRunId(), TaskID: "resolve", From: messages.Actor{Agent: "caller", Revision: "v1"}, To: plan.Workflow.Name, Parts: []messages.Part{{Name: "input", Kind: messages.Data, Schema: "workflow-input", Data: value}}})
		return result, err
	}, hatchet.WithRetries(0), hatchet.WithExecutionTimeout(time.Minute))
	readResult := func(ctx hatchet.Context) (ReviewResult, error) {
		var result ReviewResult
		if err := ctx.StepOutput(resolveName, &result); err != nil {
			return result, err
		}
		if result.Configuration != revision || result.State.Digest != plan.Digest || (!result.Resolved.Skip && result.Resolved.Digest != plan.Digest) {
			return result, errors.New("pinned review configuration changed; original worker required")
		}
		return result, nil
	}
	readState := func(ctx hatchet.Context) (ConfiguredState, error) {
		result, err := readResult(ctx)
		return result.State, err
	}
	graph := configuredStageGraph{current: plan, load: load, executor: executor, store: store}
	stageName := func(id string) string { return ConfiguredWorkflowName("agent-"+id, revision) }
	stages, err := graph.register(workflow, resolve, readState, stageName, append([]ConfiguredHooks{ReviewBeforeStep(reload, handler)}, hooks...)...)
	if err != nil {
		return nil, err
	}
	parents := []*hatchet.Task{resolve}
	seen := map[string]bool{}
	for _, id := range append(append(append([]string{}, config.CandidateSteps...), config.WriteupStep), plan.Workflow.Output) {
		if !seen[id] {
			parents = append(parents, stages[id])
			seen[id] = true
		}
	}
	reduceName := ConfiguredWorkflowName("reduce", revision)
	reduce := workflow.NewTask(reduceName, func(ctx hatchet.Context, _ map[string]any) (ReviewResult, error) {
		result, err := readResult(ctx)
		if err != nil || result.Resolved.Skip {
			return result, err
		}
		if _, err = load(result.State.Digest); err != nil {
			return result, err
		}
		if err = CheckReview(ctx.GetContext(), reload, handler, result.Resolved.Input); err != nil {
			return result, err
		}
		read := func(id string) (json.RawMessage, error) {
			var output ConfiguredResult
			if err := ctx.StepOutput(stageName(id), &output); err != nil {
				return nil, err
			}
			if output.Digest != plan.Digest {
				return nil, errors.New("review output digest mismatch")
			}
			return configuredOutput(ctx.GetContext(), service, ctx.WorkflowRunId(), plan.Workflow.Name, result.State, plan, id, output.Message)
		}
		candidates := []json.RawMessage{}
		for _, id := range config.CandidateSteps {
			value, err := read(id)
			if err != nil {
				return result, err
			}
			candidates = append(candidates, value)
		}
		wording, err := read(config.WriteupStep)
		if err != nil {
			return result, err
		}
		result.Output, err = githubreview.Reduce(candidates, wording)
		if err != nil {
			return result, err
		}
		return result, githubreview.ValidateResolvedOutput(result.Resolved, result.Output)
	}, hatchet.WithParents(parents...), hatchet.WithRetries(0), hatchet.WithExecutionTimeout(time.Minute))
	workflow.NewTask(ConfiguredWorkflowName("publish", revision), func(ctx hatchet.Context, _ map[string]any) (githubreview.Outcome, error) {
		var result ReviewResult
		if err := ctx.StepOutput(reduceName, &result); err != nil {
			return githubreview.Outcome{}, err
		}
		if result.Configuration != revision || (!result.Resolved.Skip && result.Resolved.Digest != plan.Digest) {
			return githubreview.Outcome{}, errors.New("pinned review configuration changed; original worker required")
		}
		return PublishReview(ctx.GetContext(), reload, handler, result)
	}, hatchet.WithParents(reduce), hatchet.WithRetries(2), hatchet.WithExecutionTimeout(2*time.Minute))
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
