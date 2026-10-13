package hatchetbridge

import (
	"context"
	"encoding/json"
)

// ConfiguredStep carries the durable caller input, independently of model outputs.
type ConfiguredStep struct {
	Workflow string
	Step     string
	RunID    string
	Initial  json.RawMessage
}

// ConfiguredHooks are trusted worker integrations, never loaded from agent YAML.
// A hook can reject a stage or decorate its context. Its cleanup runs after the
// invocation, including failed invocations; hooks compose in registration order.
type ConfiguredHooks struct {
	BeforeStep func(context.Context, ConfiguredStep) (context.Context, func(), error)
}
