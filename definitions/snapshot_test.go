package definitions

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPlainPromptAgent(t *testing.T) {
	root := fixture(t)
	writeFixture(t, root, "agents/reviewer/agent.yaml", strings.Replace(strings.Replace(agentYAML, "output_schema: output.schema.json\n", "", 1), "[github.diff]", "[]", 1))
	catalog := loaded(t, root)
	snapshot, err := catalog.Snapshot("reviewer")
	if err != nil {
		t.Fatal(err)
	}
	definition, err := snapshot.Definition()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := definition.Config(nil); err != nil {
		t.Fatal(err)
	}
	if err := snapshot.ValidateOutput(json.RawMessage(`"anything"`)); err == nil {
		t.Fatal("schema-less validation accepted")
	}
	if _, err := definition.Secrets(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestDigestIgnoresJSONFormattingAndKeyOrder(t *testing.T) {
	root := fixture(t)
	before := loaded(t, root).Agents["reviewer"].Digest
	var schema map[string]any
	if err := json.Unmarshal([]byte(outputSchema), &schema); err != nil {
		t.Fatal(err)
	}
	pretty, err := json.MarshalIndent(schema, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	writeFixture(t, root, "agents/reviewer/output.schema.json", string(pretty))
	if after := loaded(t, root).Agents["reviewer"].Digest; before != after {
		t.Fatal("JSON formatting changed digest")
	}
}

func TestDigestPreservesLargeNumbers(t *testing.T) {
	root := fixture(t)
	writeFixture(t, root, "agents/reviewer/output.schema.json", `{"const":9007199254740992}`)
	before := loaded(t, root).Agents["reviewer"].Digest
	writeFixture(t, root, "agents/reviewer/output.schema.json", `{"const":9007199254740993}`)
	if after := loaded(t, root).Agents["reviewer"].Digest; before == after {
		t.Fatal("large integer difference lost in digest")
	}
}

func TestSkillOrder(t *testing.T) {
	root := fixture(t)
	writeFixture(t, root, "agents/reviewer/skills/aaa/SKILL.md", "First skill.")
	definition, err := loaded(t, root).Resolve("reviewer")
	if err != nil {
		t.Fatal(err)
	}
	config, err := definition.Config(nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Index(string(config), "First skill.") > strings.Index(string(config), "Check naming conventions.") {
		t.Fatal("skills compiled out of order")
	}
}

func TestSnapshotReadRejectsInvalidStorage(t *testing.T) {
	snapshot, err := loaded(t, fixture(t)).Snapshot("reviewer")
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"malformed":             "{",
		"trailing document":     string(data) + " {}",
		"unknown field":         strings.TrimSuffix(string(data), "}") + `,"unknown":true}`,
		"unknown agent field":   strings.Replace(string(data), `"agent":{`, `"agent":{"config":{},`, 1),
		"unknown profile field": strings.Replace(string(data), `"profile":{`, `"profile":{"tools":{},`, 1),
		"stored digest":         strings.Replace(string(data), snapshot.Agent.Digest, strings.Repeat("0", 64), 1),
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeFixture(t, root, filepath.Join("snapshots", snapshot.Agent.Digest+".json"), content)
			if _, err := ReadSnapshot(root, snapshot.Agent.Digest); err == nil {
				t.Fatal("invalid stored snapshot accepted")
			}
		})
	}
	t.Run("snapshot file symlink", func(t *testing.T) {
		root := t.TempDir()
		if err := SaveSnapshot(root, snapshot); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, "snapshots", snapshot.Agent.Digest+".json")
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("/etc/passwd", path); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadSnapshot(root, snapshot.Agent.Digest); err == nil {
			t.Fatal("snapshot symlink accepted")
		}
		if err := SaveSnapshot(root, snapshot); err == nil {
			t.Fatal("snapshot symlink overwritten")
		}
	})
	t.Run("snapshot directory symlink", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Symlink(t.TempDir(), filepath.Join(root, "snapshots")); err != nil {
			t.Fatal(err)
		}
		if err := SaveSnapshot(root, snapshot); err == nil {
			t.Fatal("snapshot directory symlink accepted")
		}
	})
	t.Run("invalid in memory digest", func(t *testing.T) {
		snapshot.Agent.Digest = strings.Repeat("0", 64)
		if err := SaveSnapshot(t.TempDir(), snapshot); err == nil {
			t.Fatal("invalid snapshot digest accepted")
		}
	})
}

func TestMissingCredentialFilesLoadButFailAtExecution(t *testing.T) {
	root := fixture(t)
	path := filepath.Join(t.TempDir(), "absent")
	writeFixture(t, root, "deployment.yaml", strings.Replace(deploymentYAML, "secret_files: {}", "secret_files: {openai: "+path+"}", 1))
	definition, err := loaded(t, root).Resolve("reviewer")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := definition.Secrets(context.Background()); err == nil {
		t.Fatal("missing credential accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := definition.Secrets(ctx); err != context.Canceled {
		t.Fatalf("cancellation: %v", err)
	}
}
