package workflows

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/jake-molnia/agent-runtime/definitions"
	"gopkg.in/yaml.v3"
)

const userYAML = `steps:
  review: {agent: code-review, input: input}
  adversarial: {agent: adversarial-review, input: input}
  verify: {agent: verify, input: [review, adversarial]}
output: verify
`

func fixtureCatalog() *definitions.Catalog {
	catalog := &definitions.Catalog{Agents: map[string]definitions.Agent{}, Profiles: map[string]definitions.Profile{}}
	for _, name := range []string{"code-review", "adversarial-review", "verify"} {
		catalog.Profiles[name] = definitions.Profile{Pool: "pool-" + name, Namespace: "tests", Directory: "/workspace", SecretFiles: map[string]string{"openai": "/missing/credential"}}
		catalog.Agents[name] = definitions.Agent{Name: name, Version: 1, Description: "Generic agent " + name, Instructions: "Original instructions " + name, Model: definitions.Model{Provider: "openai", ID: "model-" + name}, Execution: definitions.Execution{Profile: name, TimeoutSeconds: 30}, OutputSchema: "output.schema.json", Schema: json.RawMessage(`{"type":["object","array","string","number","boolean","null"]}`)}
	}
	return catalog
}

func writeFile(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, "workflows", name)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
}

func captured(t *testing.T) Snapshot {
	t.Helper()
	root := t.TempDir()
	writeFile(t, root, "review.yaml", userYAML)
	loaded, err := Load(root, fixtureCatalog())
	if err != nil {
		t.Fatal(err)
	}
	return loaded["review"]
}

func cloneSnapshot(t *testing.T, original Snapshot) Snapshot {
	t.Helper()
	data, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	var snapshot Snapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestUserManifest(t *testing.T) {
	snapshot := captured(t)
	if snapshot.Workflow.Version != 1 || snapshot.Workflow.Name != "review" {
		t.Fatal("filename/default version not preserved")
	}
	order, err := snapshot.Order()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(order, []string{"adversarial", "review", "verify"}) {
		t.Fatalf("order: %v", order)
	}
	if snapshot.Workflow.Steps["review"].Input.Multiple || !snapshot.Workflow.Steps["verify"].Input.Multiple {
		t.Fatal("scalar/list shape lost")
	}
	if err := snapshot.Verify(); err != nil {
		t.Fatal(err)
	}
	for name, agent := range snapshot.Agents {
		if agent.Agent.Name != snapshot.Workflow.Steps[name].Agent {
			t.Fatal("agent not keyed by step")
		}
		if !bytes.Equal(agent.Agent.Schema, fixtureCatalog().Agents[agent.Agent.Name].Schema) {
			t.Fatal("schema changed")
		}
		for _, value := range []string{`"text"`, `true`, `null`, `[1,2]`, `{"key":3}`} {
			if err := agent.ValidateOutput(json.RawMessage(value)); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestResolveInput(t *testing.T) {
	for _, value := range []string{`"plain text"`, `true`, `false`, `null`, `12`, `[1,"two"]`, `{"nested":{"value":true}}`} {
		for _, multiple := range []bool{false, true} {
			step := Step{Input: InputRefs{Sources: []string{"input"}, Multiple: multiple}}
			initial := json.RawMessage(value)
			result, err := ResolveInput(step, initial, nil)
			if err != nil {
				t.Fatal(err)
			}
			expected := value
			if multiple {
				expected = "[" + value + "]"
			}
			if string(result) != expected {
				t.Fatalf("got %s want %s", result, expected)
			}
			initial[0] = 'x'
			if string(result) != expected {
				t.Fatal("returned input aliases initial")
			}
		}
	}
	parents := map[string]json.RawMessage{"first": json.RawMessage(`{"a":1}`), "second": json.RawMessage(`"two"`)}
	step := Step{Input: InputRefs{Sources: []string{"second", "first", "input", "second"}, Multiple: true}}
	result, err := ResolveInput(step, json.RawMessage(`false`), parents)
	if err != nil || string(result) != `["two",{"a":1},false,"two"]` {
		t.Fatalf("ordered input: %s %v", result, err)
	}
	for _, refs := range []InputRefs{{}, {Sources: []string{"missing"}}, {Sources: []string{"first", "second"}}, {Sources: []string{"first.key"}}, {Sources: []string{"input"}}} {
		if _, err := ResolveInput(Step{Input: refs}, json.RawMessage(`not JSON`), parents); err == nil {
			t.Fatalf("accepted invalid input %+v", refs)
		}
	}
	parents["first"] = json.RawMessage(`{`)
	if _, err := ResolveInput(Step{Input: InputRefs{Sources: []string{"first"}}}, nil, parents); err == nil {
		t.Fatal("malformed parent accepted")
	}
	oversized := json.RawMessage(`"` + strings.Repeat("x", MaxInputBytes) + `"`)
	if _, err := ResolveInput(Step{Input: InputRefs{Sources: []string{"input"}}}, oversized, nil); err == nil {
		t.Fatal("oversized scalar accepted")
	}
	value := json.RawMessage(`"` + strings.Repeat("x", MaxInputBytes/2) + `"`)
	if _, err := ResolveInput(Step{Input: InputRefs{Sources: []string{"input", "input"}, Multiple: true}}, value, nil); err == nil {
		t.Fatal("oversized array accepted")
	}
}

func TestInvalidYAML(t *testing.T) {
	for name, manifest := range map[string]string{
		"map":            strings.Replace(userYAML, "input: input", "input: {source: input}", 1),
		"null":           strings.Replace(userYAML, "input: input", "input: null", 1),
		"empty":          strings.Replace(userYAML, "input: input", "input: []", 1),
		"empty string":   strings.Replace(userYAML, "input: input", "input: ''", 1),
		"number":         strings.Replace(userYAML, "input: input", "input: 42", 1),
		"list null":      strings.Replace(userYAML, "[review, adversarial]", "[review, null]", 1),
		"expression":     strings.Replace(userYAML, "input: input", "input: '${input}'", 1),
		"path":           strings.Replace(userYAML, "input: input", "input: input.field", 1),
		"unknown top":    userYAML + "unknown: true\n",
		"unknown step":   strings.Replace(userYAML, "agent: code-review", "unknown: true, agent: code-review", 1),
		"duplicate top":  userYAML + "output: review\n",
		"duplicate step": strings.Replace(userYAML, "agent: code-review", "agent: verify, agent: code-review", 1),
		"duplicate id":   strings.Replace(userYAML, "  adversarial:", "  review:", 1),
		"unknown ref":    strings.Replace(userYAML, "[review, adversarial]", "[missing, adversarial]", 1),
		"cycle":          strings.Replace(userYAML, "input: input", "input: verify", 1),
		"stray":          strings.Replace(userYAML, "[review, adversarial]", "[review]", 1),
		"reserved":       strings.ReplaceAll(userYAML, "adversarial:", "resolve:"),
		"unknown output": strings.Replace(userYAML, "output: verify", "output: missing", 1),
		"unknown agent":  strings.Replace(userYAML, "agent: code-review", "agent: absent", 1),
		"version":        "version: 2\n" + userYAML,
		"extra document": userYAML + "---\n" + userYAML,
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, root, "review.yaml", manifest)
			if _, err := Load(root, fixtureCatalog()); err == nil {
				t.Fatal("invalid manifest accepted")
			}
		})
	}
}

func TestNamesAndLimits(t *testing.T) {
	base := captured(t).Workflow
	for _, name := range []string{"", strings.Repeat("a", 64), "../bad", "bad.name", "bad name"} {
		workflow := base
		workflow.Name = name
		if _, err := Capture(workflow, fixtureCatalog()); err == nil {
			t.Fatalf("invalid name accepted: %q", name)
		}
	}
	for _, reserved := range []string{"input", "resolve", "result"} {
		workflow := Workflow{Name: "test", Steps: map[string]Step{reserved: {Agent: "verify", Input: InputRefs{Sources: []string{"input"}}}}, Output: reserved}
		if _, err := Capture(workflow, fixtureCatalog()); err == nil {
			t.Fatal("reserved ID accepted")
		}
	}
	workflow := Workflow{Name: "chain", Steps: map[string]Step{}, Output: "step32"}
	source := "input"
	for number := 0; number <= 32; number++ {
		name := "step" + strings.Repeat("a", number)
		workflow.Steps[name] = Step{Agent: "verify", Input: InputRefs{Sources: []string{source}}}
		source = name
	}
	workflow.Output = source
	if _, err := Capture(workflow, fixtureCatalog()); err == nil {
		t.Fatal("33 steps accepted")
	}
	delete(workflow.Steps, source)
	workflow.Output = "step" + strings.Repeat("a", 31)
	if _, err := Capture(workflow, fixtureCatalog()); err != nil {
		t.Fatalf("32 steps rejected: %v", err)
	}
	catalog := fixtureCatalog()
	agent := catalog.Agents["verify"]
	agent.Schema = nil
	agent.OutputSchema = ""
	catalog.Agents["verify"] = agent
	if _, err := Capture(base, catalog); err == nil {
		t.Fatal("agent without output schema accepted")
	}
}

func TestSnapshotReplayAndIdentity(t *testing.T) {
	original := captured(t)
	root := t.TempDir()
	if err := Save(root, original); err != nil {
		t.Fatal(err)
	}
	catalog := fixtureCatalog()
	agent := catalog.Agents["code-review"]
	agent.Instructions = "New deployed instructions"
	agent.Model.ID = "new-model"
	catalog.Agents["code-review"] = agent
	changed, err := Capture(original.Workflow, catalog)
	if err != nil || changed.Digest == original.Digest {
		t.Fatalf("deployment identity: %v", err)
	}
	old, err := Read(root, original.Digest)
	if err != nil || !reflect.DeepEqual(old, original) {
		t.Fatalf("replay changed: %v", err)
	}
	if _, err := old.Agents["review"].Definition(); err != nil {
		t.Fatal(err)
	}
	catalog.Agents = nil
	if err := old.Verify(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Snapshot){
		"shape": func(snapshot *Snapshot) {
			step := snapshot.Workflow.Steps["review"]
			step.Input.Multiple = true
			snapshot.Workflow.Steps["review"] = step
		},
		"reference order": func(snapshot *Snapshot) {
			step := snapshot.Workflow.Steps["verify"]
			step.Input.Sources = []string{"adversarial", "review"}
			snapshot.Workflow.Steps["verify"] = step
		},
		"output": func(snapshot *Snapshot) { snapshot.Workflow.Output = "review" },
		"model": func(snapshot *Snapshot) {
			agent := snapshot.Agents["review"]
			agent.Agent.Model.ID = "different"
			snapshot.Agents["review"] = agent
		},
		"schema": func(snapshot *Snapshot) {
			agent := snapshot.Agents["review"]
			agent.Agent.Schema = json.RawMessage(`true`)
			snapshot.Agents["review"] = agent
		},
		"profile": func(snapshot *Snapshot) {
			agent := snapshot.Agents["review"]
			agent.Profile.Pool = "different"
			snapshot.Agents["review"] = agent
		},
		"missing agent": func(snapshot *Snapshot) { delete(snapshot.Agents, "review") },
		"extra agent":   func(snapshot *Snapshot) { snapshot.Agents["extra"] = snapshot.Agents["review"] },
	} {
		t.Run(name, func(t *testing.T) {
			altered := cloneSnapshot(t, original)
			mutate(&altered)
			if err := altered.Verify(); err == nil {
				t.Fatal("mutation accepted")
			}
		})
	}
	workflow := cloneSnapshot(t, original).Workflow
	step := workflow.Steps["review"]
	step.Input.Multiple = true
	workflow.Steps["review"] = step
	changed, err = Capture(workflow, fixtureCatalog())
	if err != nil || changed.Digest == original.Digest {
		t.Fatalf("shape omitted from digest: %v", err)
	}
	workflow = cloneSnapshot(t, original).Workflow
	step = workflow.Steps["verify"]
	step.Input.Sources = []string{"adversarial", "review"}
	workflow.Steps["verify"] = step
	changed, err = Capture(workflow, fixtureCatalog())
	if err != nil || changed.Digest == original.Digest {
		t.Fatalf("reference order omitted from digest: %v", err)
	}
}

func TestStorageConcurrentAndCorrupt(t *testing.T) {
	snapshot := captured(t)
	root := filepath.Join(t.TempDir(), "new", "root")
	var workers sync.WaitGroup
	failures := make(chan error, 20)
	for worker := 0; worker < 20; worker++ {
		workers.Add(1)
		go func() { defer workers.Done(); failures <- Save(root, snapshot) }()
	}
	workers.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(root, "workflows"))
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary files left: %v %v", entries, err)
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	for name, content := range map[string]string{
		"malformed":      "{",
		"trailing":       string(data) + " {}",
		"unknown":        strings.TrimSuffix(string(data), "}") + `,"unknown":true}`,
		"nested unknown": strings.Replace(string(data), `"workflow":{`, `"workflow":{"unknown":true,`, 1),
		"input unknown":  strings.Replace(string(data), `"sources":`, `"unknown":true,"sources":`, 1),
		"digest":         strings.Replace(string(data), snapshot.Digest, strings.Repeat("0", 64), 1),
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, root, snapshot.Digest+".json", content)
			if _, err := Read(root, snapshot.Digest); err == nil {
				t.Fatal("corrupt snapshot accepted")
			}
			if err := Save(root, snapshot); err == nil {
				t.Fatal("corrupt snapshot overwritten")
			}
		})
	}
	for _, digest := range []string{"", "../escape", strings.ToUpper(snapshot.Digest), strings.Repeat("g", 64)} {
		if _, err := Read(root, digest); err == nil {
			t.Fatal("unsafe digest accepted")
		}
	}
	loaded, err := Load(root, nil)
	if err != nil || len(loaded) != 0 {
		t.Fatalf("snapshot files loaded as manifests: %v", err)
	}
}

func TestLoadWithAbandonedSnapshotTemporaryFile(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "review.yaml", userYAML)
	writeFile(t, root, ".snapshot-interrupted", "partial snapshot")
	if err := Save(root, captured(t)); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(root, fixtureCatalog())
	if err != nil || len(loaded) != 1 {
		t.Fatalf("abandoned snapshot blocked manifest loading: %v", err)
	}
	if err := os.Symlink("review.yaml", filepath.Join(root, "workflows", ".snapshot-link")); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root, fixtureCatalog()); err == nil {
		t.Fatal("snapshot temporary symlink accepted")
	}
}

func TestUnsafePathsAndOptionalDirectory(t *testing.T) {
	empty, err := Load(t.TempDir(), nil)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Fatalf("optional directory: %v", err)
	}
	snapshot := captured(t)
	for _, scope := range []string{"ancestor", "directory", "file"} {
		t.Run(scope, func(t *testing.T) {
			root := t.TempDir()
			switch scope {
			case "ancestor":
				link := filepath.Join(root, "linked")
				if err := os.Symlink(t.TempDir(), link); err != nil {
					t.Fatal(err)
				}
				root = filepath.Join(link, "missing")
			case "directory":
				if err := os.Symlink(t.TempDir(), filepath.Join(root, "workflows")); err != nil {
					t.Fatal(err)
				}
			case "file":
				writeFile(t, root, "review.yaml", userYAML)
				if err := os.Symlink("review.yaml", filepath.Join(root, "workflows", snapshot.Digest+".json")); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := Load(root, fixtureCatalog()); err == nil {
				t.Fatal("unsafe load accepted")
			}
			if err := Save(root, snapshot); err == nil {
				t.Fatal("unsafe save accepted")
			}
			if _, err := Read(root, snapshot.Digest); err == nil {
				t.Fatal("unsafe read accepted")
			}
		})
	}
	for _, name := range []string{"stray.txt", "bad.name.yaml", strings.Repeat("a", 64) + ".yaml"} {
		root := t.TempDir()
		writeFile(t, root, name, userYAML)
		if _, err := Load(root, fixtureCatalog()); err == nil {
			t.Fatal("unsafe filename accepted")
		}
	}
	root := t.TempDir() + "/../escape"
	if _, err := Load(root, nil); err == nil {
		t.Fatal("traversal accepted")
	}
	if err := Save(root, snapshot); err == nil {
		t.Fatal("traversal save accepted")
	}
}

func TestCaptureOwnsManifestAndYAMLShape(t *testing.T) {
	original := captured(t)
	workflow := cloneSnapshot(t, original).Workflow
	snapshot, err := Capture(workflow, fixtureCatalog())
	if err != nil {
		t.Fatal(err)
	}
	workflow.Steps["verify"].Input.Sources[0] = "changed"
	delete(workflow.Steps, "review")
	if err := snapshot.Verify(); err != nil {
		t.Fatal("capture shares manifest memory", err)
	}
	for _, multiple := range []bool{false, true} {
		refs := InputRefs{Sources: []string{"input"}, Multiple: multiple}
		data, err := yaml.Marshal(refs)
		if err != nil {
			t.Fatal(err)
		}
		var decoded InputRefs
		if err := yaml.Unmarshal(data, &decoded); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(refs, decoded) {
			t.Fatal("YAML shape lost")
		}
	}
}

func TestResolveInputCombinesNearLimitReviewReports(t *testing.T) {
	for _, character := range []string{"x", "<"} {
		t.Run(character, func(t *testing.T) {
			report := json.RawMessage(`{"summary":"` + strings.Repeat(character, (1<<20)-100) + `","findings":[],"limitations":"none"}`)
			got, err := ResolveInput(Step{Input: InputRefs{Sources: []string{"verify", "adversarial"}, Multiple: true}}, nil, map[string]json.RawMessage{"verify": report, "adversarial": report})
			if err != nil {
				t.Fatal(err)
			}
			var reports []json.RawMessage
			if err = json.Unmarshal(got, &reports); err != nil || len(reports) != 2 || !bytes.Equal(reports[0], report) || !bytes.Equal(reports[1], report) {
				t.Fatalf("changed reports: %v", err)
			}
		})
	}
}
