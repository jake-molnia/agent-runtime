package hatchetbridge

import (
	"context"
	"errors"
	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
	"github.com/jake-molnia/agent-runtime/lifecycle"
	"time"
)

func RegisterT3(client *hatchet.Client, controller *lifecycle.Controller) *hatchet.Workflow {
	key := "input.workspaceId + ':' + input.operationId"
	if controller.PoolID != "" {
		key = "'" + controller.PoolID + ":' + " + key
	}
	one := int32(1)
	strategy := hatchet.GroupRoundRobin
	workflow := client.NewWorkflow(t3Name("t3-workspace-lifecycle", controller.PoolID), hatchet.WithWorkflowConcurrency(hatchet.Concurrency{Expression: "input.workspaceId", MaxRuns: &one, LimitStrategy: &strategy, Name: t3Name("t3-workspace", controller.PoolID), IsTenantScoped: true}), hatchet.WithWorkflowIdempotency(hatchet.IdempotencyConfig{Expression: key, TTL: 24 * time.Hour, Method: hatchet.IdempotencyMethodStatus}))
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
func (t T3Tasks) Status(ctx context.Context, id string) (lifecycle.TaskStatus, error) {
	run, err := t.Client.Runs().Get(ctx, id)
	if err != nil {
		return lifecycle.TaskStatus{}, err
	}
	return lifecycle.TaskStatus{ID: id, Status: string(run.Run.Status)}, nil
}

func t3Name(base, pool string) string {
	if pool == "" {
		return base
	}
	return base + "-" + pool
}
