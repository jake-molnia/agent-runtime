package hatchetbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"time"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
	"github.com/jake-molnia/agent-runtime/agentexec"
	"github.com/jake-molnia/agent-runtime/messages"
)

type MessageNodeInput struct {
	Request messages.Delivery
	Parents map[string]messages.Delivery
}

type MessageNode struct {
	ID         string
	Stage      messages.Actor
	Parents    []string
	BuildInput func(context.Context, MessageNodeInput) ([]messages.Part, error)
}

type MessageDAG struct {
	Name     string
	Revision string
	Nodes    []MessageNode
	Output   string
	Timeout  time.Duration
}

type MessageDAGInput struct {
	Parts []messages.Part `json:"parts"`
}

type MessageDAGState struct {
	Revision string                    `json:"revision"`
	Actors   map[string]messages.Actor `json:"actors"`
	Request  messages.Reference        `json:"request"`
}

type MessageNodeResult struct {
	Revision string             `json:"revision"`
	Node     string             `json:"node"`
	Actor    messages.Actor     `json:"actor"`
	Message  messages.Reference `json:"message"`
}

var messageNodeName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,62}$`)
var messageDAGRevision = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.:-]{0,127}$`)

func orderMessageDAG(service *messages.Service, dag MessageDAG) ([]MessageNode, error) {
	if service == nil || !messageNodeName.MatchString(dag.Name) || !messageDAGRevision.MatchString(dag.Revision) || dag.Timeout <= 0 || dag.Timeout > 24*time.Hour || len(dag.Nodes) == 0 || len(dag.Nodes) > 32 {
		return nil, errors.New("invalid message DAG")
	}
	if !messageNodeName.MatchString(dag.Output) {
		return nil, errors.New("message DAG output node required")
	}
	nodes := make(map[string]MessageNode, len(dag.Nodes))
	for _, node := range dag.Nodes {
		if !messageNodeName.MatchString(node.ID) || node.ID == "resolve" || node.ID == "result" || node.BuildInput == nil || !service.HasStage(node.Stage) {
			return nil, errors.New("invalid or unavailable message node")
		}
		if _, exists := nodes[node.ID]; exists {
			return nil, errors.New("duplicate message node")
		}
		node.Parents = append([]string(nil), node.Parents...)
		slices.Sort(node.Parents)
		for index, parent := range node.Parents {
			if parent == node.ID || (index > 0 && parent == node.Parents[index-1]) {
				return nil, errors.New("invalid or duplicate node dependency")
			}
		}
		nodes[node.ID] = node
	}
	if _, exists := nodes[dag.Output]; !exists {
		return nil, errors.New("unknown message DAG output")
	}
	states := map[string]int{}
	ordered := make([]MessageNode, 0, len(nodes))
	var visit func(string) error
	visit = func(id string) error {
		node, exists := nodes[id]
		if !exists {
			return fmt.Errorf("unknown node dependency: %s", id)
		}
		if states[id] == 1 {
			return errors.New("message DAG contains a cycle")
		}
		if states[id] == 2 {
			return nil
		}
		states[id] = 1
		for _, parent := range node.Parents {
			if err := visit(parent); err != nil {
				return err
			}
		}
		states[id] = 2
		ordered = append(ordered, node)
		return nil
	}
	if err := visit(dag.Output); err != nil {
		return nil, err
	}
	if len(ordered) != len(nodes) {
		return nil, errors.New("all nodes must contribute to the selected output")
	}
	return ordered, nil
}

func RegisterMessageDAG(client *hatchet.Client, service *messages.Service, dag MessageDAG, options ...hatchet.WorkflowOption) (*hatchet.Workflow, error) {
	if client == nil {
		return nil, errors.New("Hatchet client required")
	}
	ordered, err := orderMessageDAG(service, dag)
	if err != nil {
		return nil, err
	}
	actors := make(map[string]messages.Actor, len(ordered))
	for _, node := range ordered {
		actors[node.ID] = node.Stage
	}
	options = append(append([]hatchet.WorkflowOption(nil), options...), hatchet.WithWorkflowVersion(dag.Revision))
	workflow := client.NewWorkflow(dag.Name, options...)
	recipient := dag.Name + ".outputs"
	resolve := workflow.NewTask("resolve", func(ctx hatchet.Context, input MessageDAGInput) (MessageDAGState, error) {
		pinned := make(map[string]messages.Actor, len(actors))
		for id, actor := range actors {
			pinned[id] = actor
		}
		ref, err := service.Publish(ctx.GetContext(), ctx.WorkflowRunId(), messages.Message{Version: messages.Version, ID: "request", ContextID: ctx.WorkflowRunId(), TaskID: "resolve", From: messages.Actor{Agent: "caller", Revision: "v1"}, To: recipient, Parts: input.Parts})
		return MessageDAGState{Revision: dag.Revision, Actors: pinned, Request: ref}, err
	}, hatchet.WithRetries(0), hatchet.WithExecutionTimeout(time.Minute))
	tasks := make(map[string]*hatchet.Task, len(ordered))
	for _, node := range ordered {
		parents := []*hatchet.Task{resolve}
		for _, parent := range node.Parents {
			parents = append(parents, tasks[parent])
		}
		tasks[node.ID] = workflow.NewDurableTask(node.ID, func(ctx hatchet.DurableContext, input MessageDAGInput) (MessageNodeResult, error) {
			var state MessageDAGState
			if err := ctx.StepOutput("resolve", &state); err != nil {
				return MessageNodeResult{}, err
			}
			if state.Revision != dag.Revision || len(state.Actors) != len(actors) {
				return MessageNodeResult{}, errors.New("pinned workflow revision is unavailable")
			}
			for id, actor := range actors {
				if state.Actors[id] != actor {
					return MessageNodeResult{}, errors.New("pinned agent revision is unavailable")
				}
			}
			request, err := service.Receive(ctx.GetContext(), ctx.WorkflowRunId(), state.Request, recipient)
			if err != nil {
				return MessageNodeResult{}, err
			}
			inputs := MessageNodeInput{Request: request, Parents: make(map[string]messages.Delivery, len(node.Parents))}
			for _, parent := range node.Parents {
				var output MessageNodeResult
				if err := ctx.StepOutput(parent, &output); err != nil {
					return MessageNodeResult{}, err
				}
				if output.Revision != state.Revision || output.Node != parent || output.Actor != state.Actors[parent] {
					return MessageNodeResult{}, errors.New("node output identity mismatch")
				}
				delivery, err := service.Receive(ctx.GetContext(), ctx.WorkflowRunId(), output.Message, recipient)
				if err != nil {
					return MessageNodeResult{}, err
				}
				if delivery.Message.From != output.Actor {
					return MessageNodeResult{}, errors.New("node message actor mismatch")
				}
				inputs.Parents[parent] = delivery
			}
			parts, err := node.BuildInput(ctx.GetContext(), inputs)
			if err != nil {
				return MessageNodeResult{}, err
			}
			inputRef, err := service.Publish(ctx.GetContext(), ctx.WorkflowRunId(), messages.Message{Version: messages.Version, ID: "input-" + node.ID, ContextID: ctx.WorkflowRunId(), TaskID: node.ID, From: messages.Actor{Agent: "workflow", Revision: state.Revision}, To: node.Stage.Agent, InReplyTo: &state.Request, Parts: parts})
			if err != nil {
				return MessageNodeResult{}, err
			}
			bounded := agentexec.WithInteractionWait(ctx.GetContext(), func(waitCtx context.Context, sessionID, kind string) error {
				return interactionWait(ctx, sessionID)(waitCtx, kind)
			})
			ref, err := service.Invoke(bounded, ctx.WorkflowRunId(), messages.Invocation{Stage: state.Actors[node.ID], Input: inputRef, TaskID: node.ID, To: recipient})
			return MessageNodeResult{Revision: state.Revision, Node: node.ID, Actor: state.Actors[node.ID], Message: ref}, err
		}, hatchet.WithParents(parents...), hatchet.WithRetries(0), hatchet.WithExecutionTimeout(dag.Timeout), hatchet.WithScheduleTimeout(time.Hour))
	}
	workflow.NewTask("result", func(ctx hatchet.Context, input MessageDAGInput) (MessageNodeResult, error) {
		var state MessageDAGState
		if err := ctx.StepOutput("resolve", &state); err != nil {
			return MessageNodeResult{}, err
		}
		if state.Revision != dag.Revision || len(state.Actors) != len(actors) {
			return MessageNodeResult{}, errors.New("pinned workflow revision is unavailable")
		}
		for id, actor := range actors {
			if state.Actors[id] != actor {
				return MessageNodeResult{}, errors.New("pinned agent revision is unavailable")
			}
		}
		var result MessageNodeResult
		if err := ctx.StepOutput(dag.Output, &result); err != nil {
			return result, err
		}
		if result.Revision != state.Revision || result.Node != dag.Output || result.Actor != state.Actors[dag.Output] {
			return MessageNodeResult{}, errors.New("final node output identity mismatch")
		}
		delivery, err := service.Receive(ctx.GetContext(), ctx.WorkflowRunId(), result.Message, recipient)
		if err != nil {
			return MessageNodeResult{}, err
		}
		if delivery.Message.From != result.Actor {
			return MessageNodeResult{}, errors.New("final message actor mismatch")
		}
		return result, nil
	}, hatchet.WithParents(resolve, tasks[dag.Output]), hatchet.WithRetries(0), hatchet.WithExecutionTimeout(time.Minute))
	return workflow, nil
}

func MessageData(delivery messages.Delivery, name string) (json.RawMessage, error) {
	for _, part := range delivery.Message.Parts {
		if part.Name == name && part.Kind == messages.Data && part.Attachment == nil {
			return append(json.RawMessage(nil), part.Data...), nil
		}
	}
	return nil, errors.New("named data part is unavailable")
}
