package orchestration

import (
	"context"
	"encoding/json"
	"errors"
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
