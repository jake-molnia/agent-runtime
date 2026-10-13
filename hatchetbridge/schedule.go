package hatchetbridge

import (
	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
	"github.com/jake-molnia/agent-runtime/agentexec"
	"github.com/jake-molnia/agent-runtime/messages"
	"github.com/jake-molnia/agent-runtime/workflows"
)

// Keep cron ownership stable while agent execution routes to an immutable revision.
// Register even without a schedule to remove a previous version's cron triggers.
func RegisterConfiguredSchedule(client *hatchet.Client, backend agentexec.Backend, store messages.Store, root string, plan workflows.Snapshot, settings ...ConfiguredOptions) (*hatchet.Workflow, error) {
	options := []hatchet.WorkflowOption{}
	if plan.Workflow.Schedule != nil {
		expression, err := plan.Workflow.Schedule.Expression()
		if err != nil {
			return nil, err
		}
		options = append(options, hatchet.WithWorkflowCron(expression), hatchet.WithWorkflowCronInput(ConfiguredInput{Digest: plan.Digest, Input: plan.Workflow.DefaultInput}))
	}
	taskName := func(id string) string { return ConfiguredWorkflowName(id, plan.Digest) }
	return registerConfiguredWorkflow(client, backend, store, root, plan, "schedule-"+plan.Workflow.Name, taskName, options, settings...)
}
