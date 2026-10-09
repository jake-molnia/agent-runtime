package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
)

// SkillBundle identifies the immutable skill sources installed with the image.
type SkillBundle struct {
	Digest string
	Names  []string
}

type skillFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

var skillName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

func ReadSkillBundle() (SkillBundle, error) {
	root := os.Getenv("AGENT_RUNTIME_SKILL_BUNDLE_ROOT")
	if root == "" {
		root = "/opt/agent-skill-bundles"
	}
	catalog := os.Getenv("AGENT_RUNTIME_SKILL_CATALOG_ROOT")
	if catalog == "" {
		catalog = "/opt/agent-skills"
	}
	for _, path := range []string{root, catalog} {
		resolved, err := filepath.EvalSymlinks(path)
		if !filepath.IsAbs(path) || filepath.Clean(path) != path || err != nil || resolved != path {
			return SkillBundle{}, errors.New("skill roots must be existing absolute directories without symlinks")
		}
		info, err := os.Stat(path)
		if err != nil || !info.IsDir() {
			return SkillBundle{}, errors.New("skill root is not a directory")
		}
	}
	metadata := map[string][]byte{}
	for _, name := range []string{"inventory.json", "sources.json", "files.json"} {
		path := filepath.Join(root, name)
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() > 4<<20 {
			return SkillBundle{}, errors.New("trusted skill metadata unavailable")
		}
		data, err := os.ReadFile(path)
		if err != nil || !json.Valid(data) {
			return SkillBundle{}, errors.New("invalid trusted skill metadata")
		}
		metadata[name] = data
	}
	var files []skillFile
	if err := json.Unmarshal(metadata["files.json"], &files); err != nil || len(files) == 0 {
		return SkillBundle{}, errors.New("invalid trusted skill file manifest")
	}
	expected := map[string]string{}
	for _, file := range files {
		digest, err := hex.DecodeString(file.SHA256)
		if !filepath.IsLocal(file.Path) || filepath.Clean(file.Path) != file.Path || expected[file.Path] != "" || metadata[file.Path] != nil || err != nil || len(digest) != sha256.Size || hex.EncodeToString(digest) != file.SHA256 {
			return SkillBundle{}, errors.New("invalid trusted skill file entry")
		}
		expected[file.Path] = file.SHA256
	}
	actual := map[string]bool{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		if !entry.Type().IsRegular() {
			return errors.New("non-regular trusted skill source")
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if metadata[relative] != nil {
			return nil
		}
		digest, exists := expected[relative]
		if !exists {
			return errors.New("unexpected trusted skill source file")
		}
		file, err := os.Open(path)
		if err != nil {
			return err
		}
		hash := sha256.New()
		_, readErr := io.Copy(hash, file)
		if err := errors.Join(readErr, file.Close()); err != nil {
			return err
		}
		if hex.EncodeToString(hash.Sum(nil)) != digest {
			return errors.New("trusted skill source digest mismatch")
		}
		actual[relative] = true
		return nil
	})
	if err != nil {
		return SkillBundle{}, err
	}
	if len(actual) != len(expected) {
		return SkillBundle{}, errors.New("trusted skill source file missing")
	}
	var entries []struct {
		Name   string `json:"name"`
		Path   string `json:"path"`
		SHA256 string `json:"sha256"`
	}
	if err := json.Unmarshal(metadata["inventory.json"], &entries); err != nil || len(entries) == 0 {
		return SkillBundle{}, errors.New("invalid trusted skill inventory")
	}
	bundle := SkillBundle{}
	seen := map[string]bool{}
	for _, entry := range entries {
		if !skillName.MatchString(entry.Name) || seen[entry.Name] || filepath.Base(entry.Path) != "SKILL.md" || expected[entry.Path] == "" || expected[entry.Path] != entry.SHA256 {
			return SkillBundle{}, errors.New("invalid trusted skill inventory entry")
		}
		link := filepath.Join(catalog, entry.Name)
		info, err := os.Lstat(link)
		if err != nil || info.Mode()&os.ModeSymlink == 0 {
			return SkillBundle{}, errors.New("trusted skill catalog link missing")
		}
		target, err := filepath.EvalSymlinks(link)
		if err != nil || target != filepath.Dir(filepath.Join(root, entry.Path)) {
			return SkillBundle{}, errors.New("trusted skill catalog link redirected")
		}
		seen[entry.Name] = true
		bundle.Names = append(bundle.Names, entry.Name)
	}
	catalogEntries, err := os.ReadDir(catalog)
	if err != nil || len(catalogEntries) != len(seen) {
		return SkillBundle{}, errors.New("unexpected trusted skill catalog entry")
	}
	hash := sha256.New()
	for _, name := range []string{"sources.json", "inventory.json", "files.json"} {
		hash.Write(metadata[name])
		hash.Write([]byte{0})
	}
	bundle.Digest = hex.EncodeToString(hash.Sum(nil))
	return bundle, nil
}
