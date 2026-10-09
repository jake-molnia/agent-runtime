package hatchetbridge

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
	"github.com/jake-molnia/agent-runtime/agentexec"
	"github.com/jake-molnia/agent-runtime/messages"
	"github.com/jake-molnia/agent-runtime/workflows"
)

type ConfiguredInput struct {
	Digest string          `json:"digest"`
	Input  json.RawMessage `json:"input"`
}

type ConfiguredState struct {
	Digest string             `json:"digest"`
	Input  messages.Reference `json:"input"`
}

type ConfiguredResult struct {
	Digest  string             `json:"digest"`
	Message messages.Reference `json:"message"`
	Value   json.RawMessage    `json:"value,omitempty"`
}

func configuredService(plan workflows.Snapshot, executor *agentexec.Executor, store messages.Store) (*messages.Service, error) {
	validators := map[string]messages.Validator{"workflow-input": func(data json.RawMessage) error {
		if !json.Valid(data) {
			return errors.New("workflow input is not valid JSON")
		}
		return nil
	}}
	stages := []messages.Stage{}
	seen := map[messages.Actor]bool{}
	for _, snapshot := range plan.Agents {
		actor := messages.Actor{Agent: snapshot.Agent.Name, Revision: snapshot.Agent.Digest}
		if seen[actor] {
			continue
		}
		stage, validator, err := executor.Stage(snapshot, []messages.Contract{{Name: "input", Kind: messages.Data, Schema: "workflow-input", Required: true}}, "output")
		if err != nil {
			return nil, err
		}
		seen[actor] = true
		validators[snapshot.Agent.Digest] = validator
		stages = append(stages, stage)
	}
	return messages.New(store, validators, stages, 0)
}

func RegisterConfiguredWorkflow(client *hatchet.Client, backend agentexec.Backend, store messages.Store, root string, current workflows.Snapshot) (*hatchet.Workflow, error) {
	if client == nil || store == nil || root == "" {
		return nil, errors.New("configured workflow dependencies required")
	}
	if err := current.Verify(); err != nil {
		return nil, err
	}
	executor, err := agentexec.New(backend)
	if err != nil {
		return nil, err
	}
	if _, err := configuredService(current, executor, store); err != nil {
		return nil, err
	}
	order, err := current.Order()
	if err != nil {
		return nil, err
	}
	name := current.Workflow.Name
	load := func(digest string) (workflows.Snapshot, error) {
		plan, err := workflows.Read(root, digest)
		if err != nil {
			return plan, err
		}
		if plan.Workflow.Name != name {
			return plan, errors.New("workflow snapshot name mismatch")
		}
		return plan, nil
	}
	workflow := client.NewWorkflow(name, hatchet.WithWorkflowVersion(current.Digest))
	resolve := workflow.NewTask("resolve", func(ctx hatchet.Context, input ConfiguredInput) (ConfiguredState, error) {
		plan, err := load(input.Digest)
		if err != nil {
			return ConfiguredState{}, err
		}
		if !json.Valid(input.Input) || len(input.Input) > 1<<20 {
			return ConfiguredState{}, errors.New("bounded JSON workflow input required")
		}
		service, err := configuredService(plan, executor, store)
		if err != nil {
			return ConfiguredState{}, err
		}
		ref, err := service.Publish(ctx.GetContext(), ctx.WorkflowRunId(), messages.Message{Version: messages.Version, ID: "workflow-input", ContextID: ctx.WorkflowRunId(), TaskID: "resolve", From: messages.Actor{Agent: "caller", Revision: "v1"}, To: name, Parts: []messages.Part{{Name: "input", Kind: messages.Data, Schema: "workflow-input", Data: input.Input}}})
		return ConfiguredState{Digest: input.Digest, Input: ref}, err
	}, hatchet.WithRetries(0), hatchet.WithExecutionTimeout(time.Minute))
	tasks := map[string]*hatchet.Task{}
	for _, id := range order {
		step := current.Workflow.Steps[id]
		parents := []*hatchet.Task{resolve}
		seen := map[string]bool{}
		for _, source := range step.Input.Sources {
			if source != "input" && !seen[source] {
				parents = append(parents, tasks[source])
				seen[source] = true
			}
		}
		tasks[id] = workflow.NewDurableTask(id, func(ctx hatchet.DurableContext, input ConfiguredInput) (ConfiguredResult, error) {
			var state ConfiguredState
			if err := ctx.StepOutput("resolve", &state); err != nil {
				return ConfiguredResult{}, err
			}
			plan, err := load(state.Digest)
			if err != nil {
				return ConfiguredResult{}, err
			}
			step, exists := plan.Workflow.Steps[id]
			if !exists {
				return ConfiguredResult{}, errors.New("pinned step is unavailable")
			}
			service, err := configuredService(plan, executor, store)
			if err != nil {
				return ConfiguredResult{}, err
			}
			initial, err := service.Receive(ctx.GetContext(), ctx.WorkflowRunId(), state.Input, name)
			if err != nil {
				return ConfiguredResult{}, err
			}
			initialValue, err := MessageData(initial, "input")
			if err != nil {
				return ConfiguredResult{}, err
			}
			values := map[string]json.RawMessage{}
			for _, source := range step.Input.Sources {
				if source == "input" {
					continue
				}
				var result ConfiguredResult
				if err := ctx.StepOutput(source, &result); err != nil {
					return ConfiguredResult{}, err
				}
				if result.Digest != state.Digest {
					return ConfiguredResult{}, errors.New("parent workflow identity mismatch")
				}
				values[source], err = configuredOutput(ctx.GetContext(), service, ctx.WorkflowRunId(), name, state, plan, source, result.Message)
				if err != nil {
					return ConfiguredResult{}, err
				}
			}
			value, err := workflows.ResolveInput(step, initialValue, values)
			if err != nil {
				return ConfiguredResult{}, err
			}
			snapshot := plan.Agents[id]
			ref, err := service.Publish(ctx.GetContext(), ctx.WorkflowRunId(), messages.Message{Version: messages.Version, ID: "input-" + id, ContextID: ctx.WorkflowRunId(), TaskID: id, From: messages.Actor{Agent: "workflow", Revision: state.Digest}, To: snapshot.Agent.Name, InReplyTo: &state.Input, Parts: []messages.Part{{Name: "input", Kind: messages.Data, Schema: "workflow-input", Data: value}}})
			if err != nil {
				return ConfiguredResult{}, err
			}
			bounded := agentexec.WithInteractionWait(ctx.GetContext(), func(waitCtx context.Context, sessionID, kind string) error {
				return interactionWait(ctx, sessionID)(waitCtx, kind)
			})
			ref, err = service.Invoke(bounded, ctx.WorkflowRunId(), messages.Invocation{Stage: messages.Actor{Agent: snapshot.Agent.Name, Revision: snapshot.Agent.Digest}, Input: ref, TaskID: id, To: name})
			return ConfiguredResult{Digest: state.Digest, Message: ref}, err
		}, hatchet.WithParents(parents...), hatchet.WithRetries(0), hatchet.WithExecutionTimeout(24*time.Hour), hatchet.WithScheduleTimeout(time.Hour))
	}
	workflow.NewTask("result", func(ctx hatchet.Context, input ConfiguredInput) (ConfiguredResult, error) {
		var state ConfiguredState
		if err := ctx.StepOutput("resolve", &state); err != nil {
			return ConfiguredResult{}, err
		}
		plan, err := load(state.Digest)
		if err != nil {
			return ConfiguredResult{}, err
		}
		if plan.Workflow.Output != current.Workflow.Output {
			return ConfiguredResult{}, errors.New("pinned output step changed; compatible worker required")
		}
		var result ConfiguredResult
		if err := ctx.StepOutput(plan.Workflow.Output, &result); err != nil {
			return result, err
		}
		if result.Digest != state.Digest {
			return ConfiguredResult{}, errors.New("result workflow identity mismatch")
		}
		service, err := configuredService(plan, executor, store)
		if err != nil {
			return result, err
		}
		result.Value, err = configuredOutput(ctx.GetContext(), service, ctx.WorkflowRunId(), name, state, plan, plan.Workflow.Output, result.Message)
		return result, err
	}, hatchet.WithParents(resolve, tasks[current.Workflow.Output]), hatchet.WithRetries(0), hatchet.WithExecutionTimeout(time.Minute))
	return workflow, nil
}

func configuredOutput(ctx context.Context, service *messages.Service, scope, name string, state ConfiguredState, plan workflows.Snapshot, id string, ref messages.Reference) (json.RawMessage, error) {
	delivery, err := service.Receive(ctx, scope, ref, name)
	if err != nil {
		return nil, err
	}
	snapshot, exists := plan.Agents[id]
	if !exists {
		return nil, errors.New("output step is unavailable")
	}
	actor := messages.Actor{Agent: snapshot.Agent.Name, Revision: snapshot.Agent.Digest}
	if delivery.Message.From != actor || delivery.Message.TaskID != id || delivery.Message.ContextID != scope || delivery.Message.InReplyTo == nil {
		return nil, errors.New("step output identity mismatch")
	}
	input, err := service.Receive(ctx, scope, *delivery.Message.InReplyTo, actor.Agent)
	if err != nil {
		return nil, err
	}
	if input.Message.ID != "input-"+id || input.Message.TaskID != id || input.Message.ContextID != scope || input.Message.From != (messages.Actor{Agent: "workflow", Revision: state.Digest}) || input.Message.InReplyTo == nil || *input.Message.InReplyTo != state.Input {
		return nil, errors.New("step output reply identity mismatch")
	}
	return MessageData(delivery, "output")
}
