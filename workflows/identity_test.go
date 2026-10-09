package workflows

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/jake-molnia/agent-runtime/definitions"
)

func TestPinnedMCPBindings(t *testing.T) {
	workflow := captured(t).Workflow
	catalog := fixtureCatalog()
	catalog.MCPServers = map[string]definitions.MCPServer{"research": {URL: "https://mcp.example.test", Tools: []string{"lookup"}}, "unused": {URL: "https://unused.example.test", Tools: []string{"other"}}}
	agent := catalog.Agents["code-review"]
	agent.MCP = []string{"research"}
	catalog.Agents["code-review"] = agent
	profile := catalog.Profiles["code-review"]
	profile.MCP = []string{"research"}
	catalog.Profiles["code-review"] = profile
	original, err := Capture(workflow, catalog)
	if err != nil {
		t.Fatal(err)
	}
	if len(original.Agents["review"].MCPServers) != 1 {
		t.Fatal("unselected MCP bindings pinned")
	}
	root := t.TempDir()
	if err := Save(root, original); err != nil {
		t.Fatal(err)
	}
	catalog.MCPServers["research"] = definitions.MCPServer{URL: "https://new.example.test", Tools: []string{"new_tool"}}
	changed, err := Capture(workflow, catalog)
	if err != nil || changed.Digest == original.Digest {
		t.Fatalf("MCP changes not hashed: %v", err)
	}
	replay, err := Read(root, original.Digest)
	if err != nil || !reflect.DeepEqual(replay, original) {
		t.Fatalf("MCP replay changed: %v", err)
	}
	definition, err := replay.Agents["review"].Definition()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := definition.Config(map[string]string{"openai": "runtime-secret"}); err != nil {
		t.Fatal(err)
	}
	catalog.MCPServers["research"] = definitions.MCPServer{URL: "https://mcp.example.test", Tools: []string{"lookup"}}
	catalog.MCPServers["unused"] = definitions.MCPServer{URL: "https://other.example.test", Tools: []string{"changed"}}
	unselected, err := Capture(workflow, catalog)
	if err != nil || unselected.Digest != original.Digest {
		t.Fatalf("unselected MCP changes hashed: %v", err)
	}
}

func TestCanonicalSchemaIdentity(t *testing.T) {
	workflow := captured(t).Workflow
	captureSchema := func(raw string) Snapshot {
		t.Helper()
		catalog := fixtureCatalog()
		agent := catalog.Agents["verify"]
		agent.Schema = json.RawMessage(raw)
		catalog.Agents["verify"] = agent
		snapshot, err := Capture(workflow, catalog)
		if err != nil {
			t.Fatal(err)
		}
		return snapshot
	}
	original := captureSchema(`{"const":9007199254740992,"description":"constant"}`)
	formatted := captureSchema("{\n \"description\": \"constant\", \"const\": 9007199254740992\n}")
	if original.Digest != formatted.Digest {
		t.Fatal("JSON formatting changed canonical digest")
	}
	changed := captureSchema(`{"const":9007199254740993,"description":"constant"}`)
	if changed.Digest == original.Digest {
		t.Fatal("large JSON numbers lost in digest")
	}
}

func TestArbitraryStepAndAgentNames(t *testing.T) {
	catalog := fixtureCatalog()
	agent := catalog.Agents["verify"]
	delete(catalog.Agents, "verify")
	agent.Name = "custom-agent"
	catalog.Agents[agent.Name] = agent
	workflow := Workflow{Name: "custom-flow", Steps: map[string]Step{"assemble": {Agent: "custom-agent", Input: InputRefs{Sources: []string{"input"}}}}, Output: "assemble"}
	snapshot, err := Capture(workflow, catalog)
	if err != nil {
		t.Fatal(err)
	}
	order, err := snapshot.Order()
	if err != nil || !reflect.DeepEqual(order, []string{"assemble"}) {
		t.Fatalf("arbitrary names: %v %v", order, err)
	}
}

func TestBuiltinGenericSchemas(t *testing.T) {
	catalog := fixtureCatalog()
	for _, name := range definitions.BuiltinNames() {
		builtin, ok := definitions.Builtin(name)
		if !ok {
			t.Fatal("builtin missing")
		}
		builtin.Model = catalog.Agents[name].Model
		builtin.Execution = catalog.Agents[name].Execution
		catalog.Agents[name] = builtin
	}
	snapshot, err := Capture(captured(t).Workflow, catalog)
	if err != nil {
		t.Fatal(err)
	}
	for _, agent := range snapshot.Agents {
		if err := agent.ValidateOutput(json.RawMessage(`true`)); err != nil {
			t.Fatal(err)
		}
	}
}
