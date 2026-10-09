package definitions

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBrokerCatalogPolicy(t *testing.T) {
	root := mcpFixture(t)
	catalog := loaded(t, root)
	before, err := catalog.Snapshot("verify")
	if err != nil {
		t.Fatal(err)
	}
	broker := catalog.MCPServers["broker"]
	broker.Tools, broker.ToolPolicy = nil, "broker_catalog"
	catalog.MCPServers["broker"] = broker
	snapshot, err := catalog.Snapshot("verify")
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.CompiledPolicy != compiledBrokerPolicy || snapshot.Agent.Digest == before.Agent.Digest {
		t.Fatal("broker authority not revision-pinned")
	}
	definition, err := snapshot.Definition()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := definition.Config(nil)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Permissions []permission `json:"permissions"`
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	if len(config.Permissions) != 2 || config.Permissions[0].Effect != "deny" || config.Permissions[1].Action != "broker_*" {
		t.Fatalf("unexpected grants: %s", raw)
	}
	snapshot.CompiledPolicy = compiledMCPPolicy
	if err := snapshot.validate(); err == nil {
		t.Fatal("broker allowed under legacy policy")
	}
	if _, err := before.Definition(); err != nil {
		t.Fatalf("legacy exact snapshot changed: %v", err)
	}
}

func TestBrokerNamespaceBoundary(t *testing.T) {
	cases := []struct {
		name    string
		servers map[string]MCPServer
	}{
		{"mixed", map[string]MCPServer{"broker": {URL: "https://example.com", Tools: []string{"read"}, ToolPolicy: "broker_catalog"}}},
		{"unknown", map[string]MCPServer{"broker": {URL: "https://example.com", Tools: []string{"read"}, ToolPolicy: "all"}}},
		{"prefix", map[string]MCPServer{"broker": {URL: "https://example.com", ToolPolicy: "broker_catalog"}, "broker_extra": {URL: "https://example.com", Tools: []string{"read"}}}},
		{"native", map[string]MCPServer{"read": {URL: "https://example.com", ToolPolicy: "broker_catalog"}}},
		{"native_exact", map[string]MCPServer{"read": {URL: "https://example.com", Tools: []string{"mcp_resource"}}}},
		{"native_permission", map[string]MCPServer{"external": {URL: "https://example.com", ToolPolicy: "broker_catalog"}}},
		{"native_namespace", map[string]MCPServer{"opencode": {URL: "https://example.com", ToolPolicy: "broker_catalog"}}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := validateMCPServers(tc.servers); err == nil {
				t.Fatal("unsafe broker policy accepted")
			}
		})
	}
	if err := validateMCPServers(map[string]MCPServer{"broker": {URL: "https://example.com", ToolPolicy: "broker_catalog"}, "broker-extra": {URL: "https://example.com", Tools: []string{"read"}}}); err != nil {
		t.Fatal(err)
	}
}

func TestProfilePolicyTags(t *testing.T) {
	for _, tags := range [][]string{{"tag:bad_tag"}, {"tag:Good"}, {"tag:a", "tag:a"}, {"a"}} {
		profile := Profile{Pool: "p", Namespace: "ns", Directory: "/workspace", Tags: tags}
		if err := validateProfile(profile); err == nil || !strings.Contains(err.Error(), "policy tag") {
			t.Fatalf("tags %v: %v", tags, err)
		}
	}
}

func TestLegacySnapshotTagsRemainReadable(t *testing.T) {
	catalog := loaded(t, defaultsFixture(t))
	snapshot, err := catalog.Snapshot("verify")
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Profile.Tags = []string{"tag:legacy"}
	snapshot.Agent.Digest, err = snapshot.digest()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := snapshot.Definition(); err != nil {
		t.Fatalf("legacy tag policy changed: %v", err)
	}
}

func TestBuiltinSkillsArePinnedAndScoped(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AGENT_RUNTIME_SKILL_BUNDLE_ROOT", root)
	skillCatalog := t.TempDir()
	t.Setenv("AGENT_RUNTIME_SKILL_CATALOG_ROOT", skillCatalog)
	if err := os.Symlink(root, filepath.Join(skillCatalog, "review")); err != nil {
		t.Fatal(err)
	}
	content := []byte("trusted skill")
	sum := sha256.Sum256(content)
	inventory, _ := json.Marshal([]map[string]string{{"name": "review", "path": "SKILL.md", "sha256": hex.EncodeToString(sum[:])}})
	manifest, _ := json.Marshal([]map[string]string{{"path": "SKILL.md", "sha256": hex.EncodeToString(sum[:])}})
	for name, data := range map[string][]byte{"SKILL.md": content, "inventory.json": inventory, "sources.json": []byte(`{"revision":"first"}`), "files.json": manifest} {
		if err := os.WriteFile(filepath.Join(root, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	catalog := loaded(t, defaultsFixture(t))
	agent := catalog.Agents["verify"]
	agent.BuiltinSkills = []string{"review"}
	catalog.Agents["verify"] = agent
	snapshot, err := catalog.Snapshot("verify")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.SkillBundleDigest) != 64 || snapshot.CompiledPolicy != compiledSkillPolicy {
		t.Fatal("skills were not pinned")
	}
	definition, err := snapshot.Definition()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := definition.Config(nil)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Skills      []string     `json:"skills"`
		Permissions []permission `json:"permissions"`
	}
	if err := json.Unmarshal(raw, &config); err != nil {
		t.Fatal(err)
	}
	if len(config.Skills) != 1 || config.Skills[0] != "/opt/agent-skills" || definition.SkillBundleDigest != snapshot.SkillBundleDigest {
		t.Fatalf("wrong skill installation: %s", raw)
	}
	for _, grant := range config.Permissions {
		if grant.Effect != "allow" {
			continue
		}
		switch grant.Action {
		case "skill":
			if grant.Resource != "review" {
				t.Fatal("unselected skill allowed")
			}
		case "read", "external_directory":
			if grant.Resource != "/opt/agent-skills/**" && grant.Resource != "/opt/agent-skill-bundles/**" {
				t.Fatal("non-bundle filesystem grant")
			}
		default:
			t.Fatalf("unexpected grant %+v", grant)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "sources.json"), []byte(`{"revision":"next"}`), 0600); err != nil {
		t.Fatal(err)
	}
	after, err := catalog.Snapshot("verify")
	if err != nil {
		t.Fatal(err)
	}
	if after.Agent.Digest == snapshot.Agent.Digest {
		t.Fatal("changed skill sources did not change revision")
	}
	if _, err := snapshot.Definition(); err != nil {
		t.Fatalf("old pinned snapshot not readable: %v", err)
	}
	agent.BuiltinSkills = []string{"missing"}
	catalog.Agents["verify"] = agent
	if _, err := catalog.Snapshot("verify"); err == nil {
		t.Fatal("missing installed skill accepted")
	}
}

func TestDeploymentRequiresSandboxTag(t *testing.T) {
	root := defaultsFixture(t)
	writeFixture(t, root, "deployment.yaml", strings.Replace(deploymentYAML, "tag:agent-sandbox, ", "", 1)+defaultsYAML)
	if _, err := Load(root); err == nil || !strings.Contains(err.Error(), "tag:agent-sandbox required") {
		t.Fatalf("missing base tag accepted: %v", err)
	}
}
