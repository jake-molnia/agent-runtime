package agentexec

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jake-molnia/agent-runtime/definitions"
)

func TestOutputValidationDiagnosticDoesNotEchoValues(t *testing.T) {
	agent, _ := definitions.Builtin("researcher")
	agent.Model = definitions.Model{Provider: "test", ID: "test"}
	agent.Execution = definitions.Execution{Profile: "test", TimeoutSeconds: 30}
	catalog := &definitions.Catalog{Agents: map[string]definitions.Agent{agent.Name: agent}, Profiles: map[string]definitions.Profile{"test": {Pool: "test", Namespace: "test", Directory: "/workspace"}}}
	snapshot, err := catalog.Snapshot(agent.Name)
	if err != nil {
		t.Fatal(err)
	}
	cause := snapshot.ValidateOutput(json.RawMessage(`{"status":"completed","report":{"secret":"private-result"},"sources":[],"notebook":""}`))
	if cause == nil {
		t.Fatal("bad report accepted")
	}
	message := outputValidationError(cause).Error()
	if !strings.Contains(message, "/report") || !strings.Contains(message, "type") || strings.Contains(message, "private-result") {
		t.Fatalf("unsafe or unhelpful error: %s", message)
	}
}
