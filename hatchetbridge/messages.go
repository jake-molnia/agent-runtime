package hatchetbridge

import (
	"errors"
	"time"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
	"github.com/jake-molnia/agent-runtime/messages"
)

type MessageChain struct {
	Name     string
	Producer messages.Actor
	Verifier messages.Actor
	Timeout  time.Duration
}

type MessageChainInput struct {
	Parts []messages.Part `json:"parts"`
}

type MessageChainState struct {
	Producer messages.Actor     `json:"producer"`
	Verifier messages.Actor     `json:"verifier"`
	Message  messages.Reference `json:"message"`
}

func RegisterMessageChain(client *hatchet.Client, service *messages.Service, chain MessageChain, options ...hatchet.WorkflowOption) (*hatchet.Workflow, error) {
	if client == nil || service == nil || chain.Name == "" || chain.Timeout <= 0 || chain.Timeout > 24*time.Hour {
		return nil, errors.New("invalid message chain configuration")
	}
	if err := service.CanRoute(chain.Producer, chain.Verifier); err != nil {
		return nil, err
	}
	workflow := client.NewWorkflow(chain.Name, options...)
	resolve := workflow.NewTask("resolve", messageResolve(service, chain), hatchet.WithRetries(2), hatchet.WithExecutionTimeout(time.Minute))
	produce := workflow.NewTask("produce", messageProduce(service), hatchet.WithParents(resolve), hatchet.WithRetries(2), hatchet.WithExecutionTimeout(chain.Timeout))
	workflow.NewTask("verify", messageVerify(service), hatchet.WithParents(produce), hatchet.WithRetries(2), hatchet.WithExecutionTimeout(chain.Timeout))
	return workflow, nil
}

func messageResolve(service *messages.Service, chain MessageChain) func(hatchet.Context, MessageChainInput) (MessageChainState, error) {
	return func(ctx hatchet.Context, input MessageChainInput) (MessageChainState, error) {
		ref, err := service.Publish(ctx.GetContext(), ctx.WorkflowRunId(), messages.Message{
			Version:   messages.Version,
			ID:        "request",
			ContextID: ctx.WorkflowRunId(),
			TaskID:    "resolve",
			From:      messages.Actor{Agent: "caller", Revision: "v1"},
			To:        chain.Producer.Agent,
			Parts:     input.Parts,
		})
		return MessageChainState{Producer: chain.Producer, Verifier: chain.Verifier, Message: ref}, err
	}
}

func messageProduce(service *messages.Service) func(hatchet.Context, MessageChainInput) (MessageChainState, error) {
	return func(ctx hatchet.Context, input MessageChainInput) (MessageChainState, error) {
		var state MessageChainState
		if err := ctx.StepOutput("resolve", &state); err != nil {
			return state, err
		}
		ref, err := service.Invoke(ctx.GetContext(), ctx.WorkflowRunId(), messages.Invocation{
			Stage: state.Producer, Input: state.Message, TaskID: "produce", To: state.Verifier.Agent,
		})
		state.Message = ref
		return state, err
	}
}

func messageVerify(service *messages.Service) func(hatchet.Context, MessageChainInput) (MessageChainState, error) {
	return func(ctx hatchet.Context, input MessageChainInput) (MessageChainState, error) {
		var state MessageChainState
		if err := ctx.StepOutput("produce", &state); err != nil {
			return state, err
		}
		ref, err := service.Invoke(ctx.GetContext(), ctx.WorkflowRunId(), messages.Invocation{
			Stage: state.Verifier, Input: state.Message, TaskID: "verify", To: "caller",
		})
		state.Message = ref
		return state, err
	}
}
