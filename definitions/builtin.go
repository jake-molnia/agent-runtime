package definitions

import (
	"embed"
	"encoding/json"
)

//go:embed builtins/*.md builtins/result.schema.json
var builtinAssets embed.FS

var taskPresets = []struct {
	name        string
	description string
}{
	{"ci-failure-analyst", "Investigate CI failures and identify evidence-backed next steps."},
	{"findings-triager", "Reconcile, prioritize, and verify review findings."},
	{"security-reviewer", "Review source and configuration for exploitable security weaknesses."},
	{"app-pentester", "Test an explicitly authorized application within its configured scope."},
	{"incident-analyst", "Reconstruct incidents and investigate competing explanations."},
	{"dependency-upgrader", "Update dependencies, adapt source, and validate the resulting change."},
	{"upstreamer", "Integrate upstream changes while preserving a fork's customizations."},
	{"researcher", "Explore a question and produce a sourced, decision-useful answer."},
	{"monitor", "Explore a subject regularly and report meaningful developments."},
	{"price-watcher", "Discover and investigate worthwhile buying opportunities."},
	{"news-researcher", "Discover current stories and research their evidence and context."},
	{"daily-brief", "Research and write a concise briefing or accessible explainer."},
	{"opportunity-scout", "Discover relevant opportunities and verify their fit and requirements."},
}

type AgentDefaults struct {
	Model     Model     `yaml:"model" json:"model"`
	Execution Execution `yaml:"execution" json:"execution"`
}

func BuiltinNames() []string {
	names := []string{"code-review", "verify", "adversarial-review"}
	for _, preset := range taskPresets {
		names = append(names, preset.name)
	}
	return names
}

func Builtin(name string) (Agent, bool) {
	var description, instructions string
	switch name {
	case "code-review":
		description = "Review supplied changes and supporting context."
		instructions = "# Code review\n\nReview the arbitrary input supplied with this task. Identify actionable correctness, safety, and maintainability concerns relevant to the requested scope. Ground conclusions in supplied evidence or evidence you can obtain with available approved tools. Distinguish observed facts from assumptions and report verification limits."
	case "verify":
		description = "Verify supplied claims against available evidence."
		instructions = "# Verification\n\nVerify the arbitrary input supplied with this task against its stated requirements and available evidence. Use available approved tools when they help establish the result. Explain what was checked, what the evidence supports, and what remains uncertain. Do not report an unperformed check as successful."
	case "adversarial-review":
		description = "Challenge supplied work and its assumptions."
		instructions = "# Adversarial review\n\nChallenge the arbitrary input supplied with this task. Look for unsupported assumptions, counterexamples, failure modes, and missing evidence within the requested scope. Test concerns against supplied evidence or evidence obtainable with available approved tools. Separate demonstrated problems from speculative risks."
	default:
		for _, preset := range taskPresets {
			if preset.name == name {
				return Agent{
					Name: name, Version: 1, Description: preset.description,
					Instructions: builtinAsset(name+".md") + "\n\n" + builtinAsset("common.md"),
					Schema:       json.RawMessage(builtinAsset("result.schema.json")),
					Skills:       map[string]string{},
				}, true
			}
		}
		return Agent{}, false
	}
	instructions += "\n\nFollow the supplied task constraints. Use only tools actually available to you; do not claim access to unavailable tools or invent tool results. Return valid JSON suitable for the supplied task without assuming fixed result keys."
	return Agent{Name: name, Version: 1, Description: description, Instructions: instructions, Schema: json.RawMessage(`{}`), Skills: map[string]string{}}, true
}

func builtinAsset(name string) string {
	data, err := builtinAssets.ReadFile("builtins/" + name)
	if err != nil {
		panic("missing embedded builtin asset: " + name)
	}
	return string(data)
}
