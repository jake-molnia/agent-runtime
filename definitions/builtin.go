package definitions

import (
	"embed"
	"encoding/json"
	"strings"
)

//go:embed builtins/*/instructions.md builtins/common.md schemas/*.json
var builtinAssets embed.FS

var builtinPresets = []struct{ name, description, schema string }{
	{"code-review", "Review supplied changes and supporting context.", ""},
	{"verify", "Verify supplied claims against available evidence.", ""},
	{"adversarial-review", "Challenge supplied work and its assumptions.", ""},
	{"ci-failure-analyst", "Investigate CI failures and identify evidence-backed next steps.", "result.schema.json"},
	{"findings-triager", "Reconcile, prioritize, and verify review findings.", "result.schema.json"},
	{"security-reviewer", "Review source and configuration for exploitable security weaknesses.", "result.schema.json"},
	{"app-pentester", "Test an explicitly authorized application within its configured scope.", "result.schema.json"},
	{"incident-analyst", "Reconstruct incidents and investigate competing explanations.", "result.schema.json"},
	{"dependency-upgrader", "Update dependencies, adapt source, and validate the resulting change.", "result.schema.json"},
	{"upstreamer", "Integrate upstream changes while preserving a fork's customizations.", "result.schema.json"},
	{"researcher", "Explore a question and produce a sourced, decision-useful answer.", "result.schema.json"},
	{"monitor", "Explore a subject regularly and report meaningful developments.", "result.schema.json"},
	{"price-watcher", "Discover and investigate worthwhile buying opportunities.", "result.schema.json"},
	{"news-researcher", "Discover current stories and research their evidence and context.", "result.schema.json"},
	{"daily-brief", "Research and write a concise briefing or accessible explainer.", "result.schema.json"},
	{"opportunity-scout", "Discover relevant opportunities and verify their fit and requirements.", "result.schema.json"},
	{"pr-reviewer", "Investigate a pinned change for actionable regressions and design defects.", "review-report.schema.json"},
	{"pr-adversarial", "Independently challenge a pinned change and its assumptions.", "review-report.schema.json"},
	{"pr-security", "Investigate security weaknesses introduced by a pinned change.", "review-report.schema.json"},
	{"pr-dependencies", "Check dependency and platform compatibility of a pinned change.", "review-report.schema.json"},
	{"pr-verifier", "Verify candidates from all PR investigation roles.", "verified-review.schema.json"},
	{"pr-triager", "Group verified PR findings without losing source coverage.", "review-triage.schema.json"},
	{"pr-writer", "Write a PR assessment and comments from verified findings.", "review-writeup.schema.json"},
}

type AgentDefaults struct {
	Model     Model     `yaml:"model" json:"model"`
	Execution Execution `yaml:"execution" json:"execution"`
}

func BuiltinNames() []string {
	names := make([]string, 0, len(builtinPresets))
	for _, preset := range builtinPresets {
		names = append(names, preset.name)
	}
	return names
}

func Builtin(name string) (Agent, bool) {
	for _, preset := range builtinPresets {
		if preset.name != name {
			continue
		}
		instructions := builtinAsset("builtins/" + name + "/instructions.md")
		schema := json.RawMessage(`{}`)
		if preset.schema == "" {
			instructions = strings.TrimSuffix(instructions, "\n")
		} else {
			schema = json.RawMessage(builtinAsset("schemas/" + preset.schema))
		}
		if preset.schema == "result.schema.json" {
			instructions += "\n\n" + builtinAsset("builtins/common.md")
		}
		return Agent{Name: name, Version: 1, Description: preset.description,
			Instructions: instructions, Schema: schema, Skills: map[string]string{}}, true
	}
	return Agent{}, false
}

func builtinAsset(path string) string {
	data, err := builtinAssets.ReadFile(path)
	if err != nil {
		panic("missing embedded builtin asset: " + path)
	}
	return string(data)
}
