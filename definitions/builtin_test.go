package definitions

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestBuiltinCatalog(t *testing.T) {
	want := []string{
		"code-review", "verify", "adversarial-review",
		"ci-failure-analyst", "findings-triager", "security-reviewer", "app-pentester",
		"incident-analyst", "dependency-upgrader", "upstreamer", "researcher", "monitor",
		"price-watcher", "news-researcher", "daily-brief", "opportunity-scout",
	}
	if !reflect.DeepEqual(BuiltinNames(), want) {
		t.Fatalf("builtin catalog = %v, want %v", BuiltinNames(), want)
	}
	for _, name := range want {
		t.Run(name, func(t *testing.T) {
			agent, ok := Builtin(name)
			if !ok || agent.Name != name || agent.Version != 1 || agent.Description == "" || agent.Instructions == "" {
				t.Fatalf("incomplete builtin: %#v", agent)
			}
			if agent.Model != (Model{}) || agent.Execution != (Execution{}) || len(agent.Capabilities) != 0 || len(agent.MCP) != 0 {
				t.Fatalf("builtin grants deployment settings: %#v", agent)
			}
			originalSchema := string(agent.Schema)
			agent.Schema[0] = '!'
			agent.Skills["injected"] = "injected"
			again, _ := Builtin(name)
			if string(again.Schema) != originalSchema || len(again.Skills) != 0 {
				t.Fatal("builtin shares mutable state")
			}
		})
	}
}

func TestLegacyBuiltinInstructions(t *testing.T) {
	instructions := map[string]string{
		"code-review":        "# Code review\n\nReview the arbitrary input supplied with this task. Identify actionable correctness, safety, and maintainability concerns relevant to the requested scope. Ground conclusions in supplied evidence or evidence you can obtain with available approved tools. Distinguish observed facts from assumptions and report verification limits.",
		"verify":             "# Verification\n\nVerify the arbitrary input supplied with this task against its stated requirements and available evidence. Use available approved tools when they help establish the result. Explain what was checked, what the evidence supports, and what remains uncertain. Do not report an unperformed check as successful.",
		"adversarial-review": "# Adversarial review\n\nChallenge the arbitrary input supplied with this task. Look for unsupported assumptions, counterexamples, failure modes, and missing evidence within the requested scope. Test concerns against supplied evidence or evidence obtainable with available approved tools. Separate demonstrated problems from speculative risks.",
	}
	const suffix = "\n\nFollow the supplied task constraints. Use only tools actually available to you; do not claim access to unavailable tools or invent tool results. Return valid JSON suitable for the supplied task without assuming fixed result keys."
	for name, prefix := range instructions {
		agent, _ := Builtin(name)
		if agent.Instructions != prefix+suffix || string(agent.Schema) != "{}" {
			t.Fatalf("legacy contract changed for %s", name)
		}
	}
}

func TestTaskPresetOutputContract(t *testing.T) {
	catalog := loaded(t, defaultsFixture(t))
	for _, name := range BuiltinNames()[3:] {
		t.Run(name, func(t *testing.T) {
			snapshot, err := catalog.Snapshot(name)
			if err != nil {
				t.Fatal(err)
			}
			for _, status := range []string{"completed", "no_change", "blocked"} {
				output := `{"status":"` + status + `","report":"# Result\nEvidence and limits.","sources":[],"notebook":"Remember prior useful context."}`
				if err := snapshot.ValidateOutput(json.RawMessage(output)); err != nil {
					t.Fatalf("valid status %s rejected: %v", status, err)
				}
			}
			const sourced = `{"status":"completed","report":"Sourced answer","sources":[{"url":"https://example.com/release","title":"Release notes"}],"notebook":""}`
			if err := snapshot.ValidateOutput(json.RawMessage(sourced)); err != nil {
				t.Fatalf("sourced result rejected: %v", err)
			}
			var valid map[string]any
			if err := json.Unmarshal([]byte(sourced), &valid); err != nil {
				t.Fatal(err)
			}
			for _, field := range []string{"status", "report", "sources", "notebook"} {
				value := valid[field]
				delete(valid, field)
				output, _ := json.Marshal(valid)
				if err := snapshot.ValidateOutput(output); err == nil {
					t.Errorf("accepted missing %s", field)
				}
				valid[field] = value
			}
			for _, invalid := range []string{
				`null`, `[]`, `{}`,
				`{"status":"success","report":"Result","sources":[],"notebook":""}`,
				`{"status":"completed","report":"","sources":[],"notebook":""}`,
				`{"status":"completed","report":"Result","sources":null,"notebook":""}`,
				`{"status":"completed","report":"Result","sources":[{"url":"https://example.com"}],"notebook":""}`,
				`{"status":"completed","report":"Result","sources":[{"url":"","title":"Title"}],"notebook":""}`,
				`{"status":"completed","report":"Result","sources":[{"url":"https://example.com","title":"Title","invented":true}],"notebook":""}`,
				`{"status":"completed","report":"Result","sources":[],"notebook":{}}`,
				`{"status":"completed","report":"Result","sources":[],"notebook":"","extra":true}`,
			} {
				if err := snapshot.ValidateOutput(json.RawMessage(invalid)); err == nil {
					t.Errorf("invalid result accepted: %s", invalid)
				}
			}
		})
	}
}

func TestTaskPresetInheritanceAndSchemaOverride(t *testing.T) {
	root := defaultsFixture(t)
	writeFixture(t, root, "agents/my-research/agent.yaml", "extends: researcher\ndescription: Research our chosen subject.\n")
	catalog := loaded(t, root)
	base := catalog.Agents["researcher"]
	agent := catalog.Agents["my-research"]
	if agent.Instructions != base.Instructions || string(agent.Schema) != string(base.Schema) {
		t.Fatal("custom preset did not inherit instructions and result contract")
	}
	writeFixture(t, root, "agents/my-research/agent.yaml", "extends: researcher\noutput_schema: answer.json\n")
	writeFixture(t, root, "agents/my-research/answer.json", `{"type":"string"}`)
	snapshot, err := loaded(t, root).Snapshot("my-research")
	if err != nil {
		t.Fatal(err)
	}
	if err := snapshot.ValidateOutput(json.RawMessage(`"custom answer"`)); err != nil {
		t.Fatalf("custom result schema not applied: %v", err)
	}
	if err := snapshot.ValidateOutput(json.RawMessage(`{"status":"completed","report":"Result","sources":[],"notebook":""}`)); err == nil {
		t.Fatal("inherited schema remained active after override")
	}
}
