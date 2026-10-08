package agentexec

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jake-molnia/agent-runtime/definitions"
	"github.com/jake-molnia/agent-runtime/messages"
	"github.com/jake-molnia/agent-runtime/orchestration"
	"github.com/jake-molnia/agent-runtime/sandbox"
)

var _ Backend = (*orchestration.Engine)(nil)

type fakeBackend struct {
	test     *testing.T
	calls    []string
	requests []orchestration.Request
	prepared []orchestration.Prepared
	failures map[string]error
	output   json.RawMessage
	execute  func(context.Context, orchestration.Prepared, func(context.Context, string) error) error
	inspect  func(orchestration.Definition)
}

func newFake(test *testing.T) *fakeBackend {
	return &fakeBackend{test: test, failures: make(map[string]error), output: json.RawMessage(`{"value":"ok"}`)}
}

func (backend *fakeBackend) record(ctx context.Context, operation string, bound time.Duration) error {
	backend.test.Helper()
	backend.calls = append(backend.calls, operation)
	if bound != 0 {
		deadline, exists := ctx.Deadline()
		remaining := time.Until(deadline)
		if !exists || remaining <= 0 || remaining > bound {
			backend.test.Fatalf("%s deadline: %v, remaining %s", operation, exists, remaining)
		}
	}
	if operation == "cleanup" || operation == "cancel" || operation == "collect" {
		if ctx.Err() != nil {
			backend.test.Fatalf("%s inherited cancellation", operation)
		}
	}
	return backend.failures[operation]
}

func (backend *fakeBackend) Provision(ctx context.Context, definition orchestration.Definition, request orchestration.Request) (orchestration.Prepared, error) {
	backend.requests = append(backend.requests, request)
	if backend.inspect != nil {
		backend.inspect(definition)
	}
	prepared := orchestration.Prepared{SessionID: "session-" + request.Key, Lease: sandbox.Lease{Sandbox: "sandbox-" + request.Key}}
	backend.prepared = append(backend.prepared, prepared)
	return prepared, backend.record(ctx, "provision", 3*time.Minute)
}

func (backend *fakeBackend) Execute(ctx context.Context, definition orchestration.Definition, request orchestration.Request, prepared orchestration.Prepared, wait func(context.Context, string) error) (orchestration.Result, error) {
	if err := backend.record(ctx, "execute", definition.Timeout); err != nil {
		return orchestration.Result{}, err
	}
	if backend.execute != nil {
		if err := backend.execute(ctx, prepared, wait); err != nil {
			return orchestration.Result{}, err
		}
	} else if wait != nil {
		backend.test.Fatal("default execution installed an automatic interaction callback")
	}
	return orchestration.Result{Status: "succeeded", SessionID: prepared.SessionID}, nil
}

func (backend *fakeBackend) Collect(ctx context.Context, request orchestration.Request, prepared orchestration.Prepared) (string, error) {
	return "artifact-" + request.Key, backend.record(ctx, "collect", 3*time.Minute)
}

func (backend *fakeBackend) ReadOutput(ctx context.Context, request orchestration.Request, prepared orchestration.Prepared) (json.RawMessage, error) {
	return backend.output, backend.record(ctx, "read", 0)
}

func (backend *fakeBackend) Cleanup(ctx context.Context, request orchestration.Request, prepared orchestration.Prepared) error {
	return backend.record(ctx, "cleanup", 45*time.Second)
}

func (backend *fakeBackend) Cancel(ctx context.Context, definition orchestration.Definition, request orchestration.Request) error {
	if request.Key != backend.requests[len(backend.requests)-1].Key {
		backend.test.Fatal("cancel lost execution identity")
	}
	return backend.record(ctx, "cancel", 45*time.Second)
}

func snapshotFixture(test *testing.T, name string, schema json.RawMessage) definitions.Snapshot {
	test.Helper()
	agent := definitions.Agent{
		Name: name, Version: 1, Description: "Fake-backend test agent", Instructions: "Return the test result.",
		Model:     definitions.Model{Provider: "test", ID: "fake-model"},
		Execution: definitions.Execution{Profile: "test", TimeoutSeconds: 60},
		Skills:    map[string]string{"test-skill": "Test-only skill."}, Schema: schema,
	}
	if len(schema) != 0 {
		agent.OutputSchema = "output.schema.json"
	}
	catalog := definitions.Catalog{
		Agents:   map[string]definitions.Agent{name: agent},
		Profiles: map[string]definitions.Profile{"test": {Pool: "test", Namespace: "test", Directory: "/workspace", Tags: []string{"test"}}},
	}
	snapshot, err := catalog.Snapshot(name)
	if err != nil {
		test.Fatal(err)
	}
	return snapshot
}

func testSnapshot(test *testing.T) definitions.Snapshot {
	return snapshotFixture(test, "producer", json.RawMessage(`{"type":"object","required":["value"],"properties":{"value":{"type":"string"}},"additionalProperties":false}`))
}

func testDelivery(name, key string) messages.Delivery {
	return messages.Delivery{
		ExecutionID: key, Reference: messages.Reference{ID: "request", Digest: strings.Repeat("a", 64)},
		Message: messages.Message{Version: messages.Version, ID: "request", ContextID: "context", TaskID: "task", From: messages.Actor{Agent: "caller", Revision: "v1"}, To: name,
			Parts: []messages.Part{{Name: "request", Kind: messages.Text, Text: "untrusted task text"}}},
	}
}

func testExecutor(test *testing.T, backend Backend) *Executor {
	test.Helper()
	executor, err := New(backend)
	if err != nil {
		test.Fatal(err)
	}
	return executor
}

func TestNewRejectsNilBackend(test *testing.T) {
	var typedNil *fakeBackend
	for _, backend := range []Backend{nil, typedNil} {
		if _, err := New(backend); err == nil {
			test.Fatal("nil backend accepted")
		}
	}
}

func TestStagePinsSnapshotAndContracts(test *testing.T) {
	backend := newFake(test)
	executor := testExecutor(test, backend)
	snapshot := testSnapshot(test)
	actor := messages.Actor{Agent: snapshot.Agent.Name, Revision: snapshot.Agent.Digest}
	inputs := []messages.Contract{{Name: "request", Kind: messages.Text, Required: true}}
	stage, validator, err := executor.Stage(snapshot, inputs, "result")
	if err != nil {
		test.Fatal(err)
	}
	inputs[0].Name = "changed"
	snapshot.Agent.Name = "changed"
	snapshot.Agent.Instructions = "changed"
	snapshot.Agent.Skills["test-skill"] = "changed"
	snapshot.Agent.Schema[0] = '['
	snapshot.Profile.Tags[0] = "changed"
	stage.Inputs[0].Name = "mutated exposed contract"
	stage.Outputs[0].Schema = "changed"
	backend.inspect = func(definition orchestration.Definition) {
		config, err := definition.Config(nil)
		if err != nil || strings.Contains(string(config), "changed") || definition.Tags[0] != "test" {
			test.Fatalf("snapshot not pinned: %s %v", config, err)
		}
		var compiled map[string]json.RawMessage
		if err := json.Unmarshal(config, &compiled); err != nil {
			test.Fatal(err)
		}
		if !strings.Contains(string(compiled["permissions"]), `"effect":"deny"`) || definition.AllowProjectConfig {
			test.Fatalf("default deny-all policy lost: %s", config)
		}
	}
	parts, err := stage.Execute(context.Background(), testDelivery("producer", "execution-a"))
	if err != nil || len(parts) != 1 || stage.Actor != actor || parts[0].Schema != actor.Revision || parts[0].Kind != messages.Data || parts[0].Name != "result" {
		test.Fatalf("pinned output: %+v %v", parts, err)
	}
	if err := validator(parts[0].Data); err != nil {
		test.Fatal(err)
	}
	if err := validator(json.RawMessage(`{"value":1}`)); err == nil {
		test.Fatal("validator ignored output schema")
	}
	prompt, _ := testDelivery("producer", "execution-a").Prompt()
	if backend.requests[0].Key != "execution-a" || backend.requests[0].Prompt != prompt || backend.requests[0].SubmittedAt.IsZero() {
		test.Fatalf("incorrect execution request: %+v", backend.requests[0])
	}
}

func TestIndependentStagesUseDeliveryKeys(test *testing.T) {
	backend := newFake(test)
	executor := testExecutor(test, backend)
	for _, name := range []string{"producer", "consumer"} {
		snapshot := snapshotFixture(test, name, json.RawMessage(`{"type":"object"}`))
		stage, _, err := executor.Stage(snapshot, []messages.Contract{{Name: "request", Kind: messages.Text, Required: true}}, "result")
		if err != nil {
			test.Fatal(err)
		}
		if _, err := stage.Execute(context.Background(), testDelivery(name, "execution-"+name)); err != nil {
			test.Fatal(err)
		}
	}
	if backend.requests[0].Key == backend.requests[1].Key || backend.prepared[0].Lease.Sandbox == backend.prepared[1].Lease.Sandbox {
		test.Fatal("stages shared execution keys or sandboxes")
	}
}

func TestLifecycleFailures(test *testing.T) {
	secretErr := errors.New("secret credential must not leak")
	cleanupErr := errors.New("cleanup secret")
	for _, scenario := range []struct {
		name     string
		failures map[string]error
		output   string
		calls    []string
	}{
		{"success", nil, `{"value":"ok"}`, []string{"provision", "execute", "collect", "read", "cleanup"}},
		{"provision", map[string]error{"provision": secretErr}, "", []string{"provision", "cancel"}},
		{"cancel failure", map[string]error{"provision": secretErr, "cancel": cleanupErr}, "", []string{"provision", "cancel"}},
		{"execution exports first", map[string]error{"execute": secretErr}, "", []string{"provision", "execute", "collect", "cleanup"}},
		{"collection retains lease", map[string]error{"collect": secretErr}, "", []string{"provision", "execute", "collect"}},
		{"failed execution collection retains lease", map[string]error{"execute": cleanupErr, "collect": secretErr}, "", []string{"provision", "execute", "collect"}},
		{"invalid schema", nil, `{"value":12}`, []string{"provision", "execute", "collect", "read", "cleanup"}},
		{"invalid JSON", nil, `{`, []string{"provision", "execute", "collect", "read", "cleanup"}},
		{"read failure", map[string]error{"read": secretErr}, "", []string{"provision", "execute", "collect", "read", "cleanup"}},
		{"cleanup failure rejects success", map[string]error{"cleanup": cleanupErr}, `{"value":"ok"}`, []string{"provision", "execute", "collect", "read", "cleanup"}},
		{"execution joins cleanup", map[string]error{"execute": secretErr, "cleanup": cleanupErr}, "", []string{"provision", "execute", "collect", "cleanup"}},
		{"schema joins cleanup", map[string]error{"cleanup": cleanupErr}, `{"value":12}`, []string{"provision", "execute", "collect", "read", "cleanup"}},
	} {
		test.Run(scenario.name, func(test *testing.T) {
			backend := newFake(test)
			backend.failures, backend.output = scenario.failures, json.RawMessage(scenario.output)
			output, err := testExecutor(test, backend).Run(context.Background(), testSnapshot(test), testDelivery("producer", "execution"))
			if !reflect.DeepEqual(backend.calls, scenario.calls) {
				test.Fatalf("calls: %v, want %v", backend.calls, scenario.calls)
			}
			if scenario.name == "success" {
				if err != nil || string(output) != scenario.output {
					test.Fatalf("success: %s %v", output, err)
				}
				return
			}
			if err == nil || output != nil || strings.Contains(err.Error(), "secret") {
				test.Fatalf("failure exposed output or credentials: %s %v", output, err)
			}
			for _, cause := range scenario.failures {
				if !errors.Is(err, cause) {
					test.Fatalf("lost failure cause: %v", err)
				}
			}
		})
	}
}

func TestCancellationExportsAndCleansUp(test *testing.T) {
	backend := newFake(test)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	backend.execute = func(executeCtx context.Context, prepared orchestration.Prepared, wait func(context.Context, string) error) error {
		cancel()
		return executeCtx.Err()
	}
	output, err := testExecutor(test, backend).Run(ctx, testSnapshot(test), testDelivery("producer", "execution"))
	if output != nil || !errors.Is(err, context.Canceled) || !reflect.DeepEqual(backend.calls, []string{"provision", "execute", "collect", "cleanup"}) {
		test.Fatalf("cancellation: %s %v %v", output, err, backend.calls)
	}
}

func TestInteractionWaitForwarding(test *testing.T) {
	for _, fail := range []bool{false, true} {
		test.Run(map[bool]string{false: "success", true: "durable wait failure"}[fail], func(test *testing.T) {
			backend := newFake(test)
			waitErr := errors.New("wait failed")
			type contextKey struct{}
			ctx := WithInteractionWait(context.Background(), func(waitCtx context.Context, sessionID, kind string) error {
				if sessionID != "session-execution" || kind != "permission" || waitCtx.Value(contextKey{}) != "forwarded" {
					test.Fatalf("wait routing: %s %s", sessionID, kind)
				}
				if fail {
					return waitErr
				}
				return nil
			})
			backend.execute = func(executeCtx context.Context, prepared orchestration.Prepared, wait func(context.Context, string) error) error {
				if wait == nil {
					test.Fatal("durable wait callback missing")
				}
				return wait(context.WithValue(executeCtx, contextKey{}, "forwarded"), "permission")
			}
			_, err := testExecutor(test, backend).Run(ctx, testSnapshot(test), testDelivery("producer", "execution"))
			if fail && !errors.Is(err, waitErr) || !fail && err != nil {
				test.Fatalf("wait result: %v", err)
			}
		})
	}
}

func TestInvalidDeliveryHasNoBackendEffects(test *testing.T) {
	for name, mutate := range map[string]func(*messages.Delivery){
		"empty execution ID":     func(delivery *messages.Delivery) { delivery.ExecutionID = "" },
		"empty recipient":        func(delivery *messages.Delivery) { delivery.Message.To = "" },
		"wrong recipient":        func(delivery *messages.Delivery) { delivery.Message.To = "other" },
		"bad reference":          func(delivery *messages.Delivery) { delivery.Reference.Digest = "bad" },
		"invalid input":          func(delivery *messages.Delivery) { delivery.Message.Parts[0].Kind = "bad" },
		"contract mismatch":      func(delivery *messages.Delivery) { delivery.Message.Parts[0].Name = "other" },
		"missing required input": func(delivery *messages.Delivery) { delivery.Message.Parts = nil },
		"duplicate parts": func(delivery *messages.Delivery) {
			delivery.Message.Parts = append(delivery.Message.Parts, delivery.Message.Parts[0])
		},
		"unsupported file": func(delivery *messages.Delivery) {
			delivery.Message.Parts = []messages.Part{{Name: "request", Kind: messages.File, Attachment: &messages.Attachment{Reference: delivery.Reference, Size: 1, MediaType: "text/plain"}}}
		},
	} {
		test.Run(name, func(test *testing.T) {
			backend := newFake(test)
			kind := messages.Text
			if name == "unsupported file" {
				kind = messages.File
			}
			stage, _, err := testExecutor(test, backend).Stage(testSnapshot(test), []messages.Contract{{Name: "request", Kind: kind, Required: true}}, "result")
			if err != nil {
				test.Fatal(err)
			}
			delivery := testDelivery("producer", "execution")
			mutate(&delivery)
			if _, err := stage.Execute(context.Background(), delivery); err == nil || len(backend.calls) != 0 {
				test.Fatalf("invalid delivery executed: %v %v", err, backend.calls)
			}
		})
	}
}

func TestStageRejectsInvalidSnapshotsAndContracts(test *testing.T) {
	backend := newFake(test)
	executor := testExecutor(test, backend)
	inputs := []messages.Contract{{Name: "request", Kind: messages.Text, Required: true}}
	for _, snapshot := range []definitions.Snapshot{{}, snapshotFixture(test, "producer", nil)} {
		if _, _, err := executor.Stage(snapshot, inputs, "result"); err == nil {
			test.Fatal("invalid snapshot accepted")
		}
	}
	snapshot := testSnapshot(test)
	snapshot.Agent.Instructions = "tampered"
	if _, _, err := executor.Stage(snapshot, inputs, "result"); err == nil {
		test.Fatal("tampered identity accepted")
	}
	snapshot = testSnapshot(test)
	snapshot.Agent.Schema = json.RawMessage(`{"type":"unsupported"}`)
	if _, _, err := executor.Stage(snapshot, inputs, "result"); err == nil {
		test.Fatal("invalid schema accepted")
	}
	for _, contracts := range [][]messages.Contract{nil, {{Name: "request", Kind: "bad"}}, {{Name: "request", Kind: messages.Text, Schema: "bad"}}, {{Name: "request", Kind: messages.Data}}, {inputs[0], inputs[0]}} {
		if _, _, err := executor.Stage(testSnapshot(test), contracts, "result"); err == nil {
			test.Fatal("invalid contracts accepted")
		}
	}
	if _, _, err := executor.Stage(testSnapshot(test), inputs, ""); err == nil {
		test.Fatal("empty output name accepted")
	}
}

func TestPublicRunRejectsInvalidIdentityBeforeProvision(test *testing.T) {
	for _, scenario := range []string{"snapshot", "schema", "execution ID", "recipient", "message", "reference"} {
		test.Run(scenario, func(test *testing.T) {
			backend := newFake(test)
			snapshot := testSnapshot(test)
			delivery := testDelivery("producer", "execution")
			switch scenario {
			case "snapshot":
				snapshot.Agent.Digest = strings.Repeat("0", 64)
			case "schema":
				snapshot = snapshotFixture(test, "producer", nil)
			case "execution ID":
				delivery.ExecutionID = ""
			case "recipient":
				delivery.Message.To = "other"
			case "message":
				delivery.Message.Parts[0].Data = json.RawMessage(`{}`)
			case "reference":
				delivery.Reference.Digest = "bad"
			}
			if _, err := testExecutor(test, backend).Run(context.Background(), snapshot, delivery); err == nil || len(backend.calls) != 0 {
				test.Fatalf("invalid public invocation executed: %v %v", err, backend.calls)
			}
		})
	}
}

func TestCanceledProvisionStillCancelsPartialRun(test *testing.T) {
	backend := newFake(test)
	backend.failures["provision"] = context.Canceled
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := testExecutor(test, backend).Run(ctx, testSnapshot(test), testDelivery("producer", "execution"))
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(backend.calls, []string{"provision", "cancel"}) {
		test.Fatalf("partial provision cancellation: %v %v", err, backend.calls)
	}
}

func TestSuccessfulSessionMayRunAgainBeforeMessagePersistence(test *testing.T) {
	backend := newFake(test)
	executor := testExecutor(test, backend)
	snapshot := testSnapshot(test)
	delivery := testDelivery("producer", "execution")
	output, err := executor.Run(context.Background(), snapshot, delivery)
	if err != nil {
		test.Fatal(err)
	}
	output[0] = '['
	if backend.output[0] != '{' {
		test.Fatal("output aliases backend storage")
	}
	if _, err := executor.Run(context.Background(), snapshot, delivery); err != nil {
		test.Fatal(err)
	}
	if len(backend.requests) != 2 || backend.requests[0].Key != backend.requests[1].Key {
		test.Fatal("retry invented a new execution identity")
	}
}

func TestStageRegistersAndPersistsSchemaPinnedOutput(test *testing.T) {
	backend := newFake(test)
	snapshot := testSnapshot(test)
	stage, validator, err := testExecutor(test, backend).Stage(snapshot, []messages.Contract{{Name: "request", Kind: messages.Text, Required: true}}, "result")
	if err != nil {
		test.Fatal(err)
	}
	service, err := messages.New(messages.Directory{Root: test.TempDir()}, map[string]messages.Validator{snapshot.Agent.Digest: validator}, []messages.Stage{stage}, 0)
	if err != nil {
		test.Fatal(err)
	}
	ctx := context.Background()
	input, err := service.Publish(ctx, "scope", testDelivery("producer", "unused").Message)
	if err != nil {
		test.Fatal(err)
	}
	invocation := messages.Invocation{Stage: stage.Actor, Input: input, TaskID: "execute", To: "caller"}
	output, err := service.Invoke(ctx, "scope", invocation)
	if err != nil {
		test.Fatal(err)
	}
	message, err := service.Read(ctx, "scope", output)
	if err != nil || message.From != stage.Actor || len(message.Parts) != 1 || message.Parts[0].Schema != snapshot.Agent.Digest {
		test.Fatalf("persisted stage result: %+v %v", message, err)
	}
	if _, err := service.Invoke(ctx, "scope", invocation); err != nil || len(backend.requests) != 1 {
		test.Fatalf("persisted invocation replayed: %v", err)
	}
}
