package definitions

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestNativeToolGrantsAndCompiledPermissions(t *testing.T) {
	root := mcpFixture(t)
	data := strings.Replace(deploymentYAML, "    pool: reviewers", "    tools: [read, glob, grep, edit, shell, webfetch]\n    mcp: [broker, unused]\n    pool: reviewers", 1)
	writeFixture(t, root, "deployment.yaml", data+defaultsYAML+mcpYAML)
	writeFixture(t, root, "agents/verify/agent.yaml", "tools: [webfetch, read]\nmcp: [broker]\n")
	snapshot, err := loaded(t, root).Snapshot("verify")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.CompiledPolicy != compiledNativeMCPSchemaPolicy {
		t.Fatal(snapshot.CompiledPolicy)
	}
	definition, err := snapshot.Definition()
	if err != nil {
		t.Fatal(err)
	}
	config, err := definition.Config(nil)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Permissions []permission `json:"permissions"`
		Agents      map[string]struct {
			System      string       `json:"system"`
			Permissions []permission `json:"permissions"`
			Disabled    bool         `json:"disabled"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(config, &decoded); err != nil {
		t.Fatal(err)
	}
	want := []permission{{"*", "*", "deny"}, {"read", "*", "allow"}, {"webfetch", "*", "allow"}, {"broker_fetch_issue", "*", "allow"}, {"broker_read_diff", "*", "allow"}}
	if !reflect.DeepEqual(decoded.Permissions, want) || !reflect.DeepEqual(decoded.Agents["authored"].Permissions, want) {
		t.Fatalf("permissions: %s", config)
	}
	if !decoded.Agents["title"].Disabled || !strings.Contains(decoded.Agents["authored"].System, "Required output contract") {
		t.Fatal("native policy lost title/output behavior")
	}
	if err := SaveSnapshot(root, snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSnapshot(root, snapshot.Agent.Digest); err != nil {
		t.Fatal(err)
	}
}

func TestNativeToolsRequireBothAgentAndProfile(t *testing.T) {
	for _, bad := range []string{"*", "read, read", "bash", "write", "patch", "external_directory", "websearch", "unknown"} {
		for _, where := range []string{"agent", "profile"} {
			root := defaultsFixture(t)
			if where == "agent" {
				writeFixture(t, root, "agents/verify/agent.yaml", "tools: ["+bad+"]\n")
			} else {
				writeFixture(t, root, "deployment.yaml", strings.Replace(deploymentYAML, "    pool: reviewers", "    tools: ["+bad+"]\n    pool: reviewers", 1)+defaultsYAML)
			}
			if _, err := Load(root); err == nil {
				t.Fatalf("accepted %s %s", where, bad)
			}
		}
	}
	root := defaultsFixture(t)
	writeFixture(t, root, "agents/verify/agent.yaml", "tools: [read]\n")
	if _, err := Load(root); err == nil {
		t.Fatal("ungranted native action accepted")
	}
	writeFixture(t, root, "agents/verify/agent.yaml", "version: 1\n")
	writeFixture(t, root, "deployment.yaml", strings.Replace(deploymentYAML, "    pool: reviewers", "    tools: [shell]\n    pool: reviewers", 1)+defaultsYAML)
	catalog := loaded(t, root)
	for _, name := range BuiltinNames() {
		snapshot, err := catalog.Snapshot(name)
		if err != nil {
			t.Fatal(err)
		}
		if len(snapshot.Agent.Tools) != 0 || strings.Contains(snapshot.CompiledPolicy, "exact-native") {
			t.Fatal("profile grant enabled tools on builtin")
		}
	}
}

func TestNativePoliciesCannotReplayWithLegacyOrUnknownMarkers(t *testing.T) {
	root := defaultsFixture(t)
	catalog := loaded(t, root)
	agent := catalog.Agents["verify"]
	agent.Tools = []string{"read"}
	catalog.Agents[agent.Name] = agent
	profile := catalog.Profiles[agent.Execution.Profile]
	profile.Tools = []string{"read"}
	catalog.Profiles[agent.Execution.Profile] = profile
	for _, schema := range []json.RawMessage{nil, json.RawMessage(`{}`)} {
		agent.Schema = schema
		catalog.Agents[agent.Name] = agent
		snapshot, err := catalog.Snapshot(agent.Name)
		if err != nil {
			t.Fatal(err)
		}
		for _, policy := range []string{"", compiledSchemaPolicy, compiledMCPPolicy, "future-policy"} {
			changed := snapshot
			changed.CompiledPolicy = policy
			changed.Agent.Digest, err = changed.digest()
			if err != nil {
				t.Fatal(err)
			}
			if _, err := changed.Definition(); err == nil {
				t.Fatalf("accepted native grants under %q", policy)
			}
		}
	}
	legacy, err := loaded(t, root).Snapshot("verify")
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"tools"`) {
		t.Fatal("absent native fields changed legacy representation")
	}
	legacy.CompiledPolicy = compiledNativeSchemaPolicy
	legacy.Agent.Digest, err = legacy.digest()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Definition(); err == nil {
		t.Fatal("native marker accepted without native actions")
	}
}
