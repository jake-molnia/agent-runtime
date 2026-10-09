package hatchetbridge

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
	"github.com/jake-molnia/agent-runtime/artifacts"
	"github.com/jake-molnia/agent-runtime/orchestration"
)

func offlineLifecycleClient(t *testing.T) *hatchet.Client {
	t.Helper()
	claims := `{"server_url":"http://127.0.0.1:1","grpc_broadcast_address":"127.0.0.1:1","exp":4102444800,"sub":"00000000-0000-0000-0000-000000000001"}`
	token := "offline." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".offline"
	t.Setenv("HATCHET_CLIENT_EMBEDDED_DATABASE_URL", "")
	t.Setenv("HATCHET_CLIENT_TLS_STRATEGY", "none")
	t.Setenv("HATCHET_CLIENT_SERVER_URL", "http://127.0.0.1:1")
	t.Setenv("HATCHET_CLIENT_HOST_PORT", "127.0.0.1:1")
	t.Setenv("HATCHET_CLIENT_NAMESPACE", "")
	client, err := hatchet.NewClient(hatchet.WithToken(token), hatchet.WithHostPort("127.0.0.1", 1), hatchet.WithNamespace(""))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close(context.Background()) })
	return client
}

func testLifecycle() Lifecycle {
	return Lifecycle{
		Name: "offline-lifecycle",
		Resolve: func(_ context.Context, input Input, _ string) (Spec, error) {
			return Spec{Agent: input.Agent, Digest: input.Digest, Run: input.Run}, nil
		},
		Definition: func(Spec) (orchestration.Definition, error) { return orchestration.Definition{}, nil },
	}
}

type lifecycleContext struct {
	hatchet.DurableContext
	input   Input
	outputs map[string]any
	errors  map[string]string
	ctx     context.Context
	runID   string
}

func (ctx lifecycleContext) GetContext() context.Context {
	if ctx.ctx != nil {
		return ctx.ctx
	}
	return context.Background()
}
func (ctx lifecycleContext) WorkflowRunId() string {
	if ctx.runID != "" {
		return ctx.runID
	}
	return "trusted-run-id"
}
func (ctx lifecycleContext) StepRunErrors() map[string]string { return ctx.errors }
func (ctx lifecycleContext) WorkflowInput(target any) error {
	data, err := json.Marshal(ctx.input)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}
func (ctx lifecycleContext) StepOutput(name string, target any) error {
	output, exists := ctx.outputs[name]
	if !exists {
		return errors.New("step output unavailable")
	}
	data, err := json.Marshal(output)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

func lifecycleStatus(t *testing.T, output any) map[string]string {
	t.Helper()
	data, err := json.Marshal(output)
	if err != nil {
		t.Fatal(err)
	}
	var status map[string]string
	if err := json.Unmarshal(data, &status); err != nil {
		t.Fatal(err)
	}
	return status
}

func TestLifecycleRegistration(t *testing.T) {
	client := offlineLifecycleClient(t)
	for _, mode := range []string{"retained", "collected", "structured", "automation"} {
		t.Run(mode, func(t *testing.T) {
			engine := &orchestration.Engine{}
			lifecycle := testLifecycle()
			parents := map[string][]string{"resolve": nil, "provision": {"resolve"}, "execute": {"resolve", "provision"}}
			if mode != "retained" {
				engine.Artifacts = artifacts.Directory{Root: t.TempDir()}
				parents["collect"], parents["cleanup"] = []string{"resolve", "execute"}, []string{"resolve", "collect"}
			}
			if mode == "structured" || mode == "automation" {
				lifecycle.Validate = func(Spec, json.RawMessage) error { return nil }
				parents["validate"], parents["cleanup"] = []string{"resolve", "collect"}, []string{"resolve", "collect", "validate"}
			}
			if mode == "automation" {
				lifecycle.Automation = true
				lifecycle.Publish = func(context.Context, Spec, json.RawMessage) (json.RawMessage, error) {
					return json.RawMessage(`{}`), nil
				}
				parents["publish"], parents["cleanup"] = []string{"resolve", "validate"}, []string{"resolve", "collect", "validate", "publish"}
			}
			workflow, err := Build(client, engine, lifecycle)
			if err != nil {
				t.Fatal(err)
			}
			definition, regular, durable, onFailure := workflow.Dump()
			if len(definition.Tasks) != len(parents) || len(regular) != len(parents)-1 || len(durable) != 1 || onFailure == nil || definition.OnFailureTask == nil {
				t.Fatalf("incomplete task registration: %+v", definition)
			}
			for _, task := range definition.Tasks {
				wantParents, exists := parents[task.ReadableId]
				if !exists || !reflect.DeepEqual(task.Parents, wantParents) && !(len(task.Parents) == 0 && len(wantParents) == 0) {
					t.Errorf("task %s parents=%v, want %v", task.ReadableId, task.Parents, wantParents)
				}
				wantRetries := int32(0)
				if task.ReadableId == "collect" || task.ReadableId == "publish" || task.ReadableId == "cleanup" {
					wantRetries = 2
				}
				if task.Retries != wantRetries || task.IsDurable != (task.ReadableId == "execute") || task.Timeout == "" {
					t.Errorf("task %s retry/durable/timeout mismatch: %+v", task.ReadableId, task)
				}
			}
			if mode == "automation" {
				if len(definition.ConcurrencyArr) != 1 {
					t.Fatal("missing automation concurrency")
				}
				concurrency := definition.ConcurrencyArr[0]
				if concurrency.Expression != "input.group_key" || concurrency.GetMaxRuns() != 1 || concurrency.GetLimitStrategy().String() != "GROUP_ROUND_ROBIN" {
					t.Fatalf("unexpected concurrency: %+v", concurrency)
				}
			} else if len(definition.ConcurrencyArr) != 0 {
				t.Fatal("manual workflow unexpectedly throttled")
			}
		})
	}
}

func TestLifecycleBuildRejectsIncompleteRegistration(t *testing.T) {
	client := offlineLifecycleClient(t)
	for _, missing := range []string{"client", "engine", "name", "resolve", "definition", "artifacts", "validate", "publish", "publisher validation", "publisher artifacts"} {
		t.Run(missing, func(t *testing.T) {
			engine := &orchestration.Engine{Artifacts: artifacts.Directory{Root: t.TempDir()}}
			lifecycle := testLifecycle()
			lifecycle.Automation = true
			lifecycle.Validate = func(Spec, json.RawMessage) error { return nil }
			lifecycle.Publish = func(context.Context, Spec, json.RawMessage) (json.RawMessage, error) { return nil, nil }
			registrationClient := client
			switch missing {
			case "client":
				registrationClient = nil
			case "engine":
				engine = nil
			case "name":
				lifecycle.Name = ""
			case "resolve":
				lifecycle.Resolve = nil
			case "definition":
				lifecycle.Definition = nil
			case "artifacts":
				engine.Artifacts = nil
			case "validate":
				lifecycle.Validate = nil
			case "publish":
				lifecycle.Publish = nil
			case "publisher validation":
				lifecycle.Automation, lifecycle.Validate = false, nil
			case "publisher artifacts":
				lifecycle.Automation, engine.Artifacts = false, nil
			}
			if workflow, err := Build(registrationClient, engine, lifecycle); err == nil || workflow != nil {
				t.Fatalf("accepted registration missing %s", missing)
			}
		})
	}
}

func TestLifecycleResolvePinsIdentity(t *testing.T) {
	client := offlineLifecycleClient(t)
	lifecycle := testLifecycle()
	var received Input
	lifecycle.Resolve = func(_ context.Context, input Input, runID string) (Spec, error) {
		received = input
		if runID != "trusted-run-id" {
			t.Fatalf("resolver received run ID %q", runID)
		}
		return Spec{Agent: input.Agent, Digest: input.Digest, Run: input.Run}, nil
	}
	workflow, err := Build(client, &orchestration.Engine{}, lifecycle)
	if err != nil {
		t.Fatal(err)
	}
	_, functions, _, _ := workflow.Dump()
	input := Input{Agent: "pinned-agent", Digest: "pinned-digest", Run: orchestration.Request{Key: "untrusted-run-key", Prompt: "task"}}
	before := time.Now()
	for _, function := range functions {
		if !strings.HasSuffix(function.ActionID, ":resolve") {
			continue
		}
		output, err := function.Fn(lifecycleContext{input: input})
		if err != nil {
			t.Fatal(err)
		}
		spec := output.(Spec)
		if spec.Run.Key != "trusted-run-id" || received.Run.Key != "trusted-run-id" || spec.Agent != input.Agent || spec.Digest != input.Digest || spec.Run.SubmittedAt.Before(before) || spec.Run.SubmittedAt.After(time.Now()) {
			t.Fatalf("identity not pinned: %+v received=%+v", spec, received)
		}
		return
	}
	t.Fatal("resolve function unavailable")
}

func TestLifecycleResolveSubmissionValidation(t *testing.T) {
	stamp := time.Now().Add(-time.Hour).UTC()
	input := Input{Run: orchestration.Request{SubmittedAt: stamp, Key: "caller-key"}}
	spec, err := (Lifecycle{}).resolve(context.Background(), input, "run-id")
	if err != nil || spec.Run.Key != "run-id" || !spec.Run.SubmittedAt.Equal(stamp) {
		t.Fatalf("existing submission changed: %+v, %v", spec, err)
	}
	called := false
	lifecycle := Lifecycle{Resolve: func(context.Context, Input, string) (Spec, error) { called = true; return Spec{}, nil }}
	input.Run.SubmittedAt = time.Now().Add(time.Minute)
	if _, err := lifecycle.resolve(context.Background(), input, "run-id"); err == nil || called {
		t.Fatal("future submission reached resolver")
	}
	want := errors.New("resolution rejected")
	lifecycle.Resolve = func(context.Context, Input, string) (Spec, error) { return Spec{}, want }
	if _, err := lifecycle.resolve(context.Background(), Input{}, "run-id"); !errors.Is(err, want) {
		t.Fatalf("resolver error lost: %v", err)
	}
}

func TestLifecycleSkippedTasksHaveNoSideEffects(t *testing.T) {
	client := offlineLifecycleClient(t)
	lifecycle := testLifecycle()
	lifecycle.Automation = true
	lifecycle.Definition = func(Spec) (orchestration.Definition, error) {
		t.Fatal("skipped task resolved definition")
		return orchestration.Definition{}, nil
	}
	lifecycle.Validate = func(Spec, json.RawMessage) error { t.Fatal("skipped task validated output"); return nil }
	lifecycle.Publish = func(context.Context, Spec, json.RawMessage) (json.RawMessage, error) {
		t.Fatal("skipped task published")
		return nil, nil
	}
	workflow, err := Build(client, &orchestration.Engine{Artifacts: artifacts.Directory{Root: t.TempDir()}}, lifecycle)
	if err != nil {
		t.Fatal(err)
	}
	_, regular, durable, onFailure := workflow.Dump()
	ctx := lifecycleContext{outputs: map[string]any{"resolve": Spec{Skip: true}}}
	for _, function := range append(regular, durable...) {
		if strings.HasSuffix(function.ActionID, ":resolve") {
			continue
		}
		t.Run(function.ActionID, func(t *testing.T) {
			if _, err := function.Fn(ctx); err != nil {
				t.Fatal(err)
			}
		})
	}
	output, err := onFailure(ctx)
	if err != nil || lifecycleStatus(t, output)["status"] != "not_provisioned" {
		t.Fatalf("skipped failure output=%v err=%v", output, err)
	}
}

func TestLifecycleFailureWithoutResolvedSpec(t *testing.T) {
	client := offlineLifecycleClient(t)
	lifecycle := testLifecycle()
	lifecycle.Definition = func(Spec) (orchestration.Definition, error) {
		t.Fatal("unresolved failure used definition")
		return orchestration.Definition{}, nil
	}
	workflow, err := Build(client, &orchestration.Engine{}, lifecycle)
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, onFailure := workflow.Dump()
	output, err := onFailure(lifecycleContext{errors: map[string]string{"resolve": "rejected"}})
	if err != nil || lifecycleStatus(t, output)["status"] != "not_provisioned" {
		t.Fatalf("unresolved failure output=%v err=%v", output, err)
	}
}

func TestLifecyclePublishUsesResolvedSpec(t *testing.T) {
	client := offlineLifecycleClient(t)
	lifecycle := testLifecycle()
	lifecycle.Validate = func(Spec, json.RawMessage) error { return nil }
	spec := Spec{Agent: "resolved-agent", Digest: "resolved-digest", Run: orchestration.Request{Key: "resolved-key"}, Domain: json.RawMessage(`{"pinned":true}`)}
	data := json.RawMessage(`{"summary":"review","findings":[]}`)
	called := false
	lifecycle.Publish = func(_ context.Context, received Spec, output json.RawMessage) (json.RawMessage, error) {
		called = true
		if !reflect.DeepEqual(received, spec) || string(output) != string(data) {
			t.Fatalf("publication did not use pinned values: %+v %s", received, output)
		}
		return json.RawMessage(`{"status":"published"}`), nil
	}
	workflow, err := Build(client, &orchestration.Engine{Artifacts: artifacts.Directory{Root: t.TempDir()}}, lifecycle)
	if err != nil {
		t.Fatal(err)
	}
	_, functions, _, _ := workflow.Dump()
	ctx := lifecycleContext{input: Input{Agent: "caller-agent", Digest: "caller-digest", Run: orchestration.Request{Key: "caller-key"}}, outputs: map[string]any{"resolve": spec, "validate": Output{Data: data}}}
	for _, function := range functions {
		if !strings.HasSuffix(function.ActionID, ":publish") {
			continue
		}
		output, err := function.Fn(ctx)
		if err != nil || !called || lifecycleStatus(t, output)["status"] != "published" {
			t.Fatalf("publish output=%v err=%v called=%v", output, err, called)
		}
		return
	}
	t.Fatal("publish function unavailable")
}

func TestLifecycleValidationUsesArchivedOutput(t *testing.T) {
	client := offlineLifecycleClient(t)
	for _, rejected := range []bool{false, true} {
		t.Run(map[bool]string{false: "valid", true: "invalid"}[rejected], func(t *testing.T) {
			var calls []string
			engine, prepared := failureEngine(t, &calls)
			prepared.MessageID = "msg_prompt"
			original := engine.Transport
			engine.Transport = failureTransport(func(request *http.Request) (*http.Response, error) {
				if strings.HasSuffix(request.URL.Path, "/message") {
					calls = append(calls, "output")
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"msg_final","type":"assistant","finish":"stop","time":{"created":2,"completed":3},"content":[{"type":"text","text":"{\"answer\":true}"}]},{"id":"msg_prompt","type":"user","time":{"created":1},"content":[]}],"cursor":{"next":""}}`))}, nil
				}
				return original.RoundTrip(request)
			})
			lifecycle := testLifecycle()
			lifecycle.Definition = func(Spec) (orchestration.Definition, error) { return orchestration.Definition{Namespace: "test"}, nil }
			lifecycle.Validate = func(spec Spec, data json.RawMessage) error {
				calls = append(calls, "validate")
				if spec.Run.Key != "run-key" || string(data) != `{"answer":true}` {
					t.Fatalf("invalid validation input: %+v %s", spec, data)
				}
				if rejected {
					return errors.New("invalid structured output")
				}
				return nil
			}
			workflow, err := Build(client, engine, lifecycle)
			if err != nil {
				t.Fatal(err)
			}
			_, functions, _, onFailure := workflow.Dump()
			bounded, cancel := context.WithTimeout(context.Background(), time.Minute)
			defer cancel()
			ctx := lifecycleContext{ctx: bounded, input: Input{Run: orchestration.Request{Key: "caller-key"}}, outputs: map[string]any{"resolve": Spec{Run: orchestration.Request{Key: "run-key"}}, "execute": Output{Prepared: prepared}}}
			for _, function := range functions {
				if strings.HasSuffix(function.ActionID, ":collect") {
					output, err := function.Fn(ctx)
					if err != nil || output.(Output).Artifact == "" {
						t.Fatalf("collection output=%v err=%v", output, err)
					}
					ctx.outputs["collect"] = output
				}
			}
			if ctx.outputs["collect"] == nil {
				t.Fatal("collect function unavailable")
			}
			for _, function := range functions {
				if !strings.HasSuffix(function.ActionID, ":validate") {
					continue
				}
				output, err := function.Fn(ctx)
				if rejected {
					if err == nil || output != nil {
						t.Fatalf("failed SDK task unexpectedly exports output=%v err=%v", output, err)
					}
				} else if err != nil || output.(Output).Artifact == "" || string(output.(Output).Data) != `{"answer":true}` {
					t.Fatalf("collection output=%v err=%v", output, err)
				}
				if !reflect.DeepEqual(calls, []string{"export", "output", "validate"}) {
					t.Fatalf("collection order=%v", calls)
				}
				if rejected {
					ctx.runID, ctx.errors = "run-key", map[string]string{"validate": "invalid structured output"}
					failureOutput, err := onFailure(ctx)
					status := lifecycleStatus(t, failureOutput)
					if err != nil || status["status"] != "cleaned" || status["artifact"] != ctx.outputs["collect"].(Output).Artifact {
						t.Fatalf("invalid validation lost archived output: %v, %v", failureOutput, err)
					}
					if !reflect.DeepEqual(calls, []string{"export", "output", "validate", "interrupt", "delete"}) {
						t.Fatalf("invalid validation cleanup=%v", calls)
					}
					archive, err := os.ReadFile(filepath.Join(engine.Artifacts.(artifacts.Directory).Root, status["artifact"]))
					if err != nil || string(archive) != `{"failed":true}` {
						t.Fatalf("archive missing after sandbox deletion: %s, %v", archive, err)
					}
				}
				return
			}
			t.Fatal("validate function unavailable")
		})
	}
}

func TestLifecycleFailureResolvesPinnedDefinition(t *testing.T) {
	client := offlineLifecycleClient(t)
	lifecycle := testLifecycle()
	spec := Spec{Agent: "pinned-agent", Digest: "historic-digest", Run: orchestration.Request{Key: "pinned-key"}}
	lifecycle.Definition = func(received Spec) (orchestration.Definition, error) {
		if !reflect.DeepEqual(received, spec) {
			t.Fatalf("failure used caller definition: %+v", received)
		}
		return orchestration.Definition{}, errors.New("snapshot unavailable")
	}
	workflow, err := Build(client, &orchestration.Engine{}, lifecycle)
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, onFailure := workflow.Dump()
	output, err := onFailure(lifecycleContext{input: Input{Agent: "caller-agent", Digest: "caller-digest"}, outputs: map[string]any{"resolve": spec}})
	status := lifecycleStatus(t, output)
	if err != nil || status["status"] != "retained_until_expiry" || status["reason"] != "snapshot_unavailable" {
		t.Fatalf("failure output=%v err=%v", output, err)
	}
}

func TestLifecycleFailureExportsWithoutStepErrors(t *testing.T) {
	client := offlineLifecycleClient(t)
	var calls []string
	engine, prepared := failureEngine(t, &calls)
	lifecycle := testLifecycle()
	lifecycle.Definition = func(spec Spec) (orchestration.Definition, error) {
		if spec.Agent != "pinned-agent" || spec.Digest != "pinned-digest" {
			t.Fatalf("failure definition was not pinned: %+v", spec)
		}
		return orchestration.Definition{Namespace: "test"}, nil
	}
	workflow, err := Build(client, engine, lifecycle)
	if err != nil {
		t.Fatal(err)
	}
	_, _, _, onFailure := workflow.Dump()
	ctx := lifecycleContext{runID: "run-key", input: Input{Agent: "caller-agent", Run: orchestration.Request{Key: "caller-key"}}, outputs: map[string]any{
		"resolve":   Spec{Agent: "pinned-agent", Digest: "pinned-digest", Run: orchestration.Request{Key: "run-key"}},
		"provision": prepared,
	}}
	output, err := onFailure(ctx)
	status := lifecycleStatus(t, output)
	if err != nil || status["status"] != "cleaned" || status["artifact"] == "" {
		t.Fatalf("failure output=%v err=%v", output, err)
	}
	if !reflect.DeepEqual(calls, []string{"export", "interrupt", "delete"}) {
		t.Fatalf("failure cleanup order=%v", calls)
	}
}

func TestLifecycleTasksRequireResolvedOutput(t *testing.T) {
	client := offlineLifecycleClient(t)
	lifecycle := testLifecycle()
	lifecycle.Automation = true
	lifecycle.Validate = func(Spec, json.RawMessage) error { t.Fatal("missing resolved output reached validation"); return nil }
	lifecycle.Publish = func(context.Context, Spec, json.RawMessage) (json.RawMessage, error) {
		t.Fatal("missing resolved output reached publication")
		return nil, nil
	}
	workflow, err := Build(client, &orchestration.Engine{Artifacts: artifacts.Directory{Root: t.TempDir()}}, lifecycle)
	if err != nil {
		t.Fatal(err)
	}
	_, regular, durable, _ := workflow.Dump()
	for _, function := range append(regular, durable...) {
		if strings.HasSuffix(function.ActionID, ":resolve") {
			continue
		}
		t.Run(function.ActionID, func(t *testing.T) {
			output, err := function.Fn(lifecycleContext{input: Input{Agent: "caller-agent", Run: orchestration.Request{Key: "caller-key"}}})
			if err == nil || output != nil {
				t.Fatalf("task accepted missing resolved output: %v, %v", output, err)
			}
		})
	}
}

func TestLifecycleValidationWithoutSchemaKeepsArchive(t *testing.T) {
	client := offlineLifecycleClient(t)
	lifecycle := testLifecycle()
	lifecycle.Validate = func(Spec, json.RawMessage) error { t.Fatal("unstructured agent output reached validation"); return nil }
	lifecycle.Structured = func(spec Spec) (bool, error) {
		if spec.Digest != "pinned-digest" {
			t.Fatal("structured check did not use resolved snapshot")
		}
		return false, nil
	}
	workflow, err := Build(client, &orchestration.Engine{Artifacts: artifacts.Directory{Root: t.TempDir()}}, lifecycle)
	if err != nil {
		t.Fatal(err)
	}
	_, functions, _, _ := workflow.Dump()
	collected := Output{Artifact: "already-archived.json"}
	ctx := lifecycleContext{outputs: map[string]any{"resolve": Spec{Digest: "pinned-digest"}, "collect": collected}}
	for _, function := range functions {
		if !strings.HasSuffix(function.ActionID, ":validate") {
			continue
		}
		output, err := function.Fn(ctx)
		if err != nil || !reflect.DeepEqual(output, collected) {
			t.Fatalf("unstructured validation output=%v err=%v", output, err)
		}
		return
	}
	t.Fatal("validate function unavailable")
}
