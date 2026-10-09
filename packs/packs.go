// Package packs creates editable single-agent workflow starters.
package packs

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/jake-molnia/agent-runtime/definitions"
	"github.com/jake-molnia/agent-runtime/workflows"
	"gopkg.in/yaml.v3"
)

//go:embed briefs/*.txt
var briefs embed.FS

var validName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,62}$`)

func Names() []string {
	return definitions.BuiltinNames()
}

// Write creates a starter without replacing an existing workflow or deployment.
func Write(root, preset, name string) (string, error) {
	agent, exists := definitions.Builtin(preset)
	if !exists {
		return "", fmt.Errorf("unknown preset: %s", preset)
	}
	if !validName.MatchString(name) {
		return "", fmt.Errorf("invalid workflow name: %q", name)
	}
	brief, err := briefs.ReadFile("briefs/" + preset + ".txt")
	if err != nil {
		return "", fmt.Errorf("preset brief: %w", err)
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(agent.Schema, &schema); err != nil {
		return "", err
	}
	_, notebook := schema.Properties["notebook"]
	wire := struct {
		Version  int                       `yaml:"version"`
		Input    map[string]string         `yaml:"input"`
		Notebook bool                      `yaml:"notebook,omitempty"`
		Steps    map[string]workflows.Step `yaml:"steps"`
		Output   string                    `yaml:"output"`
	}{
		Version: 1, Input: map[string]string{"brief": strings.TrimSpace(string(brief))}, Notebook: notebook,
		Steps:  map[string]workflows.Step{"work": {Agent: preset, Input: workflows.InputRefs{Sources: []string{"input"}}}},
		Output: "work",
	}
	data, err := yaml.Marshal(wire)
	if err != nil {
		return "", err
	}
	data = append([]byte("# Edit the brief and configure the agent's approved tools before running.\n# Optional schedule, disabled until uncommented:\n# schedule:\n#   cron: '0 8 * * *'\n#   timezone: UTC\n"), data...)
	directory, err := prepareDirectory(root)
	if err != nil {
		return "", err
	}
	temporary, err := os.CreateTemp(filepath.Dir(directory), ".preset-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(temporary.Name())
	defer temporary.Close()
	if _, err := temporary.Write(data); err != nil {
		return "", err
	}
	if err := temporary.Sync(); err != nil {
		return "", err
	}
	if err := temporary.Close(); err != nil {
		return "", err
	}
	if err := noSymlinks(directory); err != nil {
		return "", err
	}
	path := filepath.Join(directory, name+".yaml")
	if err := os.Link(temporary.Name(), path); err != nil {
		return "", fmt.Errorf("create workflow without overwriting: %w", err)
	}
	folder, err := os.Open(directory)
	if err != nil {
		return "", err
	}
	err = errors.Join(folder.Sync(), folder.Close())
	if err != nil {
		return "", err
	}
	return path, nil
}

func prepareDirectory(root string) (string, error) {
	if root == "" || strings.Contains(root, `\`) {
		return "", errors.New("invalid definitions root")
	}
	for _, component := range strings.Split(root, "/") {
		if component == ".." {
			return "", errors.New("parent traversal forbidden")
		}
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	directory := filepath.Join(absolute, "workflows")
	if err := noSymlinks(directory); err != nil {
		return "", err
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return "", err
	}
	if err := noSymlinks(directory); err != nil {
		return "", err
	}
	return directory, nil
}

func noSymlinks(path string) error {
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err == nil && (info.Mode()&os.ModeSymlink != 0 || !info.IsDir()) {
			return fmt.Errorf("real directory required: %s", current)
		}
		if current == filepath.Dir(current) {
			return nil
		}
	}
}
