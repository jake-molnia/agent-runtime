package workflows

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	_ "time/tzdata"

	"github.com/robfig/cron/v3"
	"gopkg.in/yaml.v3"
)

type Schedule struct {
	Cron     string `json:"cron" yaml:"cron"`
	Timezone string `json:"timezone,omitempty" yaml:"timezone,omitempty"`
}

func (schedule Schedule) Expression() (string, error) {
	fields := strings.Fields(schedule.Cron)
	if len(fields) != 5 || strings.Contains(schedule.Cron, "TZ=") {
		return "", errors.New("schedule requires a five-field cron expression without an embedded timezone")
	}
	zone := schedule.Timezone
	if zone == "" {
		zone = "UTC"
	}
	if strings.ContainsAny(zone, " \t\r\n=") || zone == "Local" {
		return "", errors.New("schedule requires an IANA timezone")
	}
	if _, err := time.LoadLocation(zone); err != nil {
		return "", fmt.Errorf("invalid schedule timezone: %w", err)
	}
	expression := "CRON_TZ=" + zone + " " + strings.Join(fields, " ")
	if _, err := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow).Parse(expression); err != nil {
		return "", fmt.Errorf("invalid schedule cron: %w", err)
	}
	return expression, nil
}

func (workflow *Workflow) UnmarshalYAML(node *yaml.Node) error {
	var wire struct {
		Use      string          `yaml:"use"`
		Version  int             `yaml:"version"`
		Steps    map[string]Step `yaml:"steps"`
		Output   string          `yaml:"output"`
		Schedule *Schedule       `yaml:"schedule"`
		Input    yaml.Node       `yaml:"input"`
		Notebook bool            `yaml:"notebook"`
	}
	wire.Version = 1
	data, err := yaml.Marshal(node)
	if err != nil {
		return err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if err := decoder.Decode(&wire); err != nil {
		return err
	}
	var input json.RawMessage
	if wire.Input.Kind != 0 {
		var value any
		if err := wire.Input.Decode(&value); err != nil {
			return fmt.Errorf("invalid default input: %w", err)
		}
		input, err = json.Marshal(value)
		if err != nil {
			return fmt.Errorf("default input must be JSON compatible: %w", err)
		}
	}
	*workflow = Workflow{Use: wire.Use, Name: workflow.Name, Version: wire.Version, Steps: wire.Steps, Output: wire.Output, Schedule: wire.Schedule, DefaultInput: input, Notebook: wire.Notebook}
	return nil
}

func (workflow Workflow) InvocationInput(override json.RawMessage) (json.RawMessage, error) {
	input := override
	if input == nil {
		input = workflow.DefaultInput
	}
	if len(input) == 0 || len(input) > MaxInputBytes || !json.Valid(input) {
		return nil, errors.New("bounded JSON input required; supply --input or configure workflow input")
	}
	return append(json.RawMessage(nil), input...), nil
}

func (workflow Workflow) validateRecurring() error {
	if workflow.DefaultInput != nil {
		if _, err := workflow.InvocationInput(nil); err != nil {
			return err
		}
	}
	if workflow.Schedule != nil {
		if _, err := workflow.Schedule.Expression(); err != nil {
			return err
		}
		if workflow.DefaultInput == nil {
			return errors.New("scheduled workflow requires a default input")
		}
	}
	if workflow.Notebook && len(workflow.Steps) != 1 {
		return errors.New("notebook workflows require exactly one step")
	}
	return nil
}

func (workflow Workflow) MarshalYAML() (any, error) {
	var input *yaml.Node
	if workflow.DefaultInput != nil {
		// Preserve JSON numbers without converting them through float64.
		var node yaml.Node
		if err := yaml.Unmarshal(workflow.DefaultInput, &node); err != nil {
			return nil, err
		}
		if len(node.Content) != 1 {
			return nil, errors.New("default input must contain one value")
		}
		input = node.Content[0]
	}
	return struct {
		Use      string          `yaml:"use,omitempty"`
		Version  int             `yaml:"version,omitempty"`
		Steps    map[string]Step `yaml:"steps,omitempty"`
		Output   string          `yaml:"output,omitempty"`
		Schedule *Schedule       `yaml:"schedule,omitempty"`
		Input    *yaml.Node      `yaml:"input,omitempty"`
		Notebook bool            `yaml:"notebook,omitempty"`
	}{workflow.Use, workflow.Version, workflow.Steps, workflow.Output, workflow.Schedule, input, workflow.Notebook}, nil
}
