package definitions

import "encoding/json"

type AgentDefaults struct {
	Model     Model     `yaml:"model" json:"model"`
	Execution Execution `yaml:"execution" json:"execution"`
}

func BuiltinNames() []string {
	return []string{"code-review", "verify", "adversarial-review"}
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
		return Agent{}, false
	}
	instructions += "\n\nFollow the supplied task constraints. Use only tools actually available to you; do not claim access to unavailable tools or invent tool results. Return valid JSON suitable for the supplied task without assuming fixed result keys."
	return Agent{Name: name, Version: 1, Description: description, Instructions: instructions, Schema: json.RawMessage(`{}`), Skills: map[string]string{}}, true
}
