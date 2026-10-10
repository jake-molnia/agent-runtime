package hatchetbridge

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jake-molnia/agent-runtime/definitions"
	"github.com/jake-molnia/agent-runtime/messages"
	"github.com/jake-molnia/agent-runtime/workflows"
)

func TestRuntimeOwnedReviewPackExecutesWithoutCopiedAgentFiles(t *testing.T) {
	catalog := configuredTestCatalog()
	for _, name := range definitions.BuiltinNames() {
		if !strings.HasPrefix(name, "pr-") {
			continue
		}
		agent, _ := definitions.Builtin(name)
		agent.Model = definitions.Model{Provider: "test", ID: "test-model"}
		agent.Execution = definitions.Execution{Profile: "test", TimeoutSeconds: 30}
		catalog.Agents[name] = agent
	}
	plan := configuredTestCapture(t, workflows.Workflow{Name: "review-change", Use: "pr-review"}, catalog)
	backend := newConfiguredTestBackend(t)
	report := json.RawMessage(`{"summary":"Checked the pinned change","findings":[],"limitations":""}`)
	for _, step := range []string{"review", "adversarial", "security", "dependencies", "verify"} {
		backend.outputs[step] = report
	}
	backend.outputs["triage"] = json.RawMessage(`{"summary":"No eligible findings","groups":[],"limitations":""}`)
	backend.outputs["writeup"] = json.RawMessage(`{"assessment":"No demonstrated issues in the supplied evidence.","findings":[]}`)
	fixture := configuredTestRegister(t, plan, t.TempDir(), backend, messages.Directory{Root: t.TempDir()})
	fixture.runStep(t, "resolve")
	order, err := plan.Order()
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range order {
		fixture.runStep(t, id)
	}
	result := fixture.runStep(t, "result")
	configuredTestJSON(t, result.Value, backend.outputs["writeup"])
	if len(backend.calls) != 7 {
		t.Fatalf("executed %d agents", len(backend.calls))
	}
	for _, call := range backend.calls {
		data := call.message.Parts[0].Data
		switch call.message.TaskID {
		case "verify":
			var input []json.RawMessage
			if err := json.Unmarshal(data, &input); err != nil || len(input) != 5 {
				t.Fatalf("verifier lost original evidence or a specialist: %s", data)
			}
			configuredTestJSON(t, input[0], fixture.ctx.input.Input)
		case "triage", "writeup":
			var input []json.RawMessage
			if err := json.Unmarshal(data, &input); err != nil || len(input) < 2 {
				t.Fatalf("invalid final-stage evidence: %s", data)
			}
			configuredTestJSON(t, input[1], backend.outputs["verify"])
		default:
			configuredTestJSON(t, data, fixture.ctx.input.Input)
		}
	}
	fixture.runStep(t, "resolve")
	for _, id := range order {
		fixture.runStep(t, id)
	}
	fixture.runStep(t, "result")
	if len(backend.calls) != 7 {
		t.Fatal("replay reran completed review agents")
	}
}
