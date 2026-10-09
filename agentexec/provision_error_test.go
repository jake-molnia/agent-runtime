package agentexec

import (
	"errors"
	"github.com/jake-molnia/agent-runtime/orchestration"
	"testing"
)

func TestPublicProvisionErrorRetainsSafePhaseAndTypedCause(t *testing.T) {
	engine := &orchestration.Engine{}
	_, cause := engine.Provision(t.Context(), orchestration.Definition{}, orchestration.Request{})
	err := backendError{operation: "provision", cause: cause}
	if err.Error() != "agent provision failed during configuration" {
		t.Fatal(err.Error())
	}
	if !errors.Is(err, cause) {
		t.Fatal("typed provisioning cause discarded")
	}
}
