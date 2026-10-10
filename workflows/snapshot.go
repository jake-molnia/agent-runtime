package workflows

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"github.com/jake-molnia/agent-runtime/definitions"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const CompilerPolicy = "workflow-v1:whole-json:scalar-or-ordered-array:all-steps-contribute:max32:input1MiB"
const recurringPolicy = "workflow-v2:whole-json:scheduled-input:single-step-notebook:v1"

type Snapshot struct {
	Workflow Workflow                        `json:"workflow"`
	Agents   map[string]definitions.Snapshot `json:"agents"`
	Digest   string                          `json:"digest"`
}

func Capture(workflow Workflow, catalog *definitions.Catalog) (Snapshot, error) {
	workflow, err := workflow.expandPreset()
	if err != nil {
		return Snapshot{}, err
	}
	if workflow.Version == 0 {
		workflow.Version = 1
	}
	order, err := workflow.order()
	if err != nil {
		return Snapshot{}, err
	}
	if catalog == nil {
		return Snapshot{}, errors.New("agent catalog required")
	}
	snapshot := Snapshot{Workflow: workflow, Agents: make(map[string]definitions.Snapshot, len(order))}
	for _, name := range order {
		agent, err := catalog.Snapshot(workflow.Steps[name].Agent)
		if err != nil {
			return Snapshot{}, fmt.Errorf("step %s: %w", name, err)
		}
		snapshot.Agents[name] = agent
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return Snapshot{}, err
	}
	var isolated Snapshot
	if err := json.Unmarshal(data, &isolated); err != nil {
		return Snapshot{}, err
	}
	snapshot = isolated
	if err := snapshot.validate(); err != nil {
		return Snapshot{}, err
	}
	snapshot.Digest, err = snapshot.digest()
	return snapshot, err
}

func (snapshot Snapshot) Order() ([]string, error) { return snapshot.Workflow.order() }

func (snapshot Snapshot) validate() error {
	order, err := snapshot.Order()
	if err != nil {
		return err
	}
	if len(snapshot.Agents) != len(order) {
		return errors.New("resolved agent set differs from workflow steps")
	}
	for _, name := range order {
		agent, exists := snapshot.Agents[name]
		if !exists || agent.Agent.Name != snapshot.Workflow.Steps[name].Agent {
			return fmt.Errorf("step %s: resolved agent mismatch", name)
		}
		data, err := json.Marshal(agent)
		if err != nil {
			return err
		}
		var isolated definitions.Snapshot
		if err := json.Unmarshal(data, &isolated); err != nil {
			return err
		}
		agent = isolated
		if _, err := agent.Definition(); err != nil {
			return fmt.Errorf("step %s: %w", name, err)
		}
		if len(agent.Agent.Schema) == 0 {
			return fmt.Errorf("step %s: agent requires output schema", name)
		}
		if snapshot.Workflow.Notebook {
			if err := validateNotebookContract(agent.Agent.Schema); err != nil {
				return fmt.Errorf("step %s: %w", name, err)
			}
		}
	}
	return nil
}

func validateNotebookContract(raw json.RawMessage) error {
	invalid := errors.New("notebook output contract requires a direct object schema with required status and notebook, a string notebook, and a nonempty status enum containing only completed, no_change, or blocked; references and schema composition are unsupported")
	var schema struct {
		Type       string                     `json:"type"`
		Required   []string                   `json:"required"`
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if json.Unmarshal(raw, &schema) != nil || schema.Type != "object" || !slices.Contains(schema.Required, "status") || !slices.Contains(schema.Required, "notebook") {
		return invalid
	}
	var notebook struct {
		Type string `json:"type"`
	}
	var status struct {
		Enum []string `json:"enum"`
	}
	if json.Unmarshal(schema.Properties["notebook"], &notebook) != nil || notebook.Type != "string" || json.Unmarshal(schema.Properties["status"], &status) != nil || len(status.Enum) == 0 {
		return invalid
	}
	for _, value := range status.Enum {
		if value != "completed" && value != "no_change" && value != "blocked" {
			return invalid
		}
	}
	for _, node := range []json.RawMessage{raw, schema.Properties["status"], schema.Properties["notebook"]} {
		var fields map[string]json.RawMessage
		if json.Unmarshal(node, &fields) != nil {
			return invalid
		}
		for _, keyword := range []string{"$ref", "$dynamicRef", "$recursiveRef", "allOf", "anyOf", "oneOf"} {
			if _, exists := fields[keyword]; exists {
				return invalid
			}
		}
	}
	return nil
}

func (snapshot Snapshot) digest() (string, error) {
	snapshot.Digest = ""
	policy := CompilerPolicy
	if snapshot.Workflow.Schedule != nil || snapshot.Workflow.DefaultInput != nil || snapshot.Workflow.Notebook {
		policy = recurringPolicy
	}
	data, err := json.Marshal(struct {
		Policy   string   `json:"policy"`
		Snapshot Snapshot `json:"snapshot"`
	}{policy, snapshot})
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

func (snapshot Snapshot) Verify() error {
	if err := snapshot.validate(); err != nil {
		return err
	}
	digest, err := snapshot.digest()
	if err != nil {
		return err
	}
	if !validDigest(snapshot.Digest) || snapshot.Digest != digest {
		return errors.New("workflow snapshot digest mismatch")
	}
	return nil
}

func validDigest(digest string) bool {
	if len(digest) != sha256.Size*2 {
		return false
	}
	for _, character := range digest {
		if !(character >= '0' && character <= '9' || character >= 'a' && character <= 'f') {
			return false
		}
	}
	return true
}
