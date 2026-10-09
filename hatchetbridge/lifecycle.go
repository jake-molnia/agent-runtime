package hatchetbridge

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
	"github.com/jake-molnia/agent-runtime/orchestration"
)

type Spec struct {
	Agent  string                `json:"agent"`
	Digest string                `json:"digest"`
	Run    orchestration.Request `json:"run"`
	Domain json.RawMessage       `json:"domain,omitempty"`
	Skip   bool                  `json:"skip"`
}

type Lifecycle struct {
	Name       string
	Automation bool
	Resolve    func(context.Context, Input, string) (Spec, error)
	Definition func(Spec) (orchestration.Definition, error)
	Validate   func(Spec, json.RawMessage) error
	Structured func(Spec) (bool, error)
	Publish    func(context.Context, Spec, json.RawMessage) (json.RawMessage, error)
}

func Build(client *hatchet.Client, engine *orchestration.Engine, lifecycle Lifecycle) (*hatchet.Workflow, error) {
	if client == nil || engine == nil || lifecycle.Name == "" || lifecycle.Definition == nil || lifecycle.Resolve == nil {
		return nil, errors.New("invalid lifecycle registration")
	}
	if lifecycle.Automation && (engine.Artifacts == nil || lifecycle.Publish == nil || lifecycle.Validate == nil) {
		return nil, errors.New("automations require artifact storage, validation and publishing")
	}
	if lifecycle.Publish != nil && (engine.Artifacts == nil || lifecycle.Validate == nil) {
		return nil, errors.New("publishing requires artifact storage and validation")
	}
	return registerLifecycle(client, engine, lifecycle), nil
}

func (l Lifecycle) resolve(ctx context.Context, input Input, runID string) (Spec, error) {
	if input.Run.SubmittedAt.After(time.Now().Add(time.Second)) {
		return Spec{}, errors.New("submission time is in the future")
	}
	if input.Run.SubmittedAt.IsZero() {
		input.Run.SubmittedAt = time.Now().UTC()
	}
	input.Run.Key = runID
	if l.Resolve != nil {
		return l.Resolve(ctx, input, runID)
	}
	return Spec{Agent: input.Agent, Digest: input.Digest, Run: input.Run}, nil
}

func SubmitInput(ctx context.Context, client *hatchet.Client, name string, input Input) (*hatchet.WorkflowRunRef, error) {
	input.Run.Key = ""
	input.Run.SubmittedAt = time.Now().UTC()
	return client.RunNoWait(ctx, name, input)
}
