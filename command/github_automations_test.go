package command

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/jake-molnia/agent-runtime/definitions"
)

func TestOptionalGitHubManifestSelectionAndDefaults(t *testing.T) {
	t.Setenv("AGENT_DEFINITIONS_DIR", "../examples/definitions")
	directory := t.TempDir()
	t.Setenv("GITHUB_AUTOMATIONS_DIR", directory)
	catalog, err := definitions.Load("../examples/definitions")
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile("../examples/github-pr-review.selection.yaml")
	if err != nil {
		t.Fatal(err)
	}
	manifest = bytes.ReplaceAll(manifest, []byte("agent: github-reviewer"), []byte("agent: code-review"))
	file := filepath.Join(directory, "review.yaml")
	if err := os.WriteFile(file, manifest, 0600); err != nil {
		t.Fatal(err)
	}
	automations, err := loadGitHubAutomations(catalog)
	if err != nil {
		t.Fatal(err)
	}
	automation := automations["review"]
	if automation.Policy.Limit != 1 || len(automation.Selection.Repositories.Include) != 2 || automation.Selection.PullRequests.Drafts == nil || *automation.Selection.PullRequests.Drafts {
		t.Fatalf("%+v", automation)
	}
	for _, invalid := range [][]byte{bytes.ReplaceAll(manifest, []byte("require_any"), []byte("required_any")), bytes.ReplaceAll(manifest, []byte("src/**"), []byte("src/[")), bytes.ReplaceAll(manifest, []byte("labeled"), []byte("unsupported"))} {
		if err := os.WriteFile(file, invalid, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadGitHubAutomations(catalog); err == nil {
			t.Fatal("invalid GitHub manifest accepted")
		}
	}
	t.Setenv("GITHUB_AUTOMATIONS_DIR", filepath.Join(directory, "absent"))
	if optional, err := loadGitHubAutomations(catalog); err != nil || len(optional) != 0 {
		t.Fatalf("optional adapter became mandatory: %v", err)
	}
}
