package orchestration

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jake-molnia/agent-runtime/opencode"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jake-molnia/agent-runtime/sandbox"
	"github.com/jake-molnia/agent-runtime/telemetry"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestProvisionPreservesClaimFailureWithoutPublishingItsPayload(t *testing.T) {
	secretCause := errors.New("server response contains private-provider-credential")
	api := fake.NewSimpleDynamicClient(runtime.NewScheme())
	api.PrependReactor("create", "sandboxclaims", func(ktesting.Action) (bool, runtime.Object, error) { return true, nil, secretCause })
	tel, err := telemetry.New(t.Context(), telemetry.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer tel.Shutdown(context.Background())
	engine := &Engine{Control: &sandbox.Control{API: api}, Telemetry: tel, SecretKey: []byte(strings.Repeat("x", 32))}
	_, err = engine.Provision(t.Context(), Definition{Namespace: "test", Pool: "warm", Timeout: time.Minute, Config: func(map[string]string) (json.RawMessage, error) { return json.RawMessage(`{}`), nil }}, Request{Key: "phase-proof"})
	var phase *ProvisionError
	if !errors.As(err, &phase) || phase.Phase() != "claim" || !errors.Is(err, secretCause) {
		t.Fatalf("claim cause/phase lost: %T", err)
	}
	if strings.Contains(err.Error(), "credential") || err.Error() != "provision claim failed" {
		t.Fatal("private claim diagnostic exposed")
	}
}

func TestProvisionMCPDiagnosticContainsOnlyFiniteReadinessDetails(t *testing.T) {
	client, err := opencode.New("http://private-runtime.invalid", nil, roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 403, Body: io.NopCloser(strings.NewReader("private-token-and-provider-response"))}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	cause := client.ReadyMCP(t.Context(), "/workspace", []string{"aperture"})
	failure := provisionFailure("mcp", cause)
	var phase *ProvisionError
	if !errors.As(failure, &phase) {
		t.Fatal("phase missing")
	}
	if !strings.Contains(phase.Diagnostic(), "server aperture") || !strings.Contains(phase.Diagnostic(), "http=403") || strings.Contains(phase.Diagnostic(), "private") {
		t.Fatal("readiness diagnostic leaked or lost safe details")
	}
	if !errors.Is(failure, cause) {
		t.Fatal("underlying readiness cause discarded")
	}
}
