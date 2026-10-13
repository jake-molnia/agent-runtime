package hatchetbridge

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/jake-molnia/agent-runtime/workflows"
)

func TestScheduleIdentitySurvivesRevisionChangesAndRemoval(t *testing.T) {
	fixture, notes := recurringFixture(t)
	client := offlineLifecycleClient(t)
	first, err := RegisterConfiguredSchedule(client, fixture.backend, fixture.store, fixture.root, fixture.plan, ConfiguredOptions{NotebookStore: notes})
	if err != nil {
		t.Fatal(err)
	}
	firstDecl, regular, durable, _ := first.Dump()
	if len(regular) != 2 || len(durable) != len(fixture.plan.Workflow.Steps) {
		t.Fatal("schedule retains a durable waiting wrapper")
	}
	originalActions := map[string]bool{}
	for _, task := range firstDecl.Tasks {
		originalActions[task.Action] = true
	}
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
	generic, err := RegisterConfiguredWorkflow(client, fixture.backend, fixture.store, fixture.root, next, ConfiguredOptions{NotebookStore: notes})
	if err != nil {
		t.Fatal(err)
	}
	second, err := RegisterConfiguredSchedule(client, fixture.backend, fixture.store, fixture.root, next, ConfiguredOptions{NotebookStore: notes})
	if err != nil {
		t.Fatal(err)
	}
	secondDecl, _, _, _ := second.Dump()
	if firstDecl.Name != secondDecl.Name {
		t.Fatal("cron identity changed across revisions")
	}
	for _, task := range secondDecl.Tasks {
		if originalActions[task.Action] {
			t.Fatalf("schedule revisions share action %s", task.Action)
		}
	}
	oldGeneric, _, _, _ := fixture.workflow.Dump()
	newGeneric, _, _, _ := generic.Dump()
	if oldGeneric.Name == newGeneric.Name || !reflect.DeepEqual(oldGeneric.ConcurrencyArr, newGeneric.ConcurrencyArr) || !reflect.DeepEqual(firstDecl.ConcurrencyArr, secondDecl.ConcurrencyArr) || !reflect.DeepEqual(newGeneric.ConcurrencyArr, secondDecl.ConcurrencyArr) {
		t.Fatal("execution routing or notebook serialization lost across revisions and entrypoints")
	}
	changed.Schedule = nil
	disabled := configuredTestCapture(t, changed, catalog)
	third, err := RegisterConfiguredSchedule(client, fixture.backend, fixture.store, fixture.root, disabled, ConfiguredOptions{NotebookStore: notes})
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
	if _, err := callbacks[ConfiguredWorkflowName("resolve", fixture.plan.Digest)](wrong); err == nil {
		t.Fatal("scheduled resolve accepted another revision's payload")
	}
}

func TestNotebookAndStepHooksKeepOriginalCallerInput(t *testing.T) {
	fixture, notes := recurringFixture(t)
	var seen json.RawMessage
	workflow, err := RegisterConfiguredSchedule(offlineLifecycleClient(t), fixture.backend, fixture.store, fixture.root, fixture.plan, ConfiguredOptions{
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
	for _, step := range []string{"resolve", "brief", "result"} {
		name := ConfiguredWorkflowName(step, fixture.plan.Digest)
		output, err := fixture.callbacks[name](fixture.ctx)
		if err != nil {
			t.Fatalf("%s: %v", step, err)
		}
		fixture.ctx.outputs[name] = output
		if step == "result" && output.(ConfiguredResult).NotebookRevision != 1 {
			t.Fatalf("scheduled run did not commit notebook: %+v", output)
		}
	}
	configuredTestJSON(t, seen, fixture.ctx.input.Input)
}
