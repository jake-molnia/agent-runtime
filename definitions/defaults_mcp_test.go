package definitions

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const defaultsYAML = `defaults:
  model: {provider: openai, id: deployment-model}
  execution: {profile: review, timeout_seconds: 90}
`

const mcpYAML = `mcp_servers:
  broker:
    url: https://broker.example/mcp
    tools: [read_diff, fetch.issue]
  unused:
    url: https://unused.example/mcp
    tools: [other]
`

func defaultsFixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFixture(t, root, "deployment.yaml", deploymentYAML+defaultsYAML)
	return root
}

func mcpFixture(t *testing.T) string {
	t.Helper()
	root := defaultsFixture(t)
	deployment := strings.Replace(deploymentYAML, "    pool: reviewers", "    mcp: [broker, unused]\n    pool: reviewers", 1)
	writeFixture(t, root, "deployment.yaml", deployment+defaultsYAML+mcpYAML)
	writeFixture(t, root, "agents/verify/agent.yaml", "mcp: [broker]\n")
	return root
}

func TestBuiltinsAndDeploymentDefaults(t *testing.T) {
	root := defaultsFixture(t)
	catalog := loaded(t, root)
	if len(catalog.Agents) != 3 || catalog.Defaults.Model.ID != "deployment-model" {
		t.Fatalf("defaults not resolved: %#v", catalog)
	}
	for _, name := range BuiltinNames() {
		base, exists := Builtin(name)
		if !exists || base.Model != (Model{}) || base.Execution != (Execution{}) || len(base.Capabilities) != 0 || len(base.MCP) != 0 || string(base.Schema) != "{}" {
			t.Fatalf("builtin contains deployment settings: %#v", base)
		}
		for _, forbidden := range []string{"github", "pull request", "summary", "findings", "gpt-", "/workspace"} {
			if strings.Contains(strings.ToLower(base.Instructions), forbidden) {
				t.Fatalf("builtin %s contains fixed content: %s", name, forbidden)
			}
		}
		snapshot, err := catalog.Snapshot(name)
		if err != nil {
			t.Fatal(err)
		}
		if snapshot.Agent.Model != catalog.Defaults.Model || snapshot.Agent.Execution != catalog.Defaults.Execution {
			t.Fatal("deployment defaults not inherited")
		}
		for _, output := range []string{`{}`, `{"arbitrary":[1,true]}`, `[]`, `null`, `"text"`, `42`, `false`} {
			if err := snapshot.ValidateOutput(json.RawMessage(output)); err != nil {
				t.Fatalf("%s rejects valid JSON %s: %v", name, output, err)
			}
		}
		for _, output := range []string{`{`, `{} {}`, ``} {
			if err := snapshot.ValidateOutput(json.RawMessage(output)); err == nil {
				t.Fatalf("invalid JSON accepted: %s", output)
			}
		}
	}
	if _, exists := Builtin("absent"); exists {
		t.Fatal("unknown builtin accepted")
	}
	names := BuiltinNames()
	names[0] = "mutated"
	base, _ := Builtin("verify")
	base.Schema[0] = '['
	base.Skills["injected"] = "injected"
	again, _ := Builtin("verify")
	if BuiltinNames()[0] != "code-review" || string(again.Schema) != "{}" || len(again.Skills) != 0 {
		t.Fatal("builtins share mutable state")
	}
	if err := catalog.Save(root); err != nil {
		t.Fatal(err)
	}
	for _, name := range BuiltinNames() {
		if _, err := ReadSnapshot(root, catalog.Agents[name].Digest); err != nil {
			t.Fatal(err)
		}
	}
}

func TestBuiltinOverrides(t *testing.T) {
	root := defaultsFixture(t)
	writeFixture(t, root, "agents/code-review/agent.yaml", "description: Custom review\nmodel: {id: custom-model}\nexecution: {timeout_seconds: 30}\n")
	writeFixture(t, root, "agents/audit/agent.yaml", "extends: verify\ndescription: Audit supplied work\n")
	writeFixture(t, root, "agents/audit/instructions.md", "Only these replacement instructions.")
	catalog := loaded(t, root)
	review := catalog.Agents["code-review"]
	base, _ := Builtin("code-review")
	if review.Description != "Custom review" || review.Model != (Model{Provider: "openai", ID: "custom-model"}) || review.Execution != (Execution{Profile: "review", TimeoutSeconds: 30}) || review.Instructions != base.Instructions {
		t.Fatalf("partial overrides failed: %#v", review)
	}
	audit := catalog.Agents["audit"]
	if audit.Extends != "verify" || audit.Name != "audit" || audit.Instructions != "Only these replacement instructions." || string(audit.Schema) != "{}" {
		t.Fatalf("inheritance failed: %#v", audit)
	}
	writeFixture(t, root, "agents/verify/agent.yaml", "output_schema: result.json\n")
	writeFixture(t, root, "agents/verify/result.json", `{"type":"string"}`)
	snapshot, err := loaded(t, root).Snapshot("verify")
	if err != nil {
		t.Fatal(err)
	}
	if err := snapshot.ValidateOutput(json.RawMessage(`{}`)); err == nil {
		t.Fatal("schema override not applied")
	}
}

func TestInvalidDefaultAndInheritanceReferences(t *testing.T) {
	for name, deployment := range map[string]string{
		"model":                 strings.Replace(defaultsYAML, "provider: openai, id: deployment-model", "provider: openai", 1),
		"profile":               strings.Replace(defaultsYAML, "profile: review", "profile: absent", 1),
		"timeout":               strings.Replace(defaultsYAML, "timeout_seconds: 90", "timeout_seconds: 0", 1),
		"unknown default field": defaultsYAML + "  tools: true\n",
	} {
		t.Run(name, func(t *testing.T) {
			root := defaultsFixture(t)
			writeFixture(t, root, "deployment.yaml", deploymentYAML+deployment)
			if _, err := Load(root); err == nil {
				t.Fatal("invalid defaults accepted")
			}
		})
	}
	for _, contents := range []string{"extends: absent\n", "extends: reviewer\n", "model: {id: ''}\n", "execution: {profile: ''}\n", "description: ''\n"} {
		root := defaultsFixture(t)
		writeFixture(t, root, "agents/verify/agent.yaml", contents)
		if _, err := Load(root); err == nil {
			t.Fatalf("invalid inherited override accepted: %s", contents)
		}
	}
	root := t.TempDir()
	writeFixture(t, root, "deployment.yaml", deploymentYAML)
	writeFixture(t, root, "agents/audit/agent.yaml", "extends: verify\nmodel: {provider: openai, id: explicit}\nexecution: {profile: review, timeout_seconds: 60}\n")
	if _, err := Load(root); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, root, "agents/audit/agent.yaml", "extends: verify\n")
	if _, err := Load(root); err == nil {
		t.Fatal("missing model/profile accepted without defaults")
	}
}

func TestMCPCompileExactReleasePolicy(t *testing.T) {
	catalog := loaded(t, mcpFixture(t))
	snapshot, err := catalog.Snapshot("verify")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.MCPServers) != 1 || snapshot.CompiledPolicy != compiledMCPPolicy {
		t.Fatalf("unselected bindings exposed: %#v", snapshot)
	}
	definition, err := snapshot.Definition()
	if err != nil {
		t.Fatal(err)
	}
	data, err := definition.Config(nil)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		MCP struct {
			Servers map[string]map[string]any `json:"servers"`
		} `json:"mcp"`
		Permissions []permission `json:"permissions"`
		Agents      map[string]struct {
			Disabled    bool         `json:"disabled"`
			Permissions []permission `json:"permissions"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"type": "remote", "url": "https://broker.example/mcp", "oauth": false, "codemode": false}
	if len(config.MCP.Servers) != 1 || !reflect.DeepEqual(config.MCP.Servers["broker"], want) {
		t.Fatalf("not pinned V2 MCP configuration: %s", data)
	}
	if len(config.Agents) != 2 || !config.Agents["title"].Disabled || config.Agents["authored"].Disabled {
		t.Fatalf("MCP execution must disable auxiliary title generation: %s", data)
	}
	rules := []permission{{Action: "*", Resource: "*", Effect: "deny"}, {Action: "broker_fetch_issue", Resource: "*", Effect: "allow"}, {Action: "broker_read_diff", Resource: "*", Effect: "allow"}}
	if !reflect.DeepEqual(config.Permissions, rules) || !reflect.DeepEqual(config.Agents["authored"].Permissions, rules) {
		t.Fatalf("unexpected permissions: %s", data)
	}
	for _, action := range []string{"shell", "read", "write", "edit", "opencode", "mcp_resource", "broker_other", "unused_other", "read_diff", "broker_read_diff_extra"} {
		effect := "ask"
		for _, rule := range config.Permissions {
			if rule.Action == "*" || rule.Action == action {
				effect = rule.Effect
			}
		}
		if effect != "deny" {
			t.Fatalf("unapproved action %s allowed", action)
		}
	}
	for _, forbidden := range []string{"unused.example", `"command"`, `"environment"`, `"client_secret"`, `"enabled"`} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("forbidden MCP config exposed: %s", forbidden)
		}
	}
}

func TestMCPStartupValidation(t *testing.T) {
	for name, deployment := range map[string]string{
		"unknown selected":        strings.Replace(mcpYAML, "  broker:", "  renamed:", 1),
		"unknown grant":           mcpYAML + "",
		"wildcard":                strings.Replace(mcpYAML, "read_diff", "'*'", 1),
		"duplicate tool":          strings.Replace(mcpYAML, "fetch.issue", "read_diff", 1),
		"normalization collision": strings.Replace(mcpYAML, "read_diff, fetch.issue", "fetch.issue, fetch_issue", 1),
		"cross-server collision":  mcpYAML + "  broker_read:\n    url: https://second.example/mcp\n    tools: [diff]\n",
		"empty tools":             strings.Replace(mcpYAML, "[read_diff, fetch.issue]", "[]", 1),
		"header credentials":      strings.Replace(mcpYAML, "    tools: [read_diff", "    headers: {Authorization: secret}\n    tools: [read_diff", 1),
		"oauth credentials":       strings.Replace(mcpYAML, "    tools: [read_diff", "    oauth: {client_secret: secret}\n    tools: [read_diff", 1),
		"stdio":                   strings.Replace(mcpYAML, "    url: https://broker.example/mcp", "    command: [sh, -c, evil]", 1),
	} {
		t.Run(name, func(t *testing.T) {
			root := mcpFixture(t)
			profile := strings.Replace(deploymentYAML, "    pool: reviewers", "    mcp: [broker, unused]\n    pool: reviewers", 1)
			if name == "unknown grant" {
				profile = strings.Replace(profile, "[broker, unused]", "[broker, missing]", 1)
			}
			writeFixture(t, root, "deployment.yaml", profile+defaultsYAML+deployment)
			if _, err := Load(root); err == nil {
				t.Fatal("unsafe registry accepted")
			}
		})
	}
	for _, selected := range []string{"[absent]", "[broker, broker]", "[https://broker.example/mcp]"} {
		root := mcpFixture(t)
		writeFixture(t, root, "agents/verify/agent.yaml", "mcp: "+selected+"\n")
		if _, err := Load(root); err == nil {
			t.Fatal("invalid selection accepted")
		}
	}
	root := mcpFixture(t)
	data, err := os.ReadFile(filepath.Join(root, "deployment.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, root, "deployment.yaml", strings.Replace(string(data), "mcp: [broker, unused]", "mcp: [unused]", 1))
	if _, err := Load(root); err == nil || !strings.Contains(err.Error(), "does not grant") {
		t.Fatalf("ungranted server accepted: %v", err)
	}
}

func TestMCPURLs(t *testing.T) {
	for _, endpoint := range []string{"http://broker.example/mcp", "http://localhost/mcp", "https://user:secret@broker.example/mcp", "https://broker.example/mcp?token=secret", "https://broker.example/mcp?", "https://broker.example/mcp#secret", "https://broker.example/mcp#", "file:///etc/passwd", "https:///missing", "https://:443/mcp", "javascript:alert(1)"} {
		t.Run(endpoint, func(t *testing.T) {
			if err := validateMCPServers(map[string]MCPServer{"broker": {URL: endpoint, Tools: []string{"read"}}}); err == nil {
				t.Fatal("unsafe URL accepted")
			}
		})
	}
	for _, endpoint := range []string{"https://broker.example/mcp", "http://127.0.0.1:9000/mcp", "http://[::1]:9000/mcp"} {
		if err := validateMCPServers(map[string]MCPServer{"broker": {URL: endpoint, Tools: []string{"read"}}}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMCPSnapshotPinning(t *testing.T) {
	root := mcpFixture(t)
	catalog := loaded(t, root)
	before, err := catalog.Snapshot("verify")
	if err != nil {
		t.Fatal(err)
	}
	store := t.TempDir()
	if err := SaveSnapshot(store, before); err != nil {
		t.Fatal(err)
	}
	for _, server := range []MCPServer{{URL: "https://changed.example/mcp", Tools: []string{"read_diff", "fetch.issue"}}, {URL: "https://broker.example/mcp", Tools: []string{"read_diff"}}} {
		catalog.MCPServers["broker"] = server
		after, err := catalog.Snapshot("verify")
		if err != nil {
			t.Fatal(err)
		}
		if after.Agent.Digest == before.Agent.Digest {
			t.Fatal("selected MCP change not pinned")
		}
	}
	catalog = loaded(t, root)
	catalog.MCPServers["unused"] = MCPServer{URL: "https://changed-unused.example/mcp", Tools: []string{"changed"}}
	after, err := catalog.Snapshot("verify")
	if err != nil || after.Agent.Digest != before.Agent.Digest {
		t.Fatalf("unselected server changed digest: %v", err)
	}
	catalog.MCPServers["broker"].Tools[0] = "mutated"
	if before.MCPServers["broker"].Tools[0] != "read_diff" {
		t.Fatal("snapshot shares registry tool slices")
	}
	pinned, err := ReadSnapshot(store, before.Agent.Digest)
	if err != nil || pinned.MCPServers["broker"].URL != "https://broker.example/mcp" {
		t.Fatalf("stored bindings not pinned: %v", err)
	}
	pinned.CompiledPolicy = "changed-policy"
	pinned.Agent.Digest, err = pinned.digest()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pinned.Definition(); err == nil {
		t.Fatal("changed compiler policy replayed")
	}
	pinned.CompiledPolicy = ""
	pinned.Agent.Digest, _ = pinned.digest()
	if _, err := pinned.Definition(); err == nil {
		t.Fatal("MCP snapshot without compiler policy replayed")
	}
}

func TestLegacySnapshotDigest(t *testing.T) {
	snapshot, err := loaded(t, fixture(t)).Snapshot("reviewer")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Agent.Digest != "fbaa71205ddceaa92966001ac2c129a5345c2ac2cbad12955aa82464a7dc733a" {
		t.Fatalf("legacy snapshot digest changed: %s", snapshot.Agent.Digest)
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"extends"`, `"mcp"`, `"mcp_servers"`, `"compiled_policy"`} {
		if strings.Contains(string(data), field) {
			t.Fatalf("empty new field changes legacy snapshot: %s", field)
		}
	}
}

func TestCredentialValuesNeverEnterSnapshots(t *testing.T) {
	for _, key := range []string{"key", "token", "password", "Authorization", "X-API-Key", "client_secret"} {
		t.Run(key, func(t *testing.T) {
			root := defaultsFixture(t)
			writeFixture(t, root, "deployment.yaml", strings.Replace(deploymentYAML, "X-Review: pinned", key+": private-value", 1)+defaultsYAML)
			if _, err := Load(root); err == nil {
				t.Fatal("literal credential accepted")
			}
		})
	}
	root := mcpFixture(t)
	secret := filepath.Join(t.TempDir(), "provider")
	writeFixture(t, filepath.Dir(secret), filepath.Base(secret), "private-value")
	data, err := os.ReadFile(filepath.Join(root, "deployment.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	deployment := strings.Replace(string(data), "secret_files: {}", "secret_files: {openai: "+secret+"}", 1)
	deployment = strings.Replace(deployment, "X-Review: pinned", "Authorization: {$secret: openai}", 1)
	writeFixture(t, root, "deployment.yaml", deployment)
	catalog := loaded(t, root)
	if err := catalog.Save(root); err != nil {
		t.Fatal(err)
	}
	stored, err := os.ReadFile(filepath.Join(root, "snapshots", catalog.Agents["verify"].Digest+".json"))
	if err != nil || strings.Contains(string(stored), "private-value") {
		t.Fatalf("credential leaked to snapshot: %v", err)
	}
}

func TestMCPDefinitionCopiesAndEmptySelection(t *testing.T) {
	root := mcpFixture(t)
	catalog := loaded(t, root)
	snapshot, err := catalog.Snapshot("verify")
	if err != nil {
		t.Fatal(err)
	}
	definition, err := snapshot.Definition()
	if err != nil {
		t.Fatal(err)
	}
	catalog.MCPServers["broker"].Tools[0] = "registry-mutated"
	if snapshot.MCPServers["broker"].Tools[0] != "read_diff" {
		t.Fatal("snapshot shares MCP registry")
	}
	snapshot.MCPServers["broker"].Tools[0] = "mutated"
	data, err := definition.Config(nil)
	if err != nil || strings.Contains(string(data), "mutated") {
		t.Fatalf("definition shares MCP snapshot: %v", err)
	}
	if _, err := snapshot.Definition(); err == nil {
		t.Fatal("mutated MCP digest accepted")
	}
	writeFixture(t, root, "agents/verify/agent.yaml", "mcp: []\n")
	snapshot, err = loaded(t, root).Snapshot("verify")
	if err != nil {
		t.Fatal(err)
	}
	definition, err = snapshot.Definition()
	if err != nil {
		t.Fatal(err)
	}
	data, err = definition.Config(nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.MCPServers) != 0 || snapshot.CompiledPolicy != "" || strings.Contains(string(data), `"mcp"`) || strings.Contains(string(data), `"allow"`) || strings.Contains(string(data), `"title"`) {
		t.Fatal("empty selection exposed MCP tools")
	}
}
