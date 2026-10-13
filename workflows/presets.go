package workflows

import (
	"embed"
	"errors"
	"fmt"
	"slices"

	"gopkg.in/yaml.v3"
)

//go:embed presets/*.yaml
var presetFiles embed.FS

func PresetNames() []string { return []string{"pr-review"} }

func (workflow Workflow) expandPreset() (Workflow, error) {
	if workflow.Use == "" {
		return workflow, nil
	}
	if !slices.Contains(PresetNames(), workflow.Use) {
		return Workflow{}, fmt.Errorf("unknown workflow preset: %s", workflow.Use)
	}
	if workflow.Steps != nil || workflow.Output != "" {
		return Workflow{}, errors.New("use cannot be combined with authored steps or output")
	}
	data, err := presetFiles.ReadFile("presets/" + workflow.Use + ".yaml")
	if err != nil {
		return Workflow{}, err
	}
	var preset Workflow
	if err := yaml.Unmarshal(data, &preset); err != nil {
		return Workflow{}, err
	}
	workflow.Steps, workflow.Output = preset.Steps, preset.Output
	// Snapshots contain the resolved graph, never a lookup against a future binary.
	workflow.Use = ""
	return workflow, nil
}
