package hatchetbridge

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jake-molnia/agent-runtime/workflows"
)

func TestScheduleIdentitySurvivesRevisionChangesAndRemoval(t *testing.T) {
	fixture, notes := recurringFixture(t)
	client := offlineLifecycleClient(t)
	first, err := RegisterConfiguredSchedule(client, fixture.workflow, fixture.plan)
	if err != nil {
		t.Fatal(err)
	}
	firstDecl, _, _, _ := first.Dump()
	catalog := configuredTestCatalog()
	for _, snapshot := range fixture.plan.Agents {
		catalog.Agents[snapshot.Agent.Name] = snapshot.Agent
	}
	changed := fixture.plan.Workflow
	changed.DefaultInput = json.RawMessage(`{"brief":"A new task revision"}`)
	next := configuredTestCapture(t, changed, catalog)
	if err := workflows.Save(fixture.root, next); err != nil {
		t.Fatal(err)
	}
	child, err := RegisterConfiguredWorkflow(client, fixture.backend, fixture.store, fixture.root, next, ConfiguredOptions{NotebookStore: notes})
	if err != nil {
		t.Fatal(err)
	}
	second, err := RegisterConfiguredSchedule(client, child, next)
	if err != nil {
		t.Fatal(err)
	}
	secondDecl, _, _, _ := second.Dump()
	if firstDecl.Name != secondDecl.Name || firstDecl.Tasks[0].Action == secondDecl.Tasks[0].Action {
		t.Fatal("cron identity changed or revisions share a dispatch action")
	}
	oldChild, _, _, _ := fixture.workflow.Dump()
	newChild, _, _, _ := child.Dump()
	if oldChild.Name == newChild.Name || oldChild.ConcurrencyArr[0].GetName() != newChild.ConcurrencyArr[0].GetName() {
		t.Fatal("execution routing or notebook serialization lost across revisions")
	}
	changed.Schedule = nil
	disabled := configuredTestCapture(t, changed, catalog)
	disabledChild, err := RegisterConfiguredWorkflow(client, fixture.backend, fixture.store, fixture.root, disabled, ConfiguredOptions{NotebookStore: notes})
	if err != nil {
		t.Fatal(err)
	}
	third, err := RegisterConfiguredSchedule(client, disabledChild, disabled)
	if err != nil {
		t.Fatal(err)
	}
	thirdDecl, _, _, _ := third.Dump()
	if thirdDecl.Name != firstDecl.Name || len(thirdDecl.CronTriggers) != 0 {
		t.Fatal("removing schedule left another cron owner")
	}
	callbacks := dagTestCallbacks(t, first)
	wrong := fixture.ctx
	wrong.input.Digest = next.Digest
	if _, err := callbacks["dispatch-"+fixture.plan.Digest](wrong); err == nil {
		t.Fatal("dispatch accepted another revision's payload")
	}
}

func TestNotebookAndStepHooksKeepOriginalCallerInput(t *testing.T) {
	fixture, notes := recurringFixture(t)
	var seen json.RawMessage
	workflow, err := RegisterConfiguredWorkflow(offlineLifecycleClient(t), fixture.backend, fixture.store, fixture.root, fixture.plan, ConfiguredOptions{
		NotebookStore: notes,
		Hooks: []ConfiguredHooks{{BeforeStep: func(ctx context.Context, step ConfiguredStep) (context.Context, func(), error) {
			seen = append(json.RawMessage(nil), step.Initial...)
			return ctx, nil, nil
		}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	fixture.callbacks = dagTestCallbacks(t, workflow)
	fixture.backend.outputs["brief"] = json.RawMessage(`{"status":"completed","report":"ok","sources":[],"notebook":"notes"}`)
	fixture.runStep(t, "resolve")
	fixture.runStep(t, "brief")
	fixture.runStep(t, "result")
	configuredTestJSON(t, seen, fixture.ctx.input.Input)
}
