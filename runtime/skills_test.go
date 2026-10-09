package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func skillBundleFixture(t *testing.T) (string, string) {
	t.Helper()
	root, catalog := t.TempDir(), t.TempDir()
	t.Setenv("AGENT_RUNTIME_SKILL_BUNDLE_ROOT", root)
	t.Setenv("AGENT_RUNTIME_SKILL_CATALOG_ROOT", catalog)
	files := []skillFile{}
	for path, content := range map[string]string{"review/SKILL.md": "trusted instructions", "review/reference.md": "trusted reference"} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(root, path)), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, path), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256([]byte(content))
		files = append(files, skillFile{Path: path, SHA256: hex.EncodeToString(sum[:])})
	}
	sum := sha256.Sum256([]byte("trusted instructions"))
	inventory, _ := json.Marshal([]map[string]string{{"name": "review", "path": "review/SKILL.md", "sha256": hex.EncodeToString(sum[:])}})
	manifest, _ := json.Marshal(files)
	for name, data := range map[string][]byte{"inventory.json": inventory, "sources.json": []byte(`{"revision":"first"}`), "files.json": manifest} {
		if err := os.WriteFile(filepath.Join(root, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(root, "review"), filepath.Join(catalog, "review")); err != nil {
		t.Fatal(err)
	}
	return root, catalog
}

func TestPinnedSkillBundle(t *testing.T) {
	root, _ := skillBundleFixture(t)
	bundle, err := ReadSkillBundle()
	if err != nil {
		t.Fatal(err)
	}
	if len(bundle.Names) != 1 || bundle.Names[0] != "review" || len(bundle.Digest) != 64 {
		t.Fatalf("invalid bundle: %+v", bundle)
	}
	if err := os.WriteFile(filepath.Join(root, "sources.json"), []byte(`{"revision":"next"}`), 0600); err != nil {
		t.Fatal(err)
	}
	supervisor := &Supervisor{Root: t.TempDir(), OpenCodeBinary: "/missing"}
	_, err = supervisor.Initialize(context.Background(), Init{RunID: "test", Password: strings.Repeat("x", 32), Config: json.RawMessage(`{}`), SkillBundleDigest: bundle.Digest})
	if err == nil || err.Error() != "installed skill bundle differs from pinned snapshot" {
		t.Fatalf("changed bundle accepted: %v", err)
	}
}

func TestSkillBundleRejectsSourceChanges(t *testing.T) {
	for _, kind := range []string{"changed_skill", "changed_reference", "deleted_reference", "extra_file", "extra_catalog_file", "redirected_link", "source_symlink"} {
		t.Run(kind, func(t *testing.T) {
			root, catalog := skillBundleFixture(t)
			if _, err := ReadSkillBundle(); err != nil {
				t.Fatal(err)
			}
			var err error
			switch kind {
			case "changed_skill":
				err = os.WriteFile(filepath.Join(root, "review/SKILL.md"), []byte("changed"), 0600)
			case "changed_reference":
				err = os.WriteFile(filepath.Join(root, "review/reference.md"), []byte("changed"), 0600)
			case "deleted_reference":
				err = os.Remove(filepath.Join(root, "review/reference.md"))
			case "extra_file":
				err = os.WriteFile(filepath.Join(root, "unlisted.txt"), []byte("unexpected"), 0600)
			case "extra_catalog_file":
				err = os.WriteFile(filepath.Join(catalog, "unlisted.txt"), []byte("unexpected"), 0600)
			case "redirected_link":
				if err = os.Remove(filepath.Join(catalog, "review")); err == nil {
					err = os.Symlink(t.TempDir(), filepath.Join(catalog, "review"))
				}
			case "source_symlink":
				if err = os.Remove(filepath.Join(root, "review/reference.md")); err == nil {
					err = os.Symlink(filepath.Join(root, "review/SKILL.md"), filepath.Join(root, "review/reference.md"))
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ReadSkillBundle(); err == nil {
				t.Fatal("changed bundle accepted")
			}
		})
	}
}
