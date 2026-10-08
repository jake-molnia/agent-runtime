package orchestration

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jake-molnia/agent-runtime/sandbox"
	"github.com/jake-molnia/agent-runtime/tailnet"
	"github.com/jake-molnia/agent-runtime/telemetry"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic/fake"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func cleanupEngine(t *testing.T, req Request, ready bool) (*Engine, sandbox.Lease) {
	t.Helper()
	lease := sandbox.Lease{Namespace: "agents", Claim: sandbox.ClaimName(req.Key), UID: "claim-uid"}
	sum := sha256.Sum256([]byte(req.Key))
	claim := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "extensions.agents.x-k8s.io/v1beta1",
		"kind":       "SandboxClaim",
		"metadata": map[string]any{
			"name": lease.Claim, "namespace": lease.Namespace, "uid": string(lease.UID),
			"annotations": map[string]any{"agent-runtime/run-key": hex.EncodeToString(sum[:])},
		},
	}}
	if ready {
		lease.Host = "sandbox.agents.svc"
		claim.Object["status"] = map[string]any{"sandbox": map[string]any{"name": "sandbox", "serviceFQDN": lease.Host}}
	}
	tel, err := telemetry.New(t.Context(), telemetry.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := tel.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	return &Engine{
		Control:   &sandbox.Control{API: fake.NewSimpleDynamicClient(runtime.NewScheme(), claim)},
		Telemetry: tel,
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.Method != "POST" || !strings.HasSuffix(req.URL.Path, "/interrupt") {
				t.Errorf("unexpected sandbox request: %s %s", req.Method, req.URL)
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader("{}")), Header: http.Header{}}, nil
		}),
	}, lease
}

func assertClaimDeleted(t *testing.T, e *Engine, lease sandbox.Lease) {
	t.Helper()
	_, err := e.Control.API.Resource(sandbox.Claims).Namespace(lease.Namespace).Get(t.Context(), lease.Claim, metav1.GetOptions{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("claim still exists or lookup failed: %v", err)
	}
}

func TestCancelWithoutTailnet(t *testing.T) {
	for _, ready := range []bool{false, true} {
		for _, tags := range [][]string{nil, {}} {
			t.Run(fmt.Sprintf("ready=%t/nil_tags=%t", ready, tags == nil), func(t *testing.T) {
				req := Request{Key: "untagged-run", SubmittedAt: time.Now()}
				e, lease := cleanupEngine(t, req, ready)
				secretCalls, httpCalls := 0, 0
				e.Tailnet = &tailnet.Client{
					ClientSecret: func(context.Context) (string, error) {
						secretCalls++
						return "", errors.New("not configured")
					},
					HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
						httpCalls++
						return nil, errors.New("unexpected tailnet request")
					})},
				}
				if err := e.Cancel(t.Context(), Definition{Namespace: lease.Namespace, Tags: tags}, req); err != nil {
					t.Errorf("Cancel returned %v", err)
				}
				assertClaimDeleted(t, e, lease)
				if secretCalls != 0 || httpCalls != 0 {
					t.Errorf("tailnet called for untagged run: secrets=%d HTTP=%d", secretCalls, httpCalls)
				}
			})
		}
	}
}

func TestTaggedRunCleanup(t *testing.T) {
	for _, mode := range []string{"cancel-ready", "cancel-partial-provision", "cleanup-prepared"} {
		t.Run(mode, func(t *testing.T) {
			req := Request{Key: "tagged-run", SubmittedAt: time.Now()}
			e, lease := cleanupEngine(t, req, mode != "cancel-partial-provision")
			scope, scopeErr := tailnet.NewScope("test-deployment", []string{lease.Namespace})
			if scopeErr != nil {
				t.Fatal(scopeErr)
			}
			hostname := (&tailnet.Client{Scope: scope}).Hostname(lease.Claim)
			var calls []string
			e.Tailnet = &tailnet.Client{
				Scope:        scope,
				ClientSecret: func(context.Context) (string, error) { return "test-secret", nil },
				HTTP: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
					call := req.Method + " " + req.URL.Path
					calls = append(calls, call)
					body := "{}"
					switch call {
					case "POST /api/v2/oauth/token":
						body = `{"access_token":"test-token","expires_in":3600}`
					case "GET /api/v2/tailnet/-/devices":
						body = fmt.Sprintf(`{"devices":[{"id":"owned-device","hostname":%q,"tags":["tag:agent-sandbox"]},{"id":"unrelated-device","hostname":"other","tags":["tag:agent-sandbox"]}]}`, hostname)
					case "DELETE /api/v2/device/owned-device", "DELETE /api/v2/tailnet/-/keys/issued-key":
					default:
						t.Errorf("unexpected tailnet request: %s", call)
					}
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
				})},
			}
			want := []string{"POST /api/v2/oauth/token"}
			var err error
			if mode == "cleanup-prepared" {
				session, message := ids(req.Key)
				err = e.Cleanup(t.Context(), req, Prepared{Lease: lease, SessionID: session, MessageID: message, IdentityID: "issued-key", Hostname: sandbox.ClaimName(req.Key), Started: req.SubmittedAt})
				want = append(want, "DELETE /api/v2/tailnet/-/keys/issued-key")
			} else {
				err = e.Cancel(t.Context(), Definition{Namespace: lease.Namespace, Tags: []string{"tag:agent-sandbox"}}, req)
			}
			if err != nil {
				t.Fatal(err)
			}
			assertClaimDeleted(t, e, lease)
			want = append(want, "GET /api/v2/tailnet/-/devices", "DELETE /api/v2/device/owned-device")
			if !reflect.DeepEqual(calls, want) {
				t.Errorf("tailnet calls = %v, want %v", calls, want)
			}
		})
	}
}
