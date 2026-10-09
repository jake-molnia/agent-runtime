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
	BuiltinSkills []string          `yaml:"builtin_skills,omitempty" json:"builtin_skills,omitempty"`
	Extends       string            `yaml:"extends,omitempty" json:"extends,omitempty"`
	MCP           []string          `yaml:"mcp,omitempty" json:"mcp,omitempty"`
	Name          string            `yaml:"-" json:"name"`
	Digest        string            `yaml:"-" json:"digest"`
	Version       int               `yaml:"version" json:"version"`
	Description   string            `yaml:"description" json:"description"`
	Model         Model             `yaml:"model" json:"model"`
	Execution     Execution         `yaml:"execution" json:"execution"`
	Capabilities  []string          `yaml:"capabilities" json:"capabilities"`
	OutputSchema  string            `yaml:"output_schema,omitempty" json:"output_schema,omitempty"`
	Instructions  string            `yaml:"-" json:"instructions"`
	Skills        map[string]string `yaml:"-" json:"skills"`
	Schema        json.RawMessage   `yaml:"-" json:"schema,omitempty"`
}

type Profile struct {
	MCP          []string          `yaml:"mcp,omitempty" json:"mcp,omitempty"`
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
	Agents     map[string]Agent
	Profiles   map[string]Profile
	Defaults   AgentDefaults
	MCPServers map[string]MCPServer
}

var validName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

func Load(root string) (*Catalog, error) {
	if err := noSymlinks(root); err != nil {
		return nil, err
	}
	if _, err := os.Lstat(filepath.Join(root, "automations")); err == nil {
		return nil, errors.New("automations are no longer loaded by definitions; migrate them to the external workflow configuration")
	} else if !errors.Is(err, fs.ErrNotExist) {
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
	catalog := &Catalog{Agents: map[string]Agent{}, Profiles: map[string]Profile{}}
	var deployment struct {
		Version    int                  `yaml:"version"`
		Profiles   map[string]Profile   `yaml:"profiles"`
		Defaults   *AgentDefaults       `yaml:"defaults"`
		MCPServers map[string]MCPServer `yaml:"mcp_servers"`
	}
	if err := readYAML(filepath.Join(root, "deployment.yaml"), &deployment); err != nil {
		return nil, err
	}
	if deployment.Version != 1 {
		return nil, fmt.Errorf("unsupported deployment version: %d", deployment.Version)
	}
	catalog.MCPServers = deployment.MCPServers
	if err := validateMCPServers(catalog.MCPServers); err != nil {
		return nil, err
	}
	if deployment.Defaults != nil {
		catalog.Defaults = *deployment.Defaults
		for _, name := range BuiltinNames() {
			agent, _ := Builtin(name)
			agent.Model, agent.Execution = catalog.Defaults.Model, catalog.Defaults.Execution
			catalog.Agents[name] = agent
		}
	}
	for name, profile := range deployment.Profiles {
		if !validName.MatchString(name) {
			return nil, fmt.Errorf("invalid profile name: %q", name)
		}
		if err := validateProfile(profile); err != nil {
			return nil, fmt.Errorf("profile %s: %w", name, err)
		}
		if len(profile.Tags) > 0 && !contains(profile.Tags, "tag:agent-sandbox") {
			return nil, fmt.Errorf("profile %s: tag:agent-sandbox required when tags are configured", name)
		}
		for _, server := range profile.MCP {
			if _, exists := catalog.MCPServers[server]; !exists {
				return nil, fmt.Errorf("profile %s: unknown MCP server: %s", name, server)
			}
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
		data, err := readRegular(filepath.Join(directory, "agent.yaml"))
		if err != nil {
			return nil, err
		}
		var authored Agent
		if err := strictYAML(data, &authored); err != nil {
			return nil, fmt.Errorf("agent %s: %w", name, err)
		}
		base := authored.Extends
		if base == "" {
			base = name
		}
		agent, inherited := Builtin(base)
		if !inherited && authored.Extends != "" {
			return nil, fmt.Errorf("agent %s: unknown builtin: %s", name, base)
		}
		agent.Model, agent.Execution = catalog.Defaults.Model, catalog.Defaults.Execution
		if err := strictYAML(data, &agent); err != nil {
			return nil, fmt.Errorf("agent %s: %w", name, err)
		}
		agent.Name = name
		instructions, err := readRegular(filepath.Join(directory, "instructions.md"))
		if err != nil && !(inherited && errors.Is(err, fs.ErrNotExist)) {
			return nil, err
		}
		if err == nil {
			agent.Instructions = string(instructions)
		}
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
	}
	for name := range catalog.Agents {
		snapshot, err := catalog.Snapshot(name)
		if err != nil {
			return nil, err
		}
		catalog.Agents[name] = snapshot.Agent
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
