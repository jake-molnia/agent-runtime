package workflows

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/jake-molnia/agent-runtime/definitions"
	"gopkg.in/yaml.v3"
)

const MaxSteps = 32
const MaxInputBytes = 4 << 20

var validName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,62}$`)

type Workflow struct {
	Name    string          `json:"name" yaml:"-"`
	Version int             `json:"version" yaml:"version,omitempty"`
	Steps   map[string]Step `json:"steps" yaml:"steps"`
	Output  string          `json:"output" yaml:"output"`
}

type Step struct {
	Agent string    `json:"agent" yaml:"agent"`
	Input InputRefs `json:"input" yaml:"input"`
}

type InputRefs struct {
	Sources  []string `json:"sources"`
	Multiple bool     `json:"multiple"`
}

func (refs *InputRefs) UnmarshalYAML(node *yaml.Node) error {
	var result InputRefs
	switch node.Kind {
	case yaml.ScalarNode:
		if node.Tag != "!!str" {
			return errors.New("input must be a string or nonempty string sequence")
		}
		result.Sources = []string{node.Value}
	case yaml.SequenceNode:
		result.Multiple = true
		for _, source := range node.Content {
			if source.Kind != yaml.ScalarNode || source.Tag != "!!str" {
				return errors.New("input sequence must contain strings")
			}
			result.Sources = append(result.Sources, source.Value)
		}
	default:
		return errors.New("input must be a string or nonempty string sequence")
	}
	if err := result.validate(); err != nil {
		return err
	}
	*refs = result
	return nil
}

func (refs InputRefs) MarshalYAML() (any, error) {
	if err := refs.validate(); err != nil {
		return nil, err
	}
	if refs.Multiple {
		return refs.Sources, nil
	}
	return refs.Sources[0], nil
}

func (refs InputRefs) validate() error {
	if len(refs.Sources) == 0 || (!refs.Multiple && len(refs.Sources) != 1) {
		return errors.New("input requires a scalar reference or nonempty reference list")
	}
	for _, source := range refs.Sources {
		if !validName.MatchString(source) {
			return fmt.Errorf("invalid input reference: %q", source)
		}
	}
	return nil
}

func (workflow Workflow) order() ([]string, error) {
	if !validName.MatchString(workflow.Name) {
		return nil, errors.New("invalid workflow name")
	}
	if workflow.Version != 1 {
		return nil, fmt.Errorf("unsupported workflow version: %d", workflow.Version)
	}
	if len(workflow.Steps) == 0 || len(workflow.Steps) > MaxSteps {
		return nil, fmt.Errorf("workflow requires 1 to %d steps", MaxSteps)
	}
	if _, exists := workflow.Steps[workflow.Output]; !exists {
		return nil, fmt.Errorf("unknown output step: %q", workflow.Output)
	}
	names := make([]string, 0, len(workflow.Steps))
	for name, step := range workflow.Steps {
		if !validName.MatchString(name) || name == "input" || name == "resolve" || name == "result" {
			return nil, fmt.Errorf("invalid or reserved step name: %q", name)
		}
		if !validName.MatchString(step.Agent) {
			return nil, fmt.Errorf("step %s: invalid agent name", name)
		}
		if err := step.Input.validate(); err != nil {
			return nil, fmt.Errorf("step %s: %w", name, err)
		}
		for _, source := range step.Input.Sources {
			if source != "input" {
				if _, exists := workflow.Steps[source]; !exists {
					return nil, fmt.Errorf("step %s: unknown input %q", name, source)
				}
			}
		}
		names = append(names, name)
	}
	sort.Strings(names)
	states := make(map[string]int, len(names))
	order := make([]string, 0, len(names))
	var visit func(string) error
	visit = func(name string) error {
		if states[name] == 1 {
			return fmt.Errorf("workflow cycle at step %s", name)
		}
		if states[name] == 2 {
			return nil
		}
		states[name] = 1
		dependencies := append([]string(nil), workflow.Steps[name].Input.Sources...)
		sort.Strings(dependencies)
		for _, source := range dependencies {
			if source != "input" {
				if err := visit(source); err != nil {
					return err
				}
			}
		}
		states[name] = 2
		order = append(order, name)
		return nil
	}
	if err := visit(workflow.Output); err != nil {
		return nil, err
	}
	if len(order) != len(names) {
		for _, name := range names {
			if states[name] != 2 {
				return nil, fmt.Errorf("step %s does not contribute to output %s", name, workflow.Output)
			}
		}
	}
	return order, nil
}

func Load(root string, catalog *definitions.Catalog) (map[string]Snapshot, error) {
	directory, err := safeDirectory(root, false)
	if err != nil {
		return nil, err
	}
	snapshots := map[string]Snapshot{}
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return snapshots, nil
	}
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		path := filepath.Join(directory, entry.Name())
		if entry.Type()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("symlink forbidden: %s", path)
		}
		if entry.Type().IsRegular() && strings.HasPrefix(entry.Name(), ".snapshot-") {
			continue
		}
		if strings.HasSuffix(entry.Name(), ".json") && validDigest(strings.TrimSuffix(entry.Name(), ".json")) {
			if _, err := Read(root, strings.TrimSuffix(entry.Name(), ".json")); err != nil {
				return nil, err
			}
			continue
		}
		name := strings.TrimSuffix(entry.Name(), ".yaml")
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".yaml") || !validName.MatchString(name) {
			return nil, fmt.Errorf("invalid workflow file: %s", entry.Name())
		}
		data, err := readRegular(path)
		if err != nil {
			return nil, err
		}
		workflow := Workflow{Version: 1}
		decoder := yaml.NewDecoder(bytes.NewReader(data))
		decoder.KnownFields(true)
		if err := decoder.Decode(&workflow); err != nil {
			return nil, fmt.Errorf("workflow %s: %w", name, err)
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			return nil, errors.New("expected exactly one workflow YAML document")
		}
		if workflow.Version != 1 {
			return nil, fmt.Errorf("workflow %s: unsupported version: %d", name, workflow.Version)
		}
		workflow.Name = name
		snapshot, err := Capture(workflow, catalog)
		if err != nil {
			return nil, fmt.Errorf("workflow %s: %w", name, err)
		}
		snapshots[name] = snapshot
	}
	return snapshots, nil
}

func ResolveInput(step Step, initial json.RawMessage, parents map[string]json.RawMessage) (json.RawMessage, error) {
	if err := step.Input.validate(); err != nil {
		return nil, err
	}
	values := make([]json.RawMessage, 0, len(step.Input.Sources))
	for _, source := range step.Input.Sources {
		value := initial
		if source != "input" {
			var exists bool
			value, exists = parents[source]
			if !exists {
				return nil, fmt.Errorf("missing input reference: %s", source)
			}
		}
		if len(value) > MaxInputBytes || !json.Valid(value) {
			return nil, fmt.Errorf("input %s must be valid JSON within %d bytes", source, MaxInputBytes)
		}
		values = append(values, append(json.RawMessage(nil), value...))
	}
	if !step.Input.Multiple {
		return values[0], nil
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(values); err != nil {
		return nil, err
	}
	data := bytes.TrimSuffix(buffer.Bytes(), []byte("\n"))
	if len(data) > MaxInputBytes {
		return nil, fmt.Errorf("resolved input exceeds %d bytes", MaxInputBytes)
	}
	return data, nil
}
