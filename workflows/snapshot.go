package workflows

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jake-molnia/agent-runtime/definitions"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const CompilerPolicy = "workflow-v1:whole-json:scalar-or-ordered-array:all-steps-contribute:max32:input1MiB"

type Snapshot struct {
	Workflow Workflow                        `json:"workflow"`
	Agents   map[string]definitions.Snapshot `json:"agents"`
	Digest   string                          `json:"digest"`
}

func Capture(workflow Workflow, catalog *definitions.Catalog) (Snapshot, error) {
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
	}
	return nil
}

func (snapshot Snapshot) digest() (string, error) {
	snapshot.Digest = ""
	data, err := json.Marshal(struct {
		Policy   string   `json:"policy"`
		Snapshot Snapshot `json:"snapshot"`
	}{CompilerPolicy, snapshot})
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
