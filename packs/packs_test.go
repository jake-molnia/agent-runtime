package packs_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/jake-molnia/agent-runtime/definitions"
	"github.com/jake-molnia/agent-runtime/packs"
	"github.com/jake-molnia/agent-runtime/workflows"
)

const deployment = `version: 1
defaults:
  model: {provider: example, id: example-model}
  execution: {profile: default, timeout_seconds: 60}
profiles:
  default:
    pool: test
    namespace: test
    directory: /workspace
`

func TestAllStartersLoad(t *testing.T) {
	wantNames := append(definitions.BuiltinNames()[:16], "pr-review")
	if !reflect.DeepEqual(packs.Names(), wantNames) {
		t.Fatal("starter catalog differs from builtin catalog")
	}
	root := t.TempDir()
	deploymentPath := filepath.Join(root, "deployment.yaml")
	if err := os.WriteFile(deploymentPath, []byte(deployment), 0600); err != nil {
		t.Fatal(err)
	}
	for _, preset := range packs.Names() {
		path, err := packs.Write(root, preset, "my-"+preset)
		if err != nil {
			t.Fatal(err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if preset != "pr-review" && !bytes.Contains(data, []byte("# schedule:")) {
			t.Fatal("optional schedule example missing")
		}
		if _, err := packs.Write(root, preset, "my-"+preset); !errors.Is(err, os.ErrExist) {
			t.Fatalf("reinstall did not reject existing workflow: %v", err)
		}
		after, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(data, after) {
			t.Fatalf("reinstall changed existing workflow: %v", err)
		}
	}
	catalog, err := definitions.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := workflows.Load(root, catalog)
	if err != nil {
		t.Fatal(err)
	}
	for index, preset := range packs.Names() {
		workflow := loaded["my-"+preset].Workflow
		if preset == "pr-review" {
			if len(workflow.Steps) != 7 || workflow.Steps[workflow.Output].Agent != "pr-writer" {
				t.Fatalf("invalid PR pack: %+v", workflow)
			}
			if _, err := os.Stat(filepath.Join(root, "agents")); !os.IsNotExist(err) {
				t.Fatal("pack copied runtime-owned agent definitions into configuration")
			}
			continue
		}
		if workflow.Schedule != nil || workflow.Notebook != (index >= 3) || len(workflow.Steps) != 1 || workflow.Steps[workflow.Output].Agent != preset {
			t.Fatalf("incorrect starter for %s: %#v", preset, workflow)
		}
		input, err := workflow.InvocationInput(nil)
		if err != nil || !bytes.Contains(input, []byte("brief")) {
			t.Fatalf("default brief missing for %s: %s, %v", preset, input, err)
		}
	}
	after, err := os.ReadFile(deploymentPath)
	if err != nil || string(after) != deployment {
		t.Fatalf("deployment changed: %v", err)
	}
}

func TestInvalidNamesAndPaths(t *testing.T) {
	for _, name := range []string{"", "../escape", "nested/name", "name.yaml", strings.Repeat("a", 64), `..\escape`} {
		if _, err := packs.Write(t.TempDir(), "researcher", name); err == nil {
			t.Fatalf("invalid workflow name accepted: %q", name)
		}
	}
	if _, err := packs.Write(t.TempDir(), "missing", "research"); err == nil {
		t.Fatal("unknown preset accepted")
	}
	for _, root := range []string{"", "../escape", `bad\root`} {
		if _, err := packs.Write(root, "researcher", "research"); err == nil {
			t.Fatalf("invalid root accepted: %q", root)
		}
	}
}

func TestSymlinksRejected(t *testing.T) {
	for _, location := range []string{"root", "ancestor", "workflows", "file", "dangling"} {
		t.Run(location, func(t *testing.T) {
			base, target := t.TempDir(), t.TempDir()
			root := filepath.Join(base, "config")
			if err := os.Mkdir(root, 0700); err != nil {
				t.Fatal(err)
			}
			var link string
			switch location {
			case "root":
				root = filepath.Join(base, "linked")
				link = root
			case "ancestor":
				link = filepath.Join(base, "linked")
				root = filepath.Join(link, "nested")
			case "workflows":
				link = filepath.Join(root, "workflows")
			case "file", "dangling":
				if err := os.Mkdir(filepath.Join(root, "workflows"), 0700); err != nil {
					t.Fatal(err)
				}
				link = filepath.Join(root, "workflows", "research.yaml")
				if location == "dangling" {
					target = filepath.Join(target, "absent")
				}
			}
			if err := os.Symlink(target, link); err != nil {
				t.Fatal(err)
			}
			if _, err := packs.Write(root, "researcher", "research"); err == nil {
				t.Fatalf("symlink accepted: %s", link)
			}
			info, err := os.Lstat(link)
			if err != nil || info.Mode()&os.ModeSymlink == 0 {
				t.Fatalf("symlink was replaced: %v", err)
			}
		})
	}
}

func TestConcurrentCreationHasOneWinner(t *testing.T) {
	root := t.TempDir()
	var group sync.WaitGroup
	results := make(chan error, 8)
	for range cap(results) {
		group.Go(func() {
			_, err := packs.Write(root, "researcher", "research")
			results <- err
		})
	}
	group.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		} else if !errors.Is(err, os.ErrExist) {
			t.Fatal(err)
		}
	}
	if winners != 1 {
		t.Fatalf("expected one exclusive creation, got %d", winners)
	}
}

func TestExampleConfigurationsLoad(t *testing.T) {
	for _, directory := range []string{"connected", "recurring", "native"} {
		t.Run(directory, func(t *testing.T) {
			root, err := filepath.Abs(filepath.Join("..", "examples", directory))
			if err != nil {
				t.Fatal(err)
			}
			catalog, err := definitions.Load(root)
			if err != nil {
				t.Fatal(err)
			}
			loaded, err := workflows.Load(root, catalog)
			if err != nil {
				t.Fatal(err)
			}
			if len(loaded) == 0 {
				t.Fatal("example contains no workflows")
			}
			for name, snapshot := range loaded {
				if directory != "recurring" && snapshot.Workflow.Schedule != nil {
					t.Fatalf("generic example enables a schedule: %s", name)
				}
				if directory == "recurring" && snapshot.Workflow.Schedule == nil {
					t.Fatalf("recurring example lacks schedule: %s", name)
				}
				if !snapshot.Workflow.Notebook {
					t.Fatalf("example lacks notebook: %s", name)
				}
			}
		})
	}
}
