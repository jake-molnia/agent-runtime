package hatchetbridge

import (
	"context"
	"errors"
	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
	"github.com/jake-molnia/agent-runtime/lifecycle"
	"time"
)

func RegisterT3(client *hatchet.Client, controller *lifecycle.Controller) *hatchet.Workflow {
	one := int32(1)
	strategy := hatchet.QueueNewest
	workflow := client.NewWorkflow("t3-workspace-lifecycle", hatchet.WithWorkflowConcurrency(hatchet.Concurrency{Expression: "input.workspaceId", MaxRuns: &one, LimitStrategy: &strategy, Name: "t3-workspace", IsTenantScoped: true}), hatchet.WithWorkflowIdempotency(hatchet.IdempotencyConfig{Expression: "input.workspaceId + ':' + input.operationId", TTL: 24 * time.Hour, Method: hatchet.IdempotencyMethodTTL}))
	workflow.NewTask("transition", func(ctx hatchet.Context, r lifecycle.Request) (lifecycle.State, error) {
		bounded, cancel := context.WithTimeout(ctx.GetContext(), 4*time.Minute)
		defer cancel()
		return controller.Execute(bounded, r)
	}, hatchet.WithRetries(3), hatchet.WithExecutionTimeout(5*time.Minute), hatchet.WithScheduleTimeout(time.Hour))
	return workflow
}

type T3Tasks struct {
	Client   *hatchet.Client
	Workflow *hatchet.Workflow
}

func (t T3Tasks) Submit(ctx context.Context, r lifecycle.Request) (string, error) {
	ref, err := t.Workflow.RunNoWait(ctx, r)
	if err != nil {
		var collision *hatchet.IdempotencyCollisionError
		if errors.As(err, &collision) {
			return collision.ExistingRunExternalId, nil
		}
		return "", err
	}
	return ref.RunId, nil
}
func (t T3Tasks) Status(ctx context.Context, id string) (any, error) {
	return t.Client.Runs().Get(ctx, id)
}
