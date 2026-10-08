package definitions

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/jake-molnia/agent-runtime/orchestration"
	"gopkg.in/yaml.v3"
)

type Model struct {
	Provider string `yaml:"provider" json:"provider"`
	ID       string `yaml:"id" json:"id"`
}

type Execution struct {
	Profile        string `yaml:"profile" json:"profile"`
	TimeoutSeconds int    `yaml:"timeout_seconds" json:"timeout_seconds"`
}

type Agent struct {
	Name         string            `yaml:"-" json:"name"`
	Digest       string            `yaml:"-" json:"digest"`
	Version      int               `yaml:"version" json:"version"`
	Description  string            `yaml:"description" json:"description"`
	Model        Model             `yaml:"model" json:"model"`
	Execution    Execution         `yaml:"execution" json:"execution"`
	Capabilities []string          `yaml:"capabilities" json:"capabilities"`
	OutputSchema string            `yaml:"output_schema,omitempty" json:"output_schema,omitempty"`
	Instructions string            `yaml:"-" json:"instructions"`
	Skills       map[string]string `yaml:"-" json:"skills"`
	Schema       json.RawMessage   `yaml:"-" json:"schema,omitempty"`
}

type Trigger struct {
	Adapter string   `yaml:"adapter" json:"adapter"`
	Actions []string `yaml:"actions" json:"actions"`
}

type Policy struct {
	Concurrency   string `yaml:"concurrency" json:"concurrency"`
	Limit         int    `yaml:"limit" json:"limit"`
	Deduplication string `yaml:"deduplication" json:"deduplication"`
}

type Automation struct {
	Name    string  `yaml:"-" json:"name"`
	Version int     `yaml:"version" json:"version"`
	Agent   string  `yaml:"agent" json:"agent"`
	Handler string  `yaml:"handler" json:"handler"`
	Trigger Trigger `yaml:"trigger" json:"trigger"`
	Policy  Policy  `yaml:"policy" json:"policy"`
}

type Profile struct {
	Pool         string            `yaml:"pool" json:"pool"`
	Namespace    string            `yaml:"namespace" json:"namespace"`
	Directory    string            `yaml:"directory" json:"directory"`
	Tags         []string          `yaml:"tags" json:"tags"`
	Capabilities []string          `yaml:"capabilities" json:"capabilities"`
	SecretFiles  map[string]string `yaml:"secret_files" json:"secret_files"`
	Config       json.RawMessage   `yaml:"-" json:"config,omitempty"`
}

func (profile *Profile) UnmarshalYAML(node *yaml.Node) error {
	type plain Profile
	var wire struct {
		plain  `yaml:",inline"`
		Config any `yaml:"config"`
	}
	data, err := yaml.Marshal(node)
	if err != nil {
		return err
	}
	if err := strictYAML(data, &wire); err != nil {
		return err
	}
	*profile = Profile(wire.plain)
	if wire.Config != nil {
		profile.Config, err = json.Marshal(wire.Config)
	}
	return err
}

type Catalog struct {
	Agents      map[string]Agent
	Automations map[string]Automation
	Profiles    map[string]Profile
}

var validName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

func Load(root string) (*Catalog, error) {
	if err := noSymlinks(root); err != nil {
		return nil, err
	}
	if err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink forbidden: %s", path)
		}
		if !entry.IsDir() && !entry.Type().IsRegular() {
			return fmt.Errorf("non-regular file: %s", path)
		}
		return nil
	}); err != nil {
		return nil, err
	}
	catalog := &Catalog{Agents: map[string]Agent{}, Automations: map[string]Automation{}, Profiles: map[string]Profile{}}
	var deployment struct {
		Version  int                `yaml:"version"`
		Profiles map[string]Profile `yaml:"profiles"`
	}
	if err := readYAML(filepath.Join(root, "deployment.yaml"), &deployment); err != nil {
		return nil, err
	}
	if deployment.Version != 1 {
		return nil, fmt.Errorf("unsupported deployment version: %d", deployment.Version)
	}
	for name, profile := range deployment.Profiles {
		if !validName.MatchString(name) {
			return nil, fmt.Errorf("invalid profile name: %q", name)
		}
		if err := validateProfile(profile); err != nil {
			return nil, fmt.Errorf("profile %s: %w", name, err)
		}
		catalog.Profiles[name] = profile
	}
	entries, err := optionalEntries(filepath.Join(root, "agents"))
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || !validName.MatchString(name) {
			return nil, fmt.Errorf("invalid agent directory: %s", name)
		}
		directory := filepath.Join(root, "agents", name)
		var agent Agent
		if err := readYAML(filepath.Join(directory, "agent.yaml"), &agent); err != nil {
			return nil, err
		}
		agent.Name = name
		instructions, err := readRegular(filepath.Join(directory, "instructions.md"))
		if err != nil {
			return nil, err
		}
		agent.Instructions = string(instructions)
		agent.Skills = map[string]string{}
		skills, err := optionalEntries(filepath.Join(directory, "skills"))
		if err != nil {
			return nil, err
		}
		for _, skill := range skills {
			if !skill.IsDir() || !validName.MatchString(skill.Name()) {
				return nil, fmt.Errorf("invalid skill directory: %s", skill.Name())
			}
			content, err := readRegular(filepath.Join(directory, "skills", skill.Name(), "SKILL.md"))
			if err != nil {
				return nil, err
			}
			agent.Skills[skill.Name()] = string(content)
		}
		if agent.OutputSchema != "" {
			if !filename(agent.OutputSchema) {
				return nil, fmt.Errorf("invalid output_schema: %q", agent.OutputSchema)
			}
			agent.Schema, err = readRegular(filepath.Join(directory, agent.OutputSchema))
			if err != nil {
				return nil, err
			}
		}
		catalog.Agents[name] = agent
		snapshot, err := catalog.Snapshot(name)
		if err != nil {
			return nil, err
		}
		catalog.Agents[name] = snapshot.Agent
	}
	entries, err = optionalEntries(filepath.Join(root, "automations"))
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		name := strings.TrimSuffix(entry.Name(), ".yaml")
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") || !validName.MatchString(name) {
			return nil, fmt.Errorf("invalid automation file: %s", entry.Name())
		}
		var automation Automation
		if err := readYAML(filepath.Join(root, "automations", entry.Name()), &automation); err != nil {
			return nil, err
		}
		automation.Name = name
		if err := validateAutomation(automation, catalog.Agents); err != nil {
			return nil, fmt.Errorf("automation %s: %w", name, err)
		}
		catalog.Automations[name] = automation
	}
	return catalog, nil
}

func (catalog *Catalog) Resolve(name string) (orchestration.Definition, error) {
	snapshot, err := catalog.Snapshot(name)
	if err != nil {
		return orchestration.Definition{}, err
	}
	return snapshot.Definition()
}

func (catalog *Catalog) Save(root string) error {
	for name := range catalog.Agents {
		snapshot, err := catalog.Snapshot(name)
		if err != nil {
			return err
		}
		if err := SaveSnapshot(root, snapshot); err != nil {
			return err
		}
	}
	return nil
}

func validateAutomation(automation Automation, agents map[string]Agent) error {
	if automation.Name == "agent-run" {
		return errors.New("agent-run is a reserved workflow name")
	}
	if automation.Version != 1 {
		return fmt.Errorf("unsupported version: %d", automation.Version)
	}
	agent, exists := agents[automation.Agent]
	if !exists {
		return fmt.Errorf("unknown agent: %s", automation.Agent)
	}
	if automation.Handler != "github.pr-review" || automation.Trigger.Adapter != "github.pull_request" {
		return errors.New("unsupported handler or trigger")
	}
	if !contains(agent.Capabilities, "github.diff") {
		return errors.New("github.pr-review requires github.diff")
	}
	if len(agent.Schema) == 0 {
		return errors.New("github.pr-review requires output_schema")
	}
	if len(automation.Trigger.Actions) == 0 {
		return errors.New("trigger actions required")
	}
	seen := map[string]bool{}
	for _, action := range automation.Trigger.Actions {
		if seen[action] || !contains([]string{"opened", "synchronize", "ready_for_review"}, action) {
			return fmt.Errorf("unsupported or repeated action: %s", action)
		}
		seen[action] = true
	}
	if automation.Policy != (Policy{Concurrency: "pull-request", Limit: 1, Deduplication: "reviewed-revision"}) {
		return errors.New("unsupported policy")
	}
	return nil
}

func capabilities(values []string) error {
	seen := map[string]bool{}
	for _, value := range values {
		if value != "github.diff" || seen[value] {
			return fmt.Errorf("unsupported or repeated capability: %s", value)
		}
		seen[value] = true
	}
	return nil
}

func contains(values []string, value string) bool {
	for _, item := range values {
		if item == value {
			return true
		}
	}
	return false
}

func filename(name string) bool {
	return name != "" && name != "." && name != ".." && !strings.ContainsAny(name, `/\`) && filepath.Base(name) == name
}

func strictYAML(data []byte, target any) error {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("expected exactly one YAML document")
	}
	return nil
}

func readYAML(path string, target any) error {
	data, err := readRegular(path)
	if err == nil {
		err = strictYAML(data, target)
	}
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

func optionalEntries(path string) ([]os.DirEntry, error) {
	entries, err := os.ReadDir(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	return entries, err
}

func noSymlinks(path string) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	for current := absolute; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink forbidden: %s", current)
		}
		if current == filepath.Dir(current) {
			break
		}
	}
	return nil
}

func readRegular(path string) ([]byte, error) {
	if err := noSymlinks(path); err != nil {
		return nil, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("regular file required: %s", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(info, opened) {
		return nil, fmt.Errorf("file changed while opening: %s", path)
	}
	return io.ReadAll(file)
}
