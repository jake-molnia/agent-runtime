package workflows_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/jake-molnia/agent-runtime/definitions"
	"github.com/jake-molnia/agent-runtime/packs"
	"github.com/jake-molnia/agent-runtime/workflows"
)

func presetRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	data := `version: 1
defaults:
  model: {provider: example, id: default-model}
  execution: {profile: default, timeout_seconds: 120}
profiles:
  default: {pool: example, namespace: example, directory: /workspace}
`
	if err := os.WriteFile(filepath.Join(root, "deployment.yaml"), []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := packs.Write(root, "pr-review", "review-change"); err != nil {
		t.Fatal(err)
	}
	return root
}

func loadPreset(t *testing.T, root string) workflows.Snapshot {
	t.Helper()
	catalog, err := definitions.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	plans, err := workflows.Load(root, catalog)
	if err != nil {
		t.Fatal(err)
	}
	return plans["review-change"]
}

func TestReviewPackNeedsOnlySelectionAndDeployment(t *testing.T) {
	root := presetRoot(t)
	plan := loadPreset(t, root)
	if plan.Workflow.Use != "" || len(plan.Workflow.Steps) != 7 || plan.Workflow.Output != "writeup" {
		t.Fatalf("pack was not resolved: %+v", plan.Workflow)
	}
	for _, id := range []string{"review", "adversarial", "security", "dependencies"} {
		if !reflect.DeepEqual(plan.Workflow.Steps[id].Input.Sources, []string{"input"}) {
			t.Fatalf("%s is not an independent investigation", id)
		}
	}
	if !reflect.DeepEqual(plan.Workflow.Steps["verify"].Input.Sources, []string{"input", "review", "adversarial", "security", "dependencies"}) ||
		!reflect.DeepEqual(plan.Workflow.Steps["writeup"].Input.Sources, []string{"input", "verify", "triage"}) {
		t.Fatal("review evidence or verification gate lost")
	}
	for _, snapshot := range plan.Agents {
		if snapshot.Agent.Model.ID != "default-model" || len(snapshot.Agent.Tools) != 0 || len(snapshot.Agent.MCP) != 0 {
			t.Fatal("pack imposed deployment settings or granted tools")
		}
	}
	store := t.TempDir()
	if err := workflows.Save(store, plan); err != nil {
		t.Fatal(err)
	}
	// Adoption can override model selection without owning or copying any prompt.
	agentPath := filepath.Join(root, "agents", "pr-security")
	if err := os.MkdirAll(agentPath, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentPath, "agent.yaml"), []byte("model: {id: security-model}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	changed := loadPreset(t, root)
	if changed.Agents["security"].Agent.Model.ID != "security-model" || changed.Digest == plan.Digest {
		t.Fatal("deployment override was not pinned")
	}
	if changed.Agents["security"].Agent.Instructions != plan.Agents["security"].Agent.Instructions {
		t.Fatal("model override changed bundled instructions")
	}
	replay, err := workflows.Read(store, plan.Digest)
	if err != nil || !reflect.DeepEqual(replay, plan) {
		t.Fatalf("saved graph changed after configuration update: %v", err)
	}
}

func TestPresetSelectionRejectsAmbiguity(t *testing.T) {
	for _, body := range []string{
		"use: missing\n", "use: ../pr-review\n", "use: pr-review\nsteps: {}\n",
		"use: pr-review\noutput: custom\n", "use: pr-review\nnotebook: true\n",
		"use: pr-review\nunknown: true\n",
	} {
		t.Run(strings.ReplaceAll(body, "\n", " "), func(t *testing.T) {
			root := presetRoot(t)
			if err := os.WriteFile(filepath.Join(root, "workflows", "review-change.yaml"), []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			catalog, err := definitions.Load(root)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := workflows.Load(root, catalog); err == nil {
				t.Fatal("invalid pack selection accepted")
			}
		})
	}
}
