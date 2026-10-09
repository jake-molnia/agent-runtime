package definitions

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

const agentYAML = `version: 1
description: Review pull requests
model:
  provider: openai
  id: gpt-review
execution:
  profile: review
  timeout_seconds: 120
capabilities: [github.diff]
output_schema: output.schema.json
`

const deploymentYAML = `version: 1
profiles:
  review:
    pool: reviewers
    namespace: agents
    directory: /workspace
    tags: [tag:review]
    capabilities: [github.diff]
    secret_files: {}
    config:
      providers:
        openai:
          request:
            headers:
              X-Review: pinned
`

const automationYAML = `version: 1
agent: reviewer
handler: github.pr-review
trigger:
  adapter: github.pull_request
  actions: [opened, synchronize, ready_for_review]
policy:
  concurrency: pull-request
  limit: 1
  deduplication: reviewed-revision
`

const outputSchema = `{"$schema":"https://json-schema.org/draft/2020-12/schema","type":"object","required":["summary"],"properties":{"summary":{"type":"string"}},"additionalProperties":false}`

func writeFixture(t *testing.T, root, path, contents string) {
	t.Helper()
	filename := filepath.Join(root, path)
	if err := os.MkdirAll(filepath.Dir(filename), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filename, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
}

func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeFixture(t, root, "deployment.yaml", deploymentYAML)
	writeFixture(t, root, "agents/reviewer/agent.yaml", agentYAML)
	writeFixture(t, root, "agents/reviewer/instructions.md", "Review the diff supplied in the task prompt.")
	writeFixture(t, root, "agents/reviewer/skills/style/SKILL.md", "Check naming conventions.")
	writeFixture(t, root, "agents/reviewer/output.schema.json", outputSchema)
	return root
}

func loaded(t *testing.T, root string) *Catalog {
	t.Helper()
	catalog, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func TestLoadAndCompile(t *testing.T) {
	root := fixture(t)
	catalog := loaded(t, root)
	if len(catalog.Agents) != 1 || len(catalog.Profiles) != 1 {
		t.Fatalf("unexpected catalog: %#v", catalog)
	}
	definition, err := catalog.Resolve("reviewer")
	if err != nil {
		t.Fatal(err)
	}
	if definition.AllowProjectConfig || definition.Agent != "authored" || definition.Pool != "reviewers" || definition.Namespace != "agents" || definition.Directory != "/workspace" || definition.Timeout != 120*time.Second {
		t.Fatalf("unexpected definition: %#v", definition)
	}
	if !reflect.DeepEqual(definition.Model, map[string]string{"providerID": "openai", "id": "gpt-review"}) {
		t.Fatalf("model: %#v", definition.Model)
	}
	config, err := definition.Config(nil)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(config, &parsed); err != nil {
		t.Fatal(err)
	}
	deny := []any{map[string]any{"action": "*", "resource": "*", "effect": "deny"}}
	if !reflect.DeepEqual(parsed["permissions"], deny) {
		t.Fatalf("global permissions: %s", config)
	}
	authored := parsed["agents"].(map[string]any)["authored"].(map[string]any)
	if !reflect.DeepEqual(authored["permissions"], deny) || authored["mode"] != "primary" {
		t.Fatalf("agent permissions: %s", config)
	}
	if !strings.Contains(authored["system"].(string), "Review the diff") || !strings.Contains(authored["system"].(string), "Check naming conventions") {
		t.Fatalf("missing behavior: %s", config)
	}
	for _, legacy := range []string{"agent", "permission", "tools", "prompt"} {
		if _, exists := parsed[legacy]; exists {
			t.Fatalf("legacy field %s", legacy)
		}
		if _, exists := authored[legacy]; exists {
			t.Fatalf("legacy agent field %s", legacy)
		}
	}
	if _, err := catalog.Resolve("missing"); err == nil {
		t.Fatal("unknown agent accepted")
	}
}

func TestInvalidPackages(t *testing.T) {
	tests := []struct{ name, path, text string }{
		{"malformed YAML", "agents/reviewer/agent.yaml", "version: ["},
		{"unknown agent field", "agents/reviewer/agent.yaml", agentYAML + "config: {}\n"},
		{"unknown nested model field", "agents/reviewer/agent.yaml", strings.Replace(agentYAML, "  id: gpt-review", "  id: gpt-review\n  endpoint: evil", 1)},
		{"unknown execution field", "agents/reviewer/agent.yaml", strings.Replace(agentYAML, "  timeout_seconds: 120", "  timeout_seconds: 120\n  tools: true", 1)},
		{"multiple documents", "agents/reviewer/agent.yaml", agentYAML + "---\nversion: 1\n"},
		{"duplicate field", "agents/reviewer/agent.yaml", agentYAML + "version: 1\n"},
		{"agent version", "agents/reviewer/agent.yaml", strings.Replace(agentYAML, "version: 1", "version: 2", 1)},
		{"missing profile", "agents/reviewer/agent.yaml", strings.Replace(agentYAML, "profile: review", "profile: absent", 1)},
		{"capability", "agents/reviewer/agent.yaml", strings.Replace(agentYAML, "github.diff", "shell", 1)},
		{"duplicate capability", "agents/reviewer/agent.yaml", strings.Replace(agentYAML, "[github.diff]", "[github.diff, github.diff]", 1)},
		{"ungranted capability", "deployment.yaml", strings.Replace(deploymentYAML, "[github.diff]", "[]", 1)},
		{"profile capability", "deployment.yaml", strings.Replace(deploymentYAML, "github.diff", "shell", 1)},
		{"timeout", "agents/reviewer/agent.yaml", strings.Replace(agentYAML, "timeout_seconds: 120", "timeout_seconds: 0", 1)},
		{"empty instructions", "agents/reviewer/instructions.md", " "},
		{"schema traversal", "agents/reviewer/agent.yaml", strings.Replace(agentYAML, "output.schema.json", "../outside.json", 1)},
		{"schema absolute", "agents/reviewer/agent.yaml", strings.Replace(agentYAML, "output.schema.json", "/outside.json", 1)},
		{"schema windows traversal", "agents/reviewer/agent.yaml", strings.Replace(agentYAML, "output.schema.json", `..\outside.json`, 1)},
		{"malformed JSON schema", "agents/reviewer/output.schema.json", "{"},
		{"invalid JSON schema", "agents/reviewer/output.schema.json", `{"type":"not-a-type"}`},
		{"external JSON schema", "agents/reviewer/output.schema.json", `{"$ref":"https://invalid.example/schema"}`},
		{"file JSON schema", "agents/reviewer/output.schema.json", `{"$ref":"file:///etc/passwd"}`},
		{"automation migration", "automations/review.yaml", automationYAML},
		{"unknown deployment field", "deployment.yaml", deploymentYAML + "unknown: true\n"},
		{"deployment version", "deployment.yaml", strings.Replace(deploymentYAML, "version: 1", "version: 2", 1)},
		{"unknown profile field", "deployment.yaml", strings.Replace(deploymentYAML, "    pool: reviewers", "    pool: reviewers\n    tools: true", 1)},
		{"profile directory traversal", "deployment.yaml", strings.Replace(deploymentYAML, "/workspace", "/workspace/../other", 1)},
		{"relative secret path", "deployment.yaml", strings.Replace(deploymentYAML, "secret_files: {}", "secret_files: {openai: ../credential}", 1)},
		{"unbound config secret", "deployment.yaml", strings.Replace(deploymentYAML, "X-Review: pinned", "X-Review: {$secret: missing}", 1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := fixture(t)
			writeFixture(t, root, test.path, test.text)
			if _, err := Load(root); err == nil {
				t.Fatal("invalid package accepted")
			}
		})
	}
}

func TestProfileCannotOverridePolicy(t *testing.T) {
	for _, key := range []string{"agents", "permissions", "agent", "permission", "tools", "plugins", "mcp", "instructions", "commands", "skills", "auth", "experimental"} {
		t.Run(key, func(t *testing.T) {
			root := fixture(t)
			writeFixture(t, root, "deployment.yaml", strings.Replace(deploymentYAML, "    config:\n", "    config:\n      "+key+": {}\n", 1))
			if _, err := Load(root); err == nil {
				t.Fatal("policy override accepted")
			}
		})
	}
}

func TestSymlinks(t *testing.T) {
	for _, path := range []string{"deployment.yaml", "agents", "agents/reviewer", "agents/reviewer/agent.yaml", "agents/reviewer/instructions.md", "agents/reviewer/skills", "agents/reviewer/skills/style/SKILL.md", "agents/reviewer/output.schema.json", "automations", "agents/reviewer/unused"} {
		t.Run(path, func(t *testing.T) {
			root := fixture(t)
			full := filepath.Join(root, path)
			if err := os.RemoveAll(full); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(t.TempDir(), full); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(root); err == nil {
				t.Fatal("symlink accepted")
			}
		})
	}
	t.Run("root", func(t *testing.T) {
		root := fixture(t)
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(root, link); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(link); err == nil {
			t.Fatal("root symlink accepted")
		}
	})
}

func TestDigestPinsBehaviorAndProfile(t *testing.T) {
	for _, test := range []struct{ name, path, text string }{
		{"instructions", "agents/reviewer/instructions.md", "New instructions"},
		{"skill", "agents/reviewer/skills/style/SKILL.md", "New skill"},
		{"schema", "agents/reviewer/output.schema.json", `{"type":"string"}`},
		{"model", "agents/reviewer/agent.yaml", strings.Replace(agentYAML, "gpt-review", "other", 1)},
		{"timeout", "agents/reviewer/agent.yaml", strings.Replace(agentYAML, "120", "121", 1)},
		{"profile config", "deployment.yaml", strings.Replace(deploymentYAML, "pinned", "changed", 1)},
		{"profile pool", "deployment.yaml", strings.Replace(deploymentYAML, "reviewers", "other", 1)},
		{"profile namespace", "deployment.yaml", strings.Replace(deploymentYAML, "namespace: agents", "namespace: other", 1)},
		{"profile directory", "deployment.yaml", strings.Replace(deploymentYAML, "/workspace", "/other", 1)},
		{"profile tags", "deployment.yaml", strings.Replace(deploymentYAML, "tag:review", "tag:other", 1)},
		{"secret binding", "deployment.yaml", strings.Replace(deploymentYAML, "secret_files: {}", "secret_files: {openai: /run/secrets/openai}", 1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := fixture(t)
			before := loaded(t, root).Agents["reviewer"].Digest
			writeFixture(t, root, test.path, test.text)
			after := loaded(t, root).Agents["reviewer"].Digest
			if before == after {
				t.Fatal("digest did not change")
			}
		})
	}
}

func TestSnapshotRecoveryAndImmutability(t *testing.T) {
	root := fixture(t)
	catalog := loaded(t, root)
	snapshot, err := catalog.Snapshot("reviewer")
	if err != nil {
		t.Fatal(err)
	}
	store := t.TempDir()
	if err := catalog.Save(store); err != nil {
		t.Fatal(err)
	}
	if err := SaveSnapshot(store, snapshot); err != nil {
		t.Fatal(err)
	}
	writeFixture(t, root, "agents/reviewer/instructions.md", "Changed after save")
	writeFixture(t, root, "deployment.yaml", strings.Replace(deploymentYAML, "pinned", "changed", 1))
	updated := loaded(t, root)
	if err := updated.Save(store); err != nil {
		t.Fatal(err)
	}
	if updated.Agents["reviewer"].Digest == snapshot.Agent.Digest {
		t.Fatal("changes not pinned")
	}
	if err := os.RemoveAll(root); err != nil {
		t.Fatal(err)
	}
	recovered, err := ReadSnapshot(store, snapshot.Agent.Digest)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(recovered, snapshot) {
		t.Fatalf("snapshot changed: %#v", recovered)
	}
	definition, err := recovered.Definition()
	if err != nil {
		t.Fatal(err)
	}
	config, err := definition.Config(nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(config), "Changed after save") || !strings.Contains(string(config), "pinned") {
		t.Fatal("recovery used current files")
	}
	path := filepath.Join(store, "snapshots", snapshot.Agent.Digest+".json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, store, filepath.Join("snapshots", snapshot.Agent.Digest+".json"), strings.Replace(string(data), "Review pull requests", "Tampered", 1))
	if _, err := ReadSnapshot(store, snapshot.Agent.Digest); err == nil {
		t.Fatal("tampered snapshot accepted")
	}
	if err := SaveSnapshot(store, snapshot); err == nil {
		t.Fatal("corrupt snapshot overwritten")
	}
	for _, digest := range []string{"../escape", strings.Repeat("x", 64), strings.ToUpper(snapshot.Agent.Digest)} {
		if _, err := ReadSnapshot(store, digest); err == nil {
			t.Fatalf("invalid digest accepted: %s", digest)
		}
	}
}

func TestOutputValidation(t *testing.T) {
	snapshot, err := loaded(t, fixture(t)).Snapshot("reviewer")
	if err != nil {
		t.Fatal(err)
	}
	if err := snapshot.ValidateOutput(json.RawMessage(`{"summary":"Looks good"}`)); err != nil {
		t.Fatal(err)
	}
	for _, output := range []string{`{}`, `{"summary":1}`, `{"summary":"ok","extra":true}`, `null`, `{`, `{"summary":"ok"} {}`} {
		if err := snapshot.ValidateOutput(json.RawMessage(output)); err == nil {
			t.Fatalf("invalid output accepted: %s", output)
		}
	}
	root := fixture(t)
	writeFixture(t, root, "agents/reviewer/output.schema.json", `{"$defs":{"text":{"type":"string"}},"$ref":"#/$defs/text"}`)
	snapshot, err = loaded(t, root).Snapshot("reviewer")
	if err != nil {
		t.Fatal(err)
	}
	if err := snapshot.ValidateOutput(json.RawMessage(`"ok"`)); err != nil {
		t.Fatal(err)
	}
}

func TestSecretsAreLateBoundAndNeverSnapshotted(t *testing.T) {
	root := fixture(t)
	secret := filepath.Join(t.TempDir(), "provider-key")
	writeFixture(t, filepath.Dir(secret), filepath.Base(secret), "sensitive-key\n")
	deployment := strings.Replace(deploymentYAML, "secret_files: {}", "secret_files: {openai: "+secret+"}", 1)
	deployment = strings.Replace(deployment, "X-Review: pinned", "X-Review: {$secret: openai}", 1)
	writeFixture(t, root, "deployment.yaml", deployment)
	catalog := loaded(t, root)
	snapshot, err := catalog.Snapshot("reviewer")
	if err != nil {
		t.Fatal(err)
	}
	definition, err := snapshot.Definition()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := definition.Config(nil); err == nil {
		t.Fatal("missing credential accepted")
	}
	secrets, err := definition.Secrets(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	config, err := definition.Config(secrets)
	if err != nil {
		t.Fatal(err)
	}
	var parsed map[string]any
	if err := json.Unmarshal(config, &parsed); err != nil {
		t.Fatal(err)
	}
	auth := parsed["auth"].(map[string]any)["openai"].(map[string]any)
	if auth["key"] != "sensitive-key" || auth["type"] != "api" {
		t.Fatalf("auth: %#v", auth)
	}
	if err := catalog.Save(root); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "snapshots", snapshot.Agent.Digest+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "sensitive-key") || !strings.Contains(string(data), secret) {
		t.Fatal("snapshot must store only secret binding")
	}
	writeFixture(t, filepath.Dir(secret), filepath.Base(secret), "rotated-key")
	if loaded(t, root).Agents["reviewer"].Digest != snapshot.Agent.Digest {
		t.Fatal("secret value changed digest")
	}
	secrets, err = definition.Secrets(context.Background())
	if err != nil || secrets["openai"] != "rotated-key" {
		t.Fatalf("late secrets: %#v %v", secrets, err)
	}
	if err := os.Remove(secret); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/etc/passwd", secret); err != nil {
		t.Fatal(err)
	}
	if _, err := definition.Secrets(context.Background()); err == nil {
		t.Fatal("secret symlink accepted")
	}
}

func TestSnapshotAndDefinitionDoNotShareMutableMaps(t *testing.T) {
	catalog := loaded(t, fixture(t))
	snapshot, err := catalog.Snapshot("reviewer")
	if err != nil {
		t.Fatal(err)
	}
	definition, err := snapshot.Definition()
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Agent.Skills["style"] = "mutated"
	catalog.Agents["reviewer"].Skills["style"] = "also mutated"
	config, err := definition.Config(nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(config), "mutated") {
		t.Fatal("definition shares package maps")
	}
	if _, err := snapshot.Definition(); err == nil {
		t.Fatal("mutated digest accepted")
	}
}

func TestConcurrentSnapshotSave(t *testing.T) {
	snapshot, err := loaded(t, fixture(t)).Snapshot("reviewer")
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "new-store")
	var workers sync.WaitGroup
	for index := 0; index < 8; index++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if err := SaveSnapshot(root, snapshot); err != nil {
				t.Error(err)
			}
		}()
	}
	workers.Wait()
	if _, err := ReadSnapshot(root, snapshot.Agent.Digest); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(root, "snapshots"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("snapshot files: %v %v", entries, err)
	}
}
