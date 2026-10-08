package hatchetbridge

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
	"github.com/jake-molnia/agent-runtime/artifacts"
	"github.com/jake-molnia/agent-runtime/orchestration"
	"github.com/jake-molnia/agent-runtime/sandbox"
	"github.com/jake-molnia/agent-runtime/tailnet"
	"github.com/jake-molnia/agent-runtime/telemetry"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

type failureContext struct {
	outputs map[string]Output
	hatchet.Context
	ctx      context.Context
	prepared orchestration.Prepared
	steps    map[string]string
	missing  bool
}

func (c failureContext) GetContext() context.Context      { return c.ctx }
func (c failureContext) WorkflowRunId() string            { return "run-key" }
func (c failureContext) StepRunErrors() map[string]string { return c.steps }
func (c failureContext) StepOutput(step string, target interface{}) error {
	if out, ok := c.outputs[step]; ok {
		data, _ := json.Marshal(out)
		return json.Unmarshal(data, target)
	}
	if step != "provision" || c.missing {
		return errors.New("step output unavailable")
	}
	data, _ := json.Marshal(c.prepared)
	return json.Unmarshal(data, target)
}

type failureTransport func(*http.Request) (*http.Response, error)

func (f failureTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type failureStore func(context.Context, string, io.Reader) (string, error)

func (f failureStore) Put(ctx context.Context, key string, r io.Reader) (string, error) {
	return f(ctx, key, r)
}

func TestFailureExportsBeforeCleanup(t *testing.T) {
	for _, exportFails := range []bool{false, true} {
		t.Run(map[bool]string{false: "stored", true: "store failure"}[exportFails], func(t *testing.T) {
			var calls []string
			engine, p := failureEngine(t, &calls)
			engine.Artifacts = failureStore(func(ctx context.Context, key string, r io.Reader) (string, error) {
				calls = append(calls, "store")
				data, err := io.ReadAll(r)
				if err != nil || string(data) != `{"failed":true}` || key != "run-key" || ctx.Err() != nil {
					t.Fatalf("bad export: %s %v key=%s ctx=%v", data, err, key, ctx.Err())
				}
				if exportFails {
					return "", errors.New("storage unavailable")
				}
				return "failed-session.json", nil
			})
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			out, err := failureHandler(engine, map[string]orchestration.Definition{"agent": {Namespace: "test"}})(failureContext{ctx: ctx, prepared: p, steps: map[string]string{"execute": "agent execution did not complete"}}, Input{Agent: "agent"})
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"export", "store", "interrupt", "delete"}
			if exportFails {
				want = []string{"export", "store"}
				if out["status"] != "retained_until_expiry" {
					t.Fatalf("output=%v", out)
				}
			} else if out["artifact"] != "failed-session.json" || out["status"] != "cleaned" {
				t.Fatalf("output=%v", out)
			}
			if !reflect.DeepEqual(calls, want) {
				t.Fatalf("calls=%v, want %v", calls, want)
			}
		})
	}
}

func failureEngine(t *testing.T, calls *[]string) (*orchestration.Engine, orchestration.Prepared) {
	t.Helper()
	tel, err := telemetry.New(context.Background(), telemetry.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = tel.Shutdown(context.Background()) })
	p := orchestration.Prepared{SessionID: "ses_test", Started: time.Now(), Lease: sandbox.Lease{Namespace: "test", Claim: sandbox.ClaimName("run-key"), UID: "uid", Host: "sandbox.test", Expires: time.Now().Add(time.Hour)}}
	sum := sha256.Sum256([]byte("run-key"))
	claim := &unstructured.Unstructured{Object: map[string]interface{}{"apiVersion": "extensions.agents.x-k8s.io/v1beta1", "kind": "SandboxClaim", "metadata": map[string]interface{}{"name": p.Lease.Claim, "namespace": "test", "uid": "uid", "annotations": map[string]interface{}{"agent-runtime/run-key": hex.EncodeToString(sum[:])}}, "status": map[string]interface{}{"sandbox": map[string]interface{}{"serviceFQDN": p.Lease.Host}}}}
	api := fake.NewSimpleDynamicClient(runtime.NewScheme(), claim)
	api.PrependReactor("delete", "sandboxclaims", func(action ktesting.Action) (bool, runtime.Object, error) {
		*calls = append(*calls, "delete")
		return false, nil, nil
	})
	engine := &orchestration.Engine{Control: &sandbox.Control{API: api}, Telemetry: tel, Artifacts: artifacts.Directory{Root: t.TempDir()}}
	engine.Transport = failureTransport(func(r *http.Request) (*http.Response, error) {
		if r.Context().Err() != nil {
			return nil, r.Context().Err()
		}
		if _, ok := r.Context().Deadline(); !ok {
			t.Error("failure operation has no deadline")
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/export"):
			*calls = append(*calls, "export")
		case strings.HasSuffix(r.URL.Path, "/interrupt"):
			*calls = append(*calls, "interrupt")
		default:
			t.Fatalf("unexpected request %s", r.URL)
		}
		if _, err := api.Resource(sandbox.Claims).Namespace("test").Get(r.Context(), p.Lease.Claim, metav1.GetOptions{}); err != nil {
			t.Errorf("sandbox removed before request: %v", err)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"failed":true}`)), Header: http.Header{}}, nil
	})
	return engine, p
}

func TestFailureRetentionAndFallbacks(t *testing.T) {
	tests := []struct {
		name                                                    string
		steps                                                   map[string]string
		missing, noStore, exportError, deleteError, revokeError bool
		want                                                    []string
		status                                                  string
	}{
		{name: "export HTTP failure", steps: map[string]string{"execute": "failed"}, exportError: true, want: []string{"export"}, status: "retained_until_expiry"},
		{name: "missing provision output", steps: map[string]string{"execute": "failed"}, missing: true, status: "retained_until_expiry"},
		{name: "collect exhausted retries", steps: map[string]string{"collect": "failed"}, status: "retained_until_expiry"},
		{name: "partial provisioning", steps: map[string]string{"provision": "failed", "execute": "cancelled"}, missing: true, want: []string{"interrupt", "delete"}, status: "cleaned"},
		{name: "no artifact store", steps: map[string]string{"execute": "failed"}, noStore: true, want: []string{"interrupt", "delete"}, status: "cleaned"},
		{name: "successful collect followed by failed cleanup", steps: map[string]string{"cleanup": "failed"}, want: []string{"interrupt", "delete"}, status: "cleaned"},
		{name: "cleanup fails after export", steps: map[string]string{"execute": "failed"}, deleteError: true, want: []string{"export", "interrupt", "delete"}, status: "cleanup_pending"},
		{name: "identity cleanup fails after deletion", steps: map[string]string{"execute": "failed"}, revokeError: true, want: []string{"export", "interrupt", "delete"}, status: "cleanup_pending"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls []string
			engine, p := failureEngine(t, &calls)
			if tt.noStore {
				engine.Artifacts = nil
			}
			if tt.exportError {
				original := engine.Transport
				engine.Transport = failureTransport(func(r *http.Request) (*http.Response, error) {
					res, err := original.RoundTrip(r)
					if res != nil && strings.HasSuffix(r.URL.Path, "/export") {
						res.StatusCode = 503
					}
					return res, err
				})
			}
			if tt.deleteError {
				engine.Control.API.(*fake.FakeDynamicClient).PrependReactor("delete", "sandboxclaims", func(ktesting.Action) (bool, runtime.Object, error) {
					calls = append(calls, "delete")
					return true, nil, errors.New("delete unavailable")
				})
			}
			if tt.revokeError {
				p.Hostname = p.Lease.Claim
				engine.Tailnet = &tailnet.Client{}
			}
			ctx := failureContext{ctx: context.Background(), prepared: p, steps: tt.steps, missing: tt.missing}
			out, err := failureHandler(engine, map[string]orchestration.Definition{"agent": {Namespace: "test"}})(ctx, Input{Agent: "agent"})
			if err != nil {
				t.Fatalf("error=%v", err)
			}
			if out["status"] != tt.status || !reflect.DeepEqual(calls, tt.want) {
				t.Fatalf("out=%v calls=%v, want status=%s calls=%v", out, calls, tt.status, tt.want)
			}
			if (tt.deleteError || tt.revokeError) && out["artifact"] == "" {
				t.Fatal("lost stored artifact reference")
			}
			if tt.revokeError {
				if _, err := engine.Control.API.Resource(sandbox.Claims).Namespace("test").Get(context.Background(), p.Lease.Claim, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
					t.Fatalf("claim not deleted: %v", err)
				}
			}
			if tt.status == "retained_until_expiry" || tt.deleteError {
				if _, err := engine.Control.API.Resource(sandbox.Claims).Namespace("test").Get(context.Background(), p.Lease.Claim, metav1.GetOptions{}); err != nil {
					t.Fatalf("claim was deleted: %v", err)
				}
			}
		})
	}
}

func TestFailureExportRetry(t *testing.T) {
	var calls []string
	engine, p := failureEngine(t, &calls)
	attempts := 0
	engine.Artifacts = failureStore(func(ctx context.Context, key string, r io.Reader) (string, error) {
		attempts++
		if attempts == 1 {
			return "", errors.New("store temporarily unavailable")
		}
		return "artifact.json", nil
	})
	handler := failureHandler(engine, map[string]orchestration.Definition{"agent": {Namespace: "test"}})
	ctx := failureContext{ctx: context.Background(), prepared: p, steps: map[string]string{"execute": "failed"}}
	out, err := handler(ctx, Input{Agent: "agent"})
	if err != nil || out["status"] != "retained_until_expiry" {
		t.Fatalf("out=%v err=%v", out, err)
	}
	out, err = handler(ctx, Input{Agent: "agent"})
	if err != nil || out["status"] != "cleaned" || out["artifact"] != "artifact.json" {
		t.Fatalf("out=%v err=%v", out, err)
	}
	if !reflect.DeepEqual(calls, []string{"export", "export", "interrupt", "delete"}) {
		t.Fatalf("calls=%v", calls)
	}
}

func TestExecutionFailuresRemainFailuresWithArtifacts(t *testing.T) {
	for _, outcome := range []string{"failed", "interrupted", "deadline", "cancelled"} {
		t.Run(outcome, func(t *testing.T) {
			var calls []string
			engine, p := failureEngine(t, &calls)
			original := engine.Transport
			engine.Transport = failureTransport(func(r *http.Request) (*http.Response, error) {
				var body string
				switch {
				case strings.HasSuffix(r.URL.Path, "/export"), strings.HasSuffix(r.URL.Path, "/interrupt"):
					return original.RoundTrip(r)
				case strings.HasSuffix(r.URL.Path, "/event"):
					body = "event: server.connected\ndata: {\"type\":\"server.connected\"}\n\n"
				case strings.HasSuffix(r.URL.Path, "/prompt"):
					body = "{}"
				case r.URL.Path == "/api/session/"+p.SessionID:
					body = `{"data":{"outcome":"` + outcome + `"}}`
				default:
					t.Errorf("unexpected request %s", r.URL)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
			})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			d := orchestration.Definition{Namespace: "test", Timeout: time.Hour}
			if outcome == "deadline" {
				p.Started = time.Now().Add(-2 * time.Hour)
			}
			if outcome == "cancelled" {
				cancel()
			}
			_, runErr := engine.Execute(ctx, d, orchestration.Request{Key: "run-key"}, p, nil)
			if runErr == nil {
				t.Fatal("execution failure became success")
			}
			if outcome == "deadline" && !errors.Is(runErr, context.DeadlineExceeded) {
				t.Fatalf("run error=%v", runErr)
			}
			if outcome == "cancelled" && !errors.Is(runErr, context.Canceled) {
				t.Fatalf("run error=%v", runErr)
			}
			out, err := failureHandler(engine, map[string]orchestration.Definition{"agent": d})(failureContext{ctx: ctx, prepared: p, steps: map[string]string{"execute": runErr.Error()}}, Input{Agent: "agent"})
			if err != nil || out["status"] != "cleaned" || out["artifact"] == "" {
				t.Fatalf("out=%v error=%v", out, err)
			}
			if !reflect.DeepEqual(calls, []string{"export", "interrupt", "delete"}) {
				t.Fatalf("calls=%v", calls)
			}
		})
	}
}

// Hatchet's DAG operator can send successful parent outputs without StepRunErrors.
func TestFailureWithoutStepErrors(t *testing.T) {
	for _, stage := range []string{"provision", "execute", "collect", "missing", "skipped descendants", "skipped provision"} {
		t.Run(stage, func(t *testing.T) {
			var calls []string
			engine, p := failureEngine(t, &calls)
			ctx := failureContext{ctx: context.Background(), prepared: p, outputs: map[string]Output{}}
			want := []string{"export", "interrupt", "delete"}
			status := "cleaned"
			switch stage {
			case "skipped descendants":
				ctx.outputs["execute"] = Output{}
				ctx.outputs["collect"] = Output{}
			case "skipped provision":
				ctx.outputs["collect"] = Output{}
				ctx.prepared = orchestration.Prepared{}
				want = nil
				status = "retained_until_expiry"
			case "execute":
				ctx.outputs["execute"] = Output{Prepared: p}
				want = nil
				status = "retained_until_expiry"
			case "collect":
				ctx.outputs["execute"] = Output{Prepared: p}
				ctx.outputs["collect"] = Output{Prepared: p, Artifact: "stored.json"}
				want = []string{"interrupt", "delete"}
			case "missing":
				ctx.missing = true
				want = nil
				status = "retained_until_expiry"
			}
			out, err := failureHandler(engine, map[string]orchestration.Definition{"agent": {Namespace: "test"}})(ctx, Input{Agent: "agent"})
			if err != nil || out["status"] != status || !reflect.DeepEqual(calls, want) {
				t.Fatalf("out=%v err=%v calls=%v want=%v", out, err, calls, want)
			}
			if stage == "collect" && out["artifact"] != "stored.json" {
				t.Fatalf("artifact reference lost: %v", out)
			}
		})
	}
}
