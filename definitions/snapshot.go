package definitions

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	runtimeapi "github.com/jake-molnia/agent-runtime/runtime"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

type Snapshot struct {
	SkillBundleDigest string               `json:"skill_bundle_digest,omitempty"`
	Agent             Agent                `json:"agent"`
	Profile           Profile              `json:"profile"`
	MCPServers        map[string]MCPServer `json:"mcp_servers,omitempty"`
	CompiledPolicy    string               `json:"compiled_policy,omitempty"`
}

const compiledPolicy = "opencode-v2.0.26:authored-primary:sandbox-unrestricted:schema-system:v5"

func (catalog *Catalog) Snapshot(name string) (Snapshot, error) {
	agent, exists := catalog.Agents[name]
	if !exists {
		return Snapshot{}, fmt.Errorf("unknown agent: %s", name)
	}
	profile, exists := catalog.Profiles[agent.Execution.Profile]
	if !exists {
		return Snapshot{}, fmt.Errorf("agent %s: unknown profile: %s", name, agent.Execution.Profile)
	}
	snapshot := Snapshot{Agent: agent, Profile: profile, CompiledPolicy: compiledPolicy}
	for _, name := range agent.MCP {
		server, exists := catalog.MCPServers[name]
		if !exists {
			return Snapshot{}, fmt.Errorf("agent %s: unknown MCP server: %s", agent.Name, name)
		}
		if snapshot.MCPServers == nil {
			snapshot.MCPServers = map[string]MCPServer{}
		}
		snapshot.MCPServers[name] = server
	}
	if len(snapshot.Agent.BuiltinSkills) > 0 {
		bundle, err := runtimeapi.ReadSkillBundle()
		if err != nil {
			return Snapshot{}, err
		}
		for _, name := range agent.BuiltinSkills {
			if !contains(bundle.Names, name) {
				return Snapshot{}, fmt.Errorf("unknown builtin skill: %s", name)
			}
		}
		snapshot.SkillBundleDigest = bundle.Digest
	}
	if err := snapshot.validate(); err != nil {
		return Snapshot{}, fmt.Errorf("agent %s: %w", name, err)
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot = Snapshot{}
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return Snapshot{}, err
	}
	snapshot.Agent.Digest, err = snapshot.digest()
	return snapshot, err
}

func (snapshot Snapshot) ValidateOutput(output json.RawMessage) error {
	if err := snapshot.verify(); err != nil {
		return err
	}
	schema, err := compileSchema(snapshot.Agent.Schema)
	if err != nil {
		return err
	}
	if schema == nil {
		return errors.New("agent has no output schema")
	}
	value, err := jsonschema.UnmarshalJSON(bytes.NewReader(output))
	if err != nil {
		return fmt.Errorf("invalid output JSON: %w", err)
	}
	return schema.Validate(value)
}

func (snapshot Snapshot) validate() error {
	agent := snapshot.Agent
	if err := validateMCPReferences(agent.BuiltinSkills); err != nil {
		return fmt.Errorf("builtin_skills: %w", err)
	}
	if err := validateMCPReferences(agent.MCP); err != nil {
		return err
	}
	if err := validateMCPServers(snapshot.MCPServers); err != nil {
		return err
	}
	if len(agent.MCP) != len(snapshot.MCPServers) {
		return errors.New("snapshot must contain only selected MCP bindings")
	}
	if len(agent.BuiltinSkills) > 0 {
		digest, err := hex.DecodeString(snapshot.SkillBundleDigest)
		if err != nil || len(digest) != sha256.Size || strings.ToLower(snapshot.SkillBundleDigest) != snapshot.SkillBundleDigest {
			return errors.New("builtin skills require a pinned bundle digest")
		}
	}
	if len(agent.BuiltinSkills) == 0 && snapshot.SkillBundleDigest != "" {
		return errors.New("skill bundle digest without selected skills")
	}
	if snapshot.CompiledPolicy != compiledPolicy {
		return errors.New("unsupported compiled policy: drain old runs with their original worker, then reload definitions for sandbox-unrestricted v5")
	}
	for _, name := range agent.MCP {
		if _, exists := snapshot.MCPServers[name]; !exists {
			return fmt.Errorf("unknown MCP server: %s", name)
		}
		if !contains(snapshot.Profile.MCP, name) {
			return fmt.Errorf("profile does not grant MCP server: %s", name)
		}
	}
	if !validName.MatchString(agent.Name) || !validName.MatchString(agent.Execution.Profile) {
		return errors.New("invalid agent or profile name")
	}
	if agent.Version != 1 {
		return fmt.Errorf("unsupported version: %d", agent.Version)
	}
	if strings.TrimSpace(agent.Description) == "" || strings.TrimSpace(agent.Instructions) == "" {
		return errors.New("description and instructions required")
	}
	if !validName.MatchString(agent.Model.Provider) || strings.TrimSpace(agent.Model.ID) == "" {
		return errors.New("model provider and id required")
	}
	if agent.Execution.TimeoutSeconds <= 0 || agent.Execution.TimeoutSeconds > 86400 {
		return errors.New("timeout_seconds must be between 1 and 86400")
	}
	if err := capabilities(agent.Capabilities); err != nil {
		return err
	}
	if err := validateProfile(snapshot.Profile); err != nil {
		return err
	}
	for _, capability := range agent.Capabilities {
		if !contains(snapshot.Profile.Capabilities, capability) {
			return fmt.Errorf("profile does not grant capability: %s", capability)
		}
	}
	for name, content := range agent.Skills {
		if !validName.MatchString(name) || strings.TrimSpace(content) == "" {
			return fmt.Errorf("invalid or empty skill: %s", name)
		}
	}
	if agent.OutputSchema != "" && len(agent.Schema) == 0 {
		return errors.New("output_schema requires schema content")
	}
	if agent.OutputSchema != "" && !filename(agent.OutputSchema) {
		return errors.New("output_schema must be a filename")
	}
	_, err := compileSchema(agent.Schema)
	return err
}

var policyTag = regexp.MustCompile(`^tag:[a-z][a-z0-9-]*$`)

func validateProfile(profile Profile) error {
	seenTags := map[string]bool{}
	for _, tag := range profile.Tags {
		if !policyTag.MatchString(tag) || seenTags[tag] {
			return fmt.Errorf("invalid or duplicate policy tag: %q", tag)
		}
		seenTags[tag] = true
	}
	if err := validateMCPReferences(profile.MCP); err != nil {
		return err
	}
	if strings.TrimSpace(profile.Pool) == "" || strings.TrimSpace(profile.Namespace) == "" || !approvedPath(profile.Directory) {
		return errors.New("pool, namespace and absolute directory required")
	}
	if err := capabilities(profile.Capabilities); err != nil {
		return err
	}
	for provider, path := range profile.SecretFiles {
		if !validName.MatchString(provider) || !approvedPath(path) {
			return fmt.Errorf("invalid provider secret binding: %s", provider)
		}
	}
	if len(profile.Config) == 0 {
		return nil
	}
	var config map[string]any
	if err := json.Unmarshal(profile.Config, &config); err != nil {
		return err
	}
	if config == nil {
		return errors.New("profile config must be an object")
	}
	if err := rejectLiteralCredentials(config); err != nil {
		return err
	}
	for key, value := range config {
		if key != "providers" {
			return fmt.Errorf("profile config key forbidden: %s", key)
		}
		if _, ok := value.(map[string]any); !ok {
			return errors.New("profile providers must be an object")
		}
	}
	placeholders := map[string]string{}
	for provider := range profile.SecretFiles {
		placeholders[provider] = ""
	}
	_, err := resolveSecrets(config, profile.SecretFiles, placeholders)
	return err
}

func approvedPath(path string) bool {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || strings.Contains(path, `\`) {
		return false
	}
	for _, component := range strings.Split(path, "/") {
		if component == ".." {
			return false
		}
	}
	return true
}

type schemaLoader struct{}

func (schemaLoader) Load(url string) (any, error) {
	return nil, fmt.Errorf("external schema resource forbidden: %s", url)
}

func compileSchema(raw json.RawMessage) (*jsonschema.Schema, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("invalid output schema: %w", err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.UseLoader(schemaLoader{})
	const resource = "https://definitions.invalid/output.schema.json"
	if err := compiler.AddResource(resource, document); err != nil {
		return nil, err
	}
	schema, err := compiler.Compile(resource)
	if err != nil {
		return nil, fmt.Errorf("invalid output schema: %w", err)
	}
	return schema, nil
}

func (snapshot Snapshot) digest() (string, error) {
	snapshot.Agent.Digest = ""
	policy := compiledPolicy
	if snapshot.CompiledPolicy != "" {
		policy = snapshot.CompiledPolicy
	}
	data, err := json.Marshal(struct {
		Policy   string   `json:"policy"`
		Snapshot Snapshot `json:"snapshot"`
	}{Policy: policy, Snapshot: snapshot})
	if err != nil {
		return "", err
	}
	canonical, err := jsonschema.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return "", err
	}
	data, err = json.Marshal(canonical)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func (snapshot Snapshot) verify() error {
	if err := snapshot.validate(); err != nil {
		return err
	}
	digest, err := snapshot.digest()
	if err != nil {
		return err
	}
	if snapshot.Agent.Digest != digest {
		return errors.New("snapshot digest mismatch")
	}
	return nil
}

func SaveSnapshot(root string, snapshot Snapshot) error {
	if err := snapshot.verify(); err != nil {
		return err
	}
	directory := filepath.Join(root, "snapshots")
	existingDirectory := directory
	for ancestor := directory; ; ancestor = filepath.Dir(ancestor) {
		if _, err := os.Lstat(ancestor); errors.Is(err, fs.ErrNotExist) {
			continue
		} else if err != nil {
			return err
		}
		if err := noSymlinks(ancestor); err != nil {
			return err
		}
		existingDirectory = ancestor
		break
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	if err := noSymlinks(directory); err != nil {
		return err
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".snapshot-*")
	if err != nil {
		return err
	}
	defer os.Remove(temporary.Name())
	defer temporary.Close()
	if _, err := temporary.Write(data); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	path := filepath.Join(directory, snapshot.Agent.Digest+".json")
	if err := os.Link(temporary.Name(), path); err != nil {
		if !errors.Is(err, fs.ErrExist) {
			return err
		}
		existing, err := ReadSnapshot(root, snapshot.Agent.Digest)
		if err != nil {
			return err
		}
		if existing.Agent.Digest != snapshot.Agent.Digest {
			return errors.New("immutable snapshot conflict")
		}
	}
	for current := directory; ; current = filepath.Dir(current) {
		folder, err := os.Open(current)
		if err != nil {
			return err
		}
		err = folder.Sync()
		closeErr := folder.Close()
		if err != nil || closeErr != nil {
			return errors.Join(err, closeErr)
		}
		if current == existingDirectory {
			return nil
		}
	}
}

func ReadSnapshot(root, digest string) (Snapshot, error) {
	if len(digest) != sha256.Size*2 {
		return Snapshot{}, errors.New("invalid snapshot digest")
	}
	if _, err := hex.DecodeString(digest); err != nil || strings.ToLower(digest) != digest {
		return Snapshot{}, errors.New("invalid snapshot digest")
	}
	data, err := readRegular(filepath.Join(root, "snapshots", digest+".json"))
	if err != nil {
		return Snapshot{}, err
	}
	var snapshot Snapshot
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&snapshot); err != nil {
		return Snapshot{}, err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return Snapshot{}, errors.New("expected exactly one snapshot JSON document")
	}
	if snapshot.Agent.Digest != digest {
		return Snapshot{}, errors.New("stored snapshot digest differs from requested digest")
	}
	if err := snapshot.verify(); err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}
