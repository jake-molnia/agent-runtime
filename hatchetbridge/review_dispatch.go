package hatchetbridge

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/hatchet-dev/hatchet/pkg/client/rest"
	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
	"github.com/jake-molnia/agent-runtime/githubreview"
)

// RegisterReviewIngress keeps one stable admission identity across graph versions.
// Only forwarding runs here; the complete review executes on required labels.
func RegisterReviewIngress(client *hatchet.Client, config githubreview.Integration, adapter *hatchet.Workflow, digest string, reload func() (githubreview.Integration, error), handler *githubreview.Handler) (*hatchet.Workflow, error) {
	revision, err := ReviewRevision(config, digest)
	if err != nil {
		return nil, err
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
	ingress := client.NewWorkflow(config.Name, options...)
	ingress.NewDurableTask("dispatch-"+revision, func(ctx hatchet.DurableContext, input map[string]any) (githubreview.Outcome, error) {
		raw, err := json.Marshal(input)
		if err != nil {
			return githubreview.Outcome{}, err
		}
		request, skip, err := config.Normalize(raw)
		if err != nil {
			return githubreview.Outcome{}, err
		}
		if skip {
			return githubreview.Outcome{Status: "skipped"}, nil
		}
		guarded, stopGuard, err := watchStage(ctx.GetContext(), 15*time.Second, func(checkCtx context.Context) error { return CheckReview(checkCtx, reload, handler, request.Review) })
		if errors.Is(err, ErrReviewObsolete) {
			return githubreview.Outcome{Status: "stale"}, nil
		}
		if err != nil {
			return githubreview.Outcome{}, err
		}
		defer stopGuard()
		ctx.SetContext(guarded)
		result, err := runReviewChild(ctx, client, adapter, input, ReviewLabels(config))
		if err != nil {
			return githubreview.Outcome{}, err
		}
		var outcome githubreview.Outcome
		err = result.TaskOutput("publish").Into(&outcome)
		return outcome, err
	}, hatchet.WithRetries(0), hatchet.WithExecutionTimeout(24*time.Hour), hatchet.WithScheduleTimeout(time.Hour))
	return ingress, nil
}

func runReviewChild(ctx hatchet.DurableContext, client *hatchet.Client, child *hatchet.Workflow, input any, labels map[string]*hatchet.DesiredWorkerLabel) (*hatchet.WorkflowResult, error) {
	ref, err := child.RunNoWait(ctx, input, hatchet.WithDesiredWorkerLabels(labels))
	if err != nil {
		return nil, err
	}
	return awaitChild(ctx.GetContext(), ref.Result, func(cancelCtx context.Context) error {
		id, err := uuid.Parse(ref.RunId)
		if err != nil {
			return err
		}
		ids := []uuid.UUID{id}
		_, err = client.Runs().Cancel(cancelCtx, rest.V1CancelTaskRequest{ExternalIds: &ids})
		return err
	})
}

func awaitChild(ctx context.Context, result func() (*hatchet.WorkflowResult, error), cancel func(context.Context) error) (*hatchet.WorkflowResult, error) {
	type outcome struct {
		result *hatchet.WorkflowResult
		err    error
	}
	done := make(chan outcome, 1)
	go func() { value, err := result(); done <- outcome{value, err} }()
	select {
	case value := <-done:
		if ctx.Err() == nil {
			return value.result, value.err
		}
	case <-ctx.Done():
	}
	cleanup, finish := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer finish()
	return nil, errors.Join(context.Cause(ctx), cancel(cleanup))
}
