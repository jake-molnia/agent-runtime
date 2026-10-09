package hatchetbridge

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
	"github.com/jake-molnia/agent-runtime/agentexec"
	"github.com/jake-molnia/agent-runtime/definitions"
	"github.com/jake-molnia/agent-runtime/messages"
	"github.com/jake-molnia/agent-runtime/orchestration"
	"github.com/jake-molnia/agent-runtime/workflows"
)

type configuredTestContext struct {
	hatchet.DurableContext
	input   ConfiguredInput
	outputs map[string]any
	run     string
}

func (ctx configuredTestContext) GetContext() context.Context { return context.Background() }
func (ctx configuredTestContext) WorkflowRunId() string       { return ctx.run }
func (ctx configuredTestContext) WorkflowInput(target any) error {
	if ctx.input.Input == nil {
		data, err := json.Marshal(map[string]string{"digest": ctx.input.Digest})
		if err != nil {
			return err
		}
		return json.Unmarshal(data, target)
	}
	data, err := json.Marshal(ctx.input)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}
func (ctx configuredTestContext) StepOutput(name string, target any) error {
	value, exists := ctx.outputs[name]
	if !exists {
		return errors.New("step output unavailable")
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

type configuredTestCall struct {
	request    orchestration.Request
	message    messages.Message
	definition orchestration.Definition
	config     json.RawMessage
}

type configuredTestBackend struct {
	test      *testing.T
	calls     []configuredTestCall
	lifecycle map[string][]string
	outputs   map[string]json.RawMessage
	readError error
}

var _ agentexec.Backend = (*configuredTestBackend)(nil)

func newConfiguredTestBackend(test *testing.T) *configuredTestBackend {
	return &configuredTestBackend{test: test, lifecycle: map[string][]string{}, outputs: map[string]json.RawMessage{
		"review":      json.RawMessage(`{"findings":["review"],"count":1}`),
		"adversarial": json.RawMessage(`[false,"adversarial",null]`),
		"verify":      json.RawMessage(`"selected-output"`),
	}}
}
func (backend *configuredTestBackend) record(key, operation string) {
	backend.lifecycle[key] = append(backend.lifecycle[key], operation)
}
func (backend *configuredTestBackend) Provision(_ context.Context, definition orchestration.Definition, request orchestration.Request) (orchestration.Prepared, error) {
	backend.test.Helper()
	_, envelope, exists := strings.Cut(request.Prompt, "\n")
	if !exists {
		backend.test.Fatal("missing JSON prompt envelope")
	}
	var decoded struct {
		Message messages.Message `json:"message"`
	}
	if err := json.Unmarshal([]byte(envelope), &decoded); err != nil {
		backend.test.Fatal(err)
	}
	if len(decoded.Message.Parts) != 1 || decoded.Message.Parts[0].Name != "input" || decoded.Message.Parts[0].Kind != messages.Data {
		backend.test.Fatalf("expected whole JSON data input: %+v", decoded.Message)
	}
	config, err := definition.Config(nil)
	if err != nil {
		backend.test.Fatal(err)
	}
	backend.calls = append(backend.calls, configuredTestCall{request: request, message: decoded.Message, definition: definition, config: config})
	backend.record(request.Key, "provision")
	return orchestration.Prepared{SessionID: "session-" + request.Key}, nil
}
func (backend *configuredTestBackend) Execute(_ context.Context, _ orchestration.Definition, request orchestration.Request, _ orchestration.Prepared, _ func(context.Context, string) error) (orchestration.Result, error) {
	backend.record(request.Key, "execute")
	return orchestration.Result{}, nil
}
func (backend *configuredTestBackend) Collect(_ context.Context, request orchestration.Request, _ orchestration.Prepared) (string, error) {
	backend.record(request.Key, "collect")
	return "collected", nil
}
func (backend *configuredTestBackend) ReadOutput(_ context.Context, request orchestration.Request, _ orchestration.Prepared) (json.RawMessage, error) {
	backend.record(request.Key, "read")
	for _, call := range backend.calls {
		if call.request.Key == request.Key {
			return backend.outputs[call.message.TaskID], backend.readError
		}
	}
	return nil, errors.New("unknown execution key")
}
func (backend *configuredTestBackend) Cleanup(_ context.Context, request orchestration.Request, _ orchestration.Prepared) error {
	backend.record(request.Key, "cleanup")
	return nil
}
func (backend *configuredTestBackend) Cancel(_ context.Context, _ orchestration.Definition, request orchestration.Request) error {
	backend.record(request.Key, "cancel")
	return nil
}

func configuredTestCatalog() *definitions.Catalog {
	catalog := &definitions.Catalog{Agents: map[string]definitions.Agent{}, Profiles: map[string]definitions.Profile{
		"test": {Pool: "test-pool", Namespace: "test", Directory: "/workspace"},
	}}
	for _, name := range []string{"code-review", "adversarial-review", "verify"} {
		catalog.Agents[name] = definitions.Agent{Name: name, Version: 1, Description: "Generic test agent", Instructions: "Test instructions " + name,
			Model: definitions.Model{Provider: "test", ID: "test-model-" + name}, Execution: definitions.Execution{Profile: "test", TimeoutSeconds: 30},
			OutputSchema: "output.schema.json", Schema: json.RawMessage(`{}`),
		}
	}
	return catalog
}
func configuredTestWorkflow() workflows.Workflow {
	return workflows.Workflow{Name: "registration-supplied", Version: 1, Steps: map[string]workflows.Step{
		"review":      {Agent: "code-review", Input: workflows.InputRefs{Sources: []string{"input"}}},
		"adversarial": {Agent: "adversarial-review", Input: workflows.InputRefs{Sources: []string{"input"}}},
		"verify":      {Agent: "verify", Input: workflows.InputRefs{Sources: []string{"review", "adversarial"}, Multiple: true}},
	}, Output: "verify"}
}
func configuredTestCapture(test *testing.T, workflow workflows.Workflow, catalog *definitions.Catalog) workflows.Snapshot {
	test.Helper()
	plan, err := workflows.Capture(workflow, catalog)
	if err != nil {
		test.Fatal(err)
	}
	return plan
}

type configuredTestFixture struct {
	root      string
	store     messages.Directory
	plan      workflows.Snapshot
	backend   *configuredTestBackend
	workflow  *hatchet.Workflow
	callbacks map[string]func(hatchet.Context) (any, error)
	ctx       configuredTestContext
}

func configuredTestRegister(test *testing.T, plan workflows.Snapshot, root string, backend *configuredTestBackend, store messages.Directory) configuredTestFixture {
	test.Helper()
	if err := workflows.Save(root, plan); err != nil {
		test.Fatal(err)
	}
	workflow, err := RegisterConfiguredWorkflow(offlineLifecycleClient(test), backend, store, root, plan)
	if err != nil {
		test.Fatal(err)
	}
	return configuredTestFixture{root: root, store: store, plan: plan, backend: backend, workflow: workflow, callbacks: dagTestCallbacks(test, workflow),
		ctx: configuredTestContext{input: ConfiguredInput{Digest: plan.Digest, Input: json.RawMessage(`{"root":true}`)}, outputs: map[string]any{}, run: "configured-run"}}
}
func newConfiguredTestFixture(test *testing.T) configuredTestFixture {
	test.Helper()
	return configuredTestRegister(test, configuredTestCapture(test, configuredTestWorkflow(), configuredTestCatalog()), test.TempDir(), newConfiguredTestBackend(test), messages.Directory{Root: test.TempDir()})
}
func (fixture *configuredTestFixture) runStep(test *testing.T, id string) ConfiguredResult {
	test.Helper()
	output, err := fixture.callbacks[id](fixture.ctx)
	if err != nil {
		test.Fatalf("%s: %v", id, err)
	}
	fixture.ctx.outputs[id] = output
	if id == "resolve" {
		return ConfiguredResult{}
	}
	return output.(ConfiguredResult)
}
func configuredTestJSON(test *testing.T, got, want json.RawMessage) {
	test.Helper()
	var actual, expected any
	if err := json.Unmarshal(got, &actual); err != nil {
		test.Fatalf("invalid actual JSON %s: %v", got, err)
	}
	if err := json.Unmarshal(want, &expected); err != nil {
		test.Fatal(err)
	}
	if !reflect.DeepEqual(actual, expected) {
		test.Fatalf("JSON = %s, want %s", got, want)
	}
}

func TestConfiguredWorkflowDeclaration(t *testing.T) {
	fixture := newConfiguredTestFixture(t)
	declaration, regular, durable, failure := fixture.workflow.Dump()
	if declaration.Name != "_"+fixture.plan.Workflow.Name || declaration.Version != fixture.plan.Digest || len(regular) != 2 || len(durable) != 3 || failure != nil || len(declaration.Tasks) != 5 || len(declaration.EventTriggers) != 0 {
		t.Fatalf("unexpected configured graph: %+v", declaration)
	}
	parents := map[string][]string{"resolve": nil, "review": {"resolve"}, "adversarial": {"resolve"}, "verify": {"resolve", "review", "adversarial"}, "result": {"resolve", "verify"}}
	for _, task := range declaration.Tasks {
		want, exists := parents[task.ReadableId]
		if !exists || len(task.Parents) != len(want) || len(want) > 0 && !reflect.DeepEqual(task.Parents, want) {
			t.Errorf("%s parents = %v, want %v", task.ReadableId, task.Parents, want)
		}
		isDurable := task.ReadableId != "resolve" && task.ReadableId != "result"
		if task.Retries != 0 || task.IsDurable != isDurable || task.Action != "_"+fixture.plan.Workflow.Name+":"+task.ReadableId {
			t.Errorf("unexpected step policy: %+v", task)
		}
		if isDurable && (task.ScheduleTimeout == nil || *task.ScheduleTimeout != "3600s") {
			t.Errorf("missing durable schedule timeout: %+v", task)
		}
	}
}

func TestConfiguredWorkflowWholeJSONAndReplay(t *testing.T) {
	for _, root := range []string{`{"nested":{"value":[1,false,null]}}`, `"text root"`, `true`, `null`} {
		t.Run(root, func(t *testing.T) {
			fixture := newConfiguredTestFixture(t)
			fixture.ctx.input.Input = json.RawMessage(root)
			fixture.runStep(t, "resolve")
			fixture.runStep(t, "adversarial")
			fixture.runStep(t, "review")
			fixture.runStep(t, "verify")
			result := fixture.runStep(t, "result")
			configuredTestJSON(t, result.Value, fixture.backend.outputs["verify"])
			if result.Message != fixture.ctx.outputs["verify"].(ConfiguredResult).Message || result.Digest != fixture.plan.Digest {
				t.Fatal("result did not select configured output")
			}
			keys := map[string]bool{}
			for _, call := range fixture.backend.calls {
				if keys[call.request.Key] {
					t.Fatal("steps shared execution key")
				}
				keys[call.request.Key] = true
				want := json.RawMessage(root)
				if call.message.TaskID == "verify" {
					want = json.RawMessage(`[{"findings":["review"],"count":1},[false,"adversarial",null]]`)
				}
				configuredTestJSON(t, call.message.Parts[0].Data, want)
				if strings.Contains(string(call.config), `"tools"`) || strings.Contains(string(call.config), `"mcp"`) {
					t.Fatalf("unexpected default tools: %s", call.config)
				}
				if !reflect.DeepEqual(fixture.backend.lifecycle[call.request.Key], []string{"provision", "execute", "collect", "read", "cleanup"}) {
					t.Fatalf("step lifecycle = %v", fixture.backend.lifecycle[call.request.Key])
				}
			}
			if len(keys) != 3 {
				t.Fatalf("executed %d steps", len(keys))
			}
			for _, id := range []string{"resolve", "review", "adversarial", "verify", "result"} {
				previous := fixture.ctx.outputs[id]
				fixture.runStep(t, id)
				if !reflect.DeepEqual(previous, fixture.ctx.outputs[id]) {
					t.Fatalf("%s replay changed output", id)
				}
			}
			if len(fixture.backend.calls) != 3 {
				t.Fatal("replay reran backend")
			}
			fixture.ctx.run = "second-configured-run"
			fixture.ctx.outputs = map[string]any{}
			for _, id := range []string{"resolve", "review", "adversarial", "verify", "result"} {
				fixture.runStep(t, id)
			}
			if len(fixture.backend.calls) != 6 {
				t.Fatal("independent run reused another run's cached executions")
			}
			for _, call := range fixture.backend.calls[3:] {
				if keys[call.request.Key] {
					t.Fatal("independent workflow runs shared execution key")
				}
				keys[call.request.Key] = true
			}
		})
	}
}

func TestConfiguredWorkflowScalarAndListInputs(t *testing.T) {
	for _, multiple := range []bool{false, true} {
		t.Run(map[bool]string{false: "scalar", true: "singleton-list"}[multiple], func(t *testing.T) {
			workflow := configuredTestWorkflow()
			delete(workflow.Steps, "adversarial")
			workflow.Steps["verify"] = workflows.Step{Agent: "verify", Input: workflows.InputRefs{Sources: []string{"review"}, Multiple: multiple}}
			fixture := configuredTestRegister(t, configuredTestCapture(t, workflow, configuredTestCatalog()), t.TempDir(), newConfiguredTestBackend(t), messages.Directory{Root: t.TempDir()})
			fixture.backend.outputs["review"] = json.RawMessage(`false`)
			for _, id := range []string{"resolve", "review", "verify"} {
				fixture.runStep(t, id)
			}
			want := json.RawMessage(`false`)
			if multiple {
				want = json.RawMessage(`[false]`)
			}
			configuredTestJSON(t, fixture.backend.calls[1].message.Parts[0].Data, want)
		})
	}
}

func TestConfiguredWorkflowRejectsInvalidPinsAndInput(t *testing.T) {
	for _, name := range []string{"missing-digest", "malformed-digest", "wrong-name", "corrupt-pin", "malformed-input", "missing-input"} {
		t.Run(name, func(t *testing.T) {
			fixture := newConfiguredTestFixture(t)
			switch name {
			case "missing-digest":
				fixture.ctx.input.Digest = strings.Repeat("f", 64)
			case "malformed-digest":
				fixture.ctx.input.Digest = "../escape"
			case "wrong-name":
				workflow := configuredTestWorkflow()
				workflow.Name = "other-registration"
				other := configuredTestCapture(t, workflow, configuredTestCatalog())
				if err := workflows.Save(fixture.root, other); err != nil {
					t.Fatal(err)
				}
				fixture.ctx.input.Digest = other.Digest
			case "corrupt-pin":
				if err := os.WriteFile(filepath.Join(fixture.root, "workflows", fixture.plan.Digest+".json"), []byte(`{}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "malformed-input":
				fixture.ctx.input.Input = json.RawMessage(`{"broken":`)
			case "missing-input":
				fixture.ctx.input.Input = nil
			}
			if _, err := fixture.callbacks["resolve"](fixture.ctx); err == nil {
				t.Fatal("invalid pin or input accepted")
			}
			if len(fixture.backend.calls) != 0 {
				t.Fatal("invalid resolve ran backend")
			}
		})
	}
}

func TestConfiguredWorkflowRejectsMissingAndMalformedOutput(t *testing.T) {
	for _, name := range []string{"missing", "malformed", "read-error"} {
		t.Run(name, func(t *testing.T) {
			fixture := newConfiguredTestFixture(t)
			fixture.runStep(t, "resolve")
			switch name {
			case "missing":
				fixture.backend.outputs["review"] = nil
			case "malformed":
				fixture.backend.outputs["review"] = json.RawMessage(`{"broken":`)
			case "read-error":
				fixture.backend.readError = errors.New("missing output artifact")
			}
			if _, err := fixture.callbacks["review"](fixture.ctx); err == nil {
				t.Fatal("invalid output accepted")
			}
			if len(fixture.backend.calls) != 1 {
				t.Fatal("unexpected backend execution count")
			}
			key := fixture.backend.calls[0].request.Key
			if !reflect.DeepEqual(fixture.backend.lifecycle[key], []string{"provision", "execute", "collect", "read", "cleanup"}) {
				t.Fatalf("failed output leaked lifecycle: %v", fixture.backend.lifecycle[key])
			}
		})
	}
}

func TestConfiguredWorkflowRejectsForgedReferences(t *testing.T) {
	for _, name := range []string{"wrong-digest", "wrong-actor", "wrong-task", "wrong-parent", "cross-scope", "reference-digest", "result-wrong-task", "result-wrong-parent"} {
		t.Run(name, func(t *testing.T) {
			fixture := newConfiguredTestFixture(t)
			for _, id := range []string{"resolve", "review", "adversarial"} {
				fixture.runStep(t, id)
			}
			source, target := "review", "verify"
			if strings.HasPrefix(name, "result-") {
				fixture.runStep(t, "verify")
				source, target = "verify", "result"
			}
			result := fixture.ctx.outputs[source].(ConfiguredResult)
			message, err := fixture.store.Get(context.Background(), fixture.ctx.run, result.Message)
			if err != nil {
				t.Fatal(err)
			}
			switch name {
			case "wrong-digest":
				result.Digest = strings.Repeat("f", 64)
			case "reference-digest":
				result.Message.Digest = strings.Repeat("f", 64)
			case "cross-scope":
				fixture.ctx.run = "another-run"
			default:
				message.ID = "forged-" + name
				switch name {
				case "wrong-actor":
					message.From = messages.Actor{Agent: "intruder", Revision: "v1"}
				case "wrong-task", "result-wrong-task":
					message.TaskID = "adversarial"
				case "wrong-parent", "result-wrong-parent":
					message.InReplyTo = &messages.Reference{ID: "unrelated", Digest: strings.Repeat("a", 64)}
				}
				result.Message, err = fixture.store.Put(context.Background(), fixture.ctx.run, message)
				if err != nil {
					t.Fatal(err)
				}
			}
			fixture.ctx.outputs[source] = result
			before := len(fixture.backend.calls)
			if _, err := fixture.callbacks[target](fixture.ctx); err == nil {
				t.Fatal("forged reference accepted")
			}
			if len(fixture.backend.calls) != before {
				t.Fatal("forged reference ran backend")
			}
		})
	}
}

func TestConfiguredWorkflowRejectsSameActorStepSubstitution(t *testing.T) {
	for _, target := range []string{"verify", "result"} {
		t.Run(target, func(t *testing.T) {
			workflow := configuredTestWorkflow()
			for id, step := range workflow.Steps {
				step.Agent = "code-review"
				workflow.Steps[id] = step
			}
			fixture := configuredTestRegister(t, configuredTestCapture(t, workflow, configuredTestCatalog()), t.TempDir(), newConfiguredTestBackend(t), messages.Directory{Root: t.TempDir()})
			for _, id := range []string{"resolve", "review", "adversarial"} {
				fixture.runStep(t, id)
			}
			source := "review"
			if target == "result" {
				fixture.runStep(t, "verify")
				source = "verify"
			}
			fixture.ctx.outputs[source] = fixture.ctx.outputs["adversarial"]
			before := len(fixture.backend.calls)
			if _, err := fixture.callbacks[target](fixture.ctx); err == nil {
				t.Fatal("same actor output from another task accepted")
			}
			if len(fixture.backend.calls) != before {
				t.Fatal("substituted output ran backend")
			}
		})
	}
}

func TestConfiguredWorkflowPinnedRecoveryAfterCatalogChanges(t *testing.T) {
	catalog := configuredTestCatalog()
	old := configuredTestCapture(t, configuredTestWorkflow(), catalog)
	root := t.TempDir()
	if err := workflows.Save(root, old); err != nil {
		t.Fatal(err)
	}
	for name, agent := range catalog.Agents {
		agent.Instructions = "Changed instructions " + name
		agent.Model.ID = "changed-model"
		agent.Schema = json.RawMessage(`{"type":"object","required":["new-field"]}`)
		catalog.Agents[name] = agent
	}
	catalog.Profiles["test"] = definitions.Profile{Pool: "changed-pool", Namespace: "changed", Directory: "/changed"}
	current := configuredTestCapture(t, configuredTestWorkflow(), catalog)
	fixture := configuredTestRegister(t, current, root, newConfiguredTestBackend(t), messages.Directory{Root: t.TempDir()})
	fixture.ctx.input.Digest = old.Digest
	for _, id := range []string{"resolve", "review", "adversarial", "verify", "result"} {
		fixture.runStep(t, id)
	}
	for _, call := range fixture.backend.calls {
		name := old.Workflow.Steps[call.message.TaskID].Agent
		if call.definition.Pool != "test-pool" || call.definition.Model["id"] != "test-model-"+name || !strings.Contains(string(call.config), "Test instructions "+name) || strings.Contains(string(call.config), "Changed instructions") {
			t.Fatalf("current config replaced pinned config: %+v, %s", call.definition, call.config)
		}
	}
	if fixture.ctx.outputs["result"].(ConfiguredResult).Digest != old.Digest {
		t.Fatal("recovery used current workflow pin")
	}
}

func TestConfiguredWorkflowRegistrationOwnsImmutablePlan(t *testing.T) {
	fixture := newConfiguredTestFixture(t)
	fixture.plan.Workflow.Steps["verify"] = workflows.Step{Agent: "code-review", Input: workflows.InputRefs{Sources: []string{"input"}}}
	fixture.plan.Agents["review"] = definitions.Snapshot{}
	fixture.plan.Workflow.Output = "review"
	for _, id := range []string{"resolve", "review", "adversarial", "verify", "result"} {
		fixture.runStep(t, id)
	}
	configuredTestJSON(t, fixture.backend.calls[2].message.Parts[0].Data, json.RawMessage(`[{"findings":["review"],"count":1},[false,"adversarial",null]]`))
	configuredTestJSON(t, fixture.ctx.outputs["result"].(ConfiguredResult).Value, json.RawMessage(`"selected-output"`))
}
