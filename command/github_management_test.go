package command

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestRepositoryEnrollmentCommands(t *testing.T) {
	file := filepath.Join(t.TempDir(), "repositories.json")
	if err := os.WriteFile(file, []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := githubCommand(ctx, []string{"repositories", "enroll", "--file", file, "--repository-id", "11", "--installation-id", "7"}); err != nil {
		t.Fatal(err)
	}
	bindings, _ := repositoryBindings(file)
	if len(bindings) != 0 {
		t.Fatal("preview mutated configuration")
	}
	if err := githubCommand(ctx, []string{"repositories", "enroll", "--file", file, "--repository-id", "11", "--installation-id", "7", "--write"}); err != nil {
		t.Fatal(err)
	}
	bindings, err := repositoryBindings(file)
	if err != nil || bindings[11] != 7 {
		t.Fatalf("%v %v", bindings, err)
	}
	if err := githubCommand(ctx, []string{"repositories", "list", "--file", file}); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"repositories", "enroll", "--file", file, "--repository-id", "-1", "--installation-id", "7", "--write"}, {"repositories", "disable", "--file", file, "--repository-id", "11", "--installation-id", "7"}} {
		if githubCommand(ctx, args) == nil {
			t.Fatal("invalid enrollment edit accepted")
		}
	}
	if err := githubCommand(ctx, []string{"repositories", "disable", "--file", file, "--repository-id", "11", "--write"}); err != nil {
		t.Fatal(err)
	}
	bindings, _ = repositoryBindings(file)
	if len(bindings) != 0 {
		t.Fatal("disable failed")
	}
}

func TestAutomationInspectionAndOfflineExplanation(t *testing.T) {
	t.Setenv("AGENT_DEFINITIONS_DIR", "../examples/definitions")
	t.Setenv("AGENT_DEFINITIONS_FILE", "")
	root := t.TempDir()
	bindings := filepath.Join(root, "repositories.json")
	if err := os.WriteFile(bindings, []byte(`{"11":7}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GITHUB_REPOSITORIES_FILE", bindings)
	facts := filepath.Join(root, "facts.json")
	data, _ := json.Marshal(map[string]any{"pull_request": map[string]any{"repository": "owner/repo", "repository_id": 11, "installation_id": 7, "state": "open", "base_branch": "main", "author": "human", "labels": []string{"agent-review"}}, "files": []any{map[string]string{"filename": "src/main.go"}}})
	if err := os.WriteFile(facts, data, 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"list"}, {"inspect", "github-pr-review"}, {"explain", "github-pr-review", "--input", facts}} {
		if err := automationsCommand(args); err != nil {
			t.Fatal(err)
		}
	}
	if err := automationsCommand([]string{"inspect", "nonexistent"}); err == nil {
		t.Fatal("unknown automation accepted")
	}
}
