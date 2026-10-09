package hatchetbridge

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"

	"github.com/jake-molnia/agent-runtime/definitions"
	"github.com/jake-molnia/agent-runtime/messages"
	"github.com/jake-molnia/agent-runtime/notebooks"
	"github.com/jake-molnia/agent-runtime/workflows"
)

func recurringFixture(t *testing.T) (configuredTestFixture, notebooks.Directory) {
	t.Helper()
	catalog := configuredTestCatalog()
	agent, _ := definitions.Builtin("daily-brief")
	agent.Model = definitions.Model{Provider: "test", ID: "test-model"}
	agent.Execution = definitions.Execution{Profile: "test", TimeoutSeconds: 30}
	catalog.Agents[agent.Name] = agent
	plan := configuredTestCapture(t, workflows.Workflow{Name: "daily-news", Version: 1,
		DefaultInput: json.RawMessage(`{"brief":"Explore current news."}`), Notebook: true,
		Schedule: &workflows.Schedule{Cron: "0 8 * * *", Timezone: "Europe/London"},
		Steps:    map[string]workflows.Step{"brief": {Agent: agent.Name, Input: workflows.InputRefs{Sources: []string{"input"}}}}, Output: "brief",
	}, catalog)
	root, notes := t.TempDir(), notebooks.Directory{Root: t.TempDir()}
	if err := workflows.Save(root, plan); err != nil {
		t.Fatal(err)
	}
	backend, store := newConfiguredTestBackend(t), messages.Directory{Root: t.TempDir()}
	workflow, err := RegisterConfiguredWorkflow(offlineLifecycleClient(t), backend, store, root, plan, notes)
	if err != nil {
		t.Fatal(err)
	}
	return configuredTestFixture{root: root, store: store, plan: plan, backend: backend, workflow: workflow,
		callbacks: dagTestCallbacks(t, workflow), ctx: configuredTestContext{
			input: ConfiguredInput{Digest: plan.Digest, Input: plan.Workflow.DefaultInput}, outputs: map[string]any{}, run: "run-1",
		}}, notes
}

func TestRecurringDeclarationPinsCronInputAndSerializesRuns(t *testing.T) {
	fixture, _ := recurringFixture(t)
	declaration, _, _, _ := fixture.workflow.Dump()
	if !reflect.DeepEqual(declaration.CronTriggers, []string{"CRON_TZ=Europe/London 0 8 * * *"}) || declaration.CronInput == nil {
		t.Fatalf("cron missing: %+v", declaration)
	}
	var input ConfiguredInput
	if err := json.Unmarshal([]byte(*declaration.CronInput), &input); err != nil {
		t.Fatal(err)
	}
	if input.Digest != fixture.plan.Digest {
		t.Fatal("scheduled run not pinned")
	}
	configuredTestJSON(t, input.Input, fixture.plan.Workflow.DefaultInput)
	if len(declaration.ConcurrencyArr) != 1 || declaration.ConcurrencyArr[0].GetMaxRuns() != 1 || declaration.ConcurrencyArr[0].GetLimitStrategy().String() != "CANCEL_NEWEST" {
		t.Fatalf("notebook concurrency missing: %+v", declaration.ConcurrencyArr)
	}
}

func TestRecurringNotebookSurvivesRunsAndOldReplay(t *testing.T) {
	fixture, notes := recurringFixture(t)
	run := func(id, note, status string) ConfiguredResult {
		t.Helper()
		fixture.ctx.run, fixture.ctx.outputs = id, map[string]any{}
		value, _ := json.Marshal(map[string]any{"status": status, "report": "A sourced report", "sources": []any{}, "notebook": note})
		fixture.backend.outputs["brief"] = value
		for _, step := range []string{"resolve", "brief"} {
			fixture.runStep(t, step)
		}
		return fixture.runStep(t, "result")
	}
	first := run("run-1", "Already covered release A", "completed")
	if first.NotebookRevision != 1 {
		t.Fatalf("first revision = %d", first.NotebookRevision)
	}
	second := run("run-2", "Covered releases A and B", "completed")
	if second.NotebookRevision != 2 || len(fixture.backend.calls) != 2 {
		t.Fatalf("second run: %+v", second)
	}
	configuredTestJSON(t, fixture.backend.calls[1].message.Parts[0].Data, json.RawMessage(`{"task":{"brief":"Explore current news."},"notebook":"Already covered release A"}`))
	// An old run reuses its original input and cached model result, even after notes advance.
	replayed := run("run-1", "This must not be generated", "completed")
	if !reflect.DeepEqual(first, replayed) || len(fixture.backend.calls) != 2 {
		t.Fatal("old replay reran model or changed result")
	}
	head, err := notes.Latest(context.Background(), fixture.plan.Workflow.Name)
	if err != nil || head.Revision != 2 || head.Notebook != "Covered releases A and B" {
		t.Fatalf("old replay regressed notes: %+v %v", head, err)
	}
	blocked := run("run-3", "Discard this incomplete draft", "blocked")
	head, err = notes.Latest(context.Background(), fixture.plan.Workflow.Name)
	if err != nil || blocked.NotebookRevision != 3 || head.Notebook != "Covered releases A and B" {
		t.Fatalf("blocked run overwrote notes: %+v %v", head, err)
	}
}

func TestRecurringFailureAndConcurrentBaselineDoNotOverwriteNotes(t *testing.T) {
	fixture, notes := recurringFixture(t)
	fixture.runStep(t, "resolve")
	fixture.backend.outputs["brief"] = json.RawMessage(`{"status":"completed","report":"Missing notebook","sources":[]}`)
	if _, err := fixture.callbacks["brief"](fixture.ctx); err == nil {
		t.Fatal("invalid result accepted")
	}
	head, err := notes.Latest(context.Background(), fixture.plan.Workflow.Name)
	if err != nil || head.Revision != 0 {
		t.Fatal("failed model result changed notes")
	}
	fixture.ctx.run = "run-2"
	fixture.ctx.outputs = map[string]any{}
	fixture.runStep(t, "resolve")
	fixture.backend.outputs["brief"] = json.RawMessage(`{"status":"completed","report":"ok","sources":[],"notebook":"new"}`)
	fixture.runStep(t, "brief")
	fixture.runStep(t, "result")
	// Simulate a delayed completion outside normal Hatchet serialization.
	_, err = notes.Commit(context.Background(), fixture.plan.Workflow.Name, "run-1", fixture.plan.Digest, fixture.backend.outputs["brief"])
	if !errors.Is(err, notebooks.ErrConflict) {
		t.Fatalf("stale notebook update: %v", err)
	}
}

func TestRecurringRequiresNotebookStore(t *testing.T) {
	fixture, _ := recurringFixture(t)
	if _, err := RegisterConfiguredWorkflow(offlineLifecycleClient(t), fixture.backend, fixture.store, fixture.root, fixture.plan); err == nil {
		t.Fatal("stateful workflow accepted without durable storage")
	}
}
