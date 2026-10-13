package hatchetbridge

import (
	"errors"
	"time"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
	"github.com/jake-molnia/agent-runtime/workflows"
)

// Keep cron ownership stable while agent execution routes to an immutable revision.
// Register even without a schedule to remove a previous version's cron triggers.
func RegisterConfiguredSchedule(client *hatchet.Client, child *hatchet.Workflow, plan workflows.Snapshot) (*hatchet.Workflow, error) {
	if client == nil || child == nil {
		return nil, errors.New("configured schedule dependencies required")
	}
	if err := plan.Verify(); err != nil {
		return nil, err
	}
	options := []hatchet.WorkflowOption{hatchet.WithWorkflowVersion(plan.Digest)}
	if plan.Workflow.Schedule != nil {
		expression, err := plan.Workflow.Schedule.Expression()
		if err != nil {
			return nil, err
		}
		options = append(options, hatchet.WithWorkflowCron(expression), hatchet.WithWorkflowCronInput(ConfiguredInput{Digest: plan.Digest, Input: plan.Workflow.DefaultInput}))
	}
	workflow := client.NewWorkflow("schedule-"+plan.Workflow.Name, options...)
	dispatchName := "dispatch-" + plan.Digest
	workflow.NewDurableTask(dispatchName, func(ctx hatchet.DurableContext, input ConfiguredInput) (ConfiguredResult, error) {
		if input.Digest != plan.Digest {
			return ConfiguredResult{}, errors.New("scheduled input requires its pinned workflow revision")
		}
		result, err := runChildWorkflow(ctx, client, child, input, nil)
		if err != nil {
			return ConfiguredResult{}, err
		}
		var output ConfiguredResult
		err = result.TaskOutput("result").Into(&output)
		return output, err
	}, hatchet.WithRetries(0), hatchet.WithExecutionTimeout(24*time.Hour), hatchet.WithScheduleTimeout(time.Hour))
	return workflow, nil
}
