package hatchetbridge

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
	"github.com/jake-molnia/agent-runtime/messages"
)

type dagTestContext struct {
	hatchet.DurableContext
	input   MessageDAGInput
	outputs map[string]any
	run     string
}

func (ctx dagTestContext) GetContext() context.Context { return context.Background() }
func (ctx dagTestContext) WorkflowRunId() string       { return ctx.run }
func (ctx dagTestContext) WorkflowInput(target any) error {
	data, err := json.Marshal(ctx.input)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}
func (ctx dagTestContext) StepOutput(name string, target any) error {
	output, exists := ctx.outputs[name]
	if !exists {
		return errors.New("parent output unavailable")
	}
	data, err := json.Marshal(output)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

type dagTestValue struct {
	Value string `json:"value"`
}

func dagTestPart(name, value string) messages.Part {
	data, _ := json.Marshal(dagTestValue{Value: value})
	return messages.Part{Name: name, Kind: messages.Data, Schema: "test-value", Data: data}
}

func dagTestData(t *testing.T, delivery messages.Delivery, name string) string {
	t.Helper()
	data, err := MessageData(delivery, name)
	if err != nil {
		t.Fatal(err)
	}
	var value dagTestValue
	if err := json.Unmarshal(data, &value); err != nil {
		t.Fatal(err)
	}
	return value.Value
}

type dagTestFixture struct {
	service    *messages.Service
	dag        MessageDAG
	calls      map[string]int
	deliveries map[string]messages.Delivery
}

func newDAGTestFixture(t *testing.T) dagTestFixture {
	t.Helper()
	fixture := dagTestFixture{calls: map[string]int{}, deliveries: map[string]messages.Delivery{}}
	actor := func(name string) messages.Actor { return messages.Actor{Agent: name + "-agent", Revision: "agent-v1"} }
	contract := func(name string) messages.Contract {
		return messages.Contract{Name: name, Kind: messages.Data, Schema: "test-value", Required: true}
	}
	var stages []messages.Stage
	for _, name := range []string{"review", "verify", "adversarial", "writer"} {
		inputs := []messages.Contract{contract("context")}
		output := "candidate"
		switch name {
		case "verify":
			inputs = append(inputs, contract("candidate"))
			output = "verified"
		case "adversarial":
			output = "adversarial-report"
		case "writer":
			inputs = append(inputs, contract("verified"), contract("adversarial-report"))
			output = "final"
		}
		stages = append(stages, messages.Stage{Actor: actor(name), Inputs: inputs, Outputs: []messages.Contract{contract(output)}, Execute: func(_ context.Context, delivery messages.Delivery) ([]messages.Part, error) {
			fixture.calls[name]++
			fixture.deliveries[name] = delivery
			if delivery.Message.To != actor(name).Agent || delivery.Message.From != (messages.Actor{Agent: "workflow", Revision: "graph-v1"}) || delivery.Message.ContextID != "dag-run" || delivery.Message.TaskID != name || delivery.ExecutionID == "" {
				t.Errorf("%s incorrect invocation envelope: %+v", name, delivery)
			}
			if got := dagTestData(t, delivery, "context"); got != "request-context" {
				t.Errorf("%s context = %q", name, got)
			}
			switch name {
			case "verify":
				if got := dagTestData(t, delivery, "candidate"); got != "review-output" {
					t.Errorf("candidate = %q", got)
				}
			case "adversarial":
				if len(delivery.Message.Parts) != 1 {
					t.Error("adversarial received candidate data")
				}
			case "writer":
				if got := dagTestData(t, delivery, "verified"); got != "verify-output" {
					t.Errorf("verified = %q", got)
				}
				if got := dagTestData(t, delivery, "adversarial-report"); got != "adversarial-output" {
					t.Errorf("adversarial report = %q", got)
				}
			}
			return []messages.Part{dagTestPart(output, name+"-output")}, nil
		}})
	}
	validator := func(data json.RawMessage) error {
		var value dagTestValue
		if err := json.Unmarshal(data, &value); err != nil {
			return err
		}
		if value.Value == "" {
			return errors.New("value required")
		}
		return nil
	}
	service, err := messages.New(messages.Directory{Root: t.TempDir()}, map[string]messages.Validator{"test-value": validator}, stages, 8)
	if err != nil {
		t.Fatal(err)
	}
	fixture.service = service
	build := func(parents ...string) func(context.Context, MessageNodeInput) ([]messages.Part, error) {
		return func(_ context.Context, input MessageNodeInput) ([]messages.Part, error) {
			parts := append([]messages.Part(nil), input.Request.Message.Parts...)
			for _, parent := range parents {
				parts = append(parts, input.Parents[parent].Message.Parts...)
			}
			return parts, nil
		}
	}
	fixture.dag = MessageDAG{Name: "generic-graph", Revision: "graph-v1", Timeout: 2 * time.Minute, Output: "writer", Nodes: []MessageNode{
		{ID: "writer", Stage: actor("writer"), Parents: []string{"verify", "adversarial"}, BuildInput: build("verify", "adversarial")},
		{ID: "adversarial", Stage: actor("adversarial"), Parents: []string{"review"}, BuildInput: build()},
		{ID: "verify", Stage: actor("verify"), Parents: []string{"review"}, BuildInput: build("review")},
		{ID: "review", Stage: actor("review"), BuildInput: build()},
	}}
	return fixture
}

func dagTestCallbacks(t *testing.T, workflow *hatchet.Workflow) map[string]func(hatchet.Context) (any, error) {
	t.Helper()
	_, regular, durable, _ := workflow.Dump()
	callbacks := map[string]func(hatchet.Context) (any, error){}
	for _, function := range append(regular, durable...) {
		id := function.ActionID[strings.LastIndex(function.ActionID, ":")+1:]
		if _, exists := callbacks[id]; exists {
			t.Fatalf("duplicate callback %s", id)
		}
		callbacks[id] = function.Fn
	}
	return callbacks
}

func dagTestResolve(t *testing.T, client *hatchet.Client, fixture dagTestFixture) (dagTestContext, map[string]func(hatchet.Context) (any, error)) {
	t.Helper()
	workflow, err := RegisterMessageDAG(client, fixture.service, fixture.dag)
	if err != nil {
		t.Fatal(err)
	}
	callbacks := dagTestCallbacks(t, workflow)
	ctx := dagTestContext{run: "dag-run", input: MessageDAGInput{Parts: []messages.Part{dagTestPart("context", "request-context")}}, outputs: map[string]any{}}
	state, err := callbacks["resolve"](ctx)
	if err != nil {
		t.Fatal(err)
	}
	ctx.outputs["resolve"] = state
	return ctx, callbacks
}

func TestMessageDAGValidation(t *testing.T) {
	client := offlineLifecycleClient(t)
	tests := []struct {
		name   string
		change func(*MessageDAG)
	}{
		{"cycle", func(dag *MessageDAG) { dag.Nodes[3].Parents = []string{"writer"} }},
		{"missing-parent", func(dag *MessageDAG) { dag.Nodes[2].Parents = []string{"absent"} }},
		{"duplicate-node", func(dag *MessageDAG) { dag.Nodes = append(dag.Nodes, dag.Nodes[3]) }},
		{"duplicate-parent", func(dag *MessageDAG) { dag.Nodes[2].Parents = []string{"review", "review"} }},
		{"self-parent", func(dag *MessageDAG) { dag.Nodes[2].Parents = []string{"verify"} }},
		{"missing-output", func(dag *MessageDAG) { dag.Output = "absent" }},
		{"unreachable-output", func(dag *MessageDAG) { dag.Output = "review" }},
		{"unknown-actor", func(dag *MessageDAG) { dag.Nodes[2].Stage.Agent = "absent" }},
		{"unknown-actor-revision", func(dag *MessageDAG) { dag.Nodes[2].Stage.Revision = "absent" }},
		{"reserved-resolve", func(dag *MessageDAG) {
			dag.Nodes = []MessageNode{dag.Nodes[3]}
			dag.Nodes[0].ID, dag.Output = "resolve", "resolve"
		}},
		{"reserved-result", func(dag *MessageDAG) {
			dag.Nodes = []MessageNode{dag.Nodes[3]}
			dag.Nodes[0].ID, dag.Output = "result", "result"
		}},
		{"invalid-id", func(dag *MessageDAG) { dag.Nodes[3].ID = "invalid.id" }},
		{"missing-adapter", func(dag *MessageDAG) { dag.Nodes[3].BuildInput = nil }},
		{"missing-revision", func(dag *MessageDAG) { dag.Revision = "" }},
		{"missing-timeout", func(dag *MessageDAG) { dag.Timeout = 0 }},
		{"excess-timeout", func(dag *MessageDAG) { dag.Timeout = 25 * time.Hour }},
		{"empty-nodes", func(dag *MessageDAG) { dag.Nodes = nil }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fixture := newDAGTestFixture(t)
			test.change(&fixture.dag)
			if _, err := RegisterMessageDAG(client, fixture.service, fixture.dag); err == nil {
				t.Fatal("invalid DAG registered")
			}
		})
	}
	fixture := newDAGTestFixture(t)
	if _, err := RegisterMessageDAG(nil, fixture.service, fixture.dag); err == nil {
		t.Fatal("nil client accepted")
	}
	if _, err := RegisterMessageDAG(client, nil, fixture.dag); err == nil {
		t.Fatal("nil service accepted")
	}
}

func TestMessageDAGSDKDeclaration(t *testing.T) {
	fixture := newDAGTestFixture(t)
	workflow, err := RegisterMessageDAG(offlineLifecycleClient(t), fixture.service, fixture.dag, hatchet.WithWorkflowEvents("test:requested"))
	if err != nil {
		t.Fatal(err)
	}
	declaration, regular, durable, failure := workflow.Dump()
	if declaration.Name != "_"+fixture.dag.Name || declaration.Version != fixture.dag.Revision || !reflect.DeepEqual(declaration.EventTriggers, []string{"_test:requested"}) || len(regular) != 2 || len(durable) != 4 || failure != nil || len(declaration.Tasks) != 6 {
		t.Fatalf("incorrect workflow declaration: %+v", declaration)
	}
	parents := map[string][]string{"resolve": nil, "review": {"resolve"}, "verify": {"resolve", "review"}, "adversarial": {"resolve", "review"}, "writer": {"resolve", "adversarial", "verify"}, "result": {"resolve", "writer"}}
	for _, task := range declaration.Tasks {
		wantParents, exists := parents[task.ReadableId]
		if !exists || len(task.Parents) != len(wantParents) || len(wantParents) > 0 && !reflect.DeepEqual(task.Parents, wantParents) {
			t.Errorf("%s parents = %v, want %v", task.ReadableId, task.Parents, wantParents)
		}
		wantDurable := task.ReadableId != "resolve" && task.ReadableId != "result"
		wantTimeout := "60s"
		if wantDurable {
			wantTimeout = "120s"
		}
		if task.Retries != 0 || task.IsDurable != wantDurable || task.Timeout != wantTimeout {
			t.Errorf("incorrect node policy: %+v", task)
		}
		if task.Action != "_"+fixture.dag.Name+":"+task.ReadableId || wantDurable && (task.ScheduleTimeout == nil || *task.ScheduleTimeout != "3600s") {
			t.Errorf("incorrect action namespace or schedule timeout: %+v", task)
		}
	}
	if !reflect.DeepEqual(fixture.dag.Nodes[0].Parents, []string{"verify", "adversarial"}) {
		t.Fatal("registration mutated caller dependencies")
	}
}

func TestMessageDAGTypedRoutingAndReplay(t *testing.T) {
	fixture := newDAGTestFixture(t)
	ctx, callbacks := dagTestResolve(t, offlineLifecycleClient(t), fixture)
	state := ctx.outputs["resolve"].(MessageDAGState)
	if state.Revision != fixture.dag.Revision || len(state.Actors) != 4 {
		t.Fatalf("incorrect pins: %+v", state)
	}
	for _, node := range fixture.dag.Nodes {
		if state.Actors[node.ID] != node.Stage {
			t.Fatalf("incorrect actor pin %s", node.ID)
		}
	}
	for _, id := range []string{"review", "verify", "adversarial", "writer", "result"} {
		output, err := callbacks[id](ctx)
		if err != nil {
			t.Fatalf("%s: %v", id, err)
		}
		ctx.outputs[id] = output
		result := output.(MessageNodeResult)
		if id == "result" {
			if result != ctx.outputs["writer"].(MessageNodeResult) {
				t.Fatal("result did not expose writer reference")
			}
			continue
		}
		delivery, err := fixture.service.Receive(ctx.GetContext(), ctx.run, result.Message, fixture.dag.Name+".outputs")
		if err != nil {
			t.Fatal(err)
		}
		if delivery.Message.From != state.Actors[id] || delivery.Message.TaskID != id || delivery.Message.ContextID != ctx.run || result.Actor != state.Actors[id] || result.Node != id || result.Revision != state.Revision {
			t.Fatalf("%s incorrect output envelope: %+v", id, delivery.Message)
		}
		input := fixture.deliveries[id]
		if delivery.Message.InReplyTo == nil || *delivery.Message.InReplyTo != input.Reference || input.Message.InReplyTo == nil || *input.Message.InReplyTo != state.Request {
			t.Fatalf("%s reply references incorrect", id)
		}
		if _, err := fixture.service.Receive(ctx.GetContext(), "other-run", result.Message, fixture.dag.Name+".outputs"); err == nil {
			t.Fatal("cross-scope output readable")
		}
		if _, err := fixture.service.Receive(ctx.GetContext(), ctx.run, result.Message, "wrong-recipient"); err == nil {
			t.Fatal("incorrect output recipient accepted")
		}
		stored, err := fixture.service.Read(ctx.GetContext(), ctx.run, result.Message)
		if err != nil || stored.Parts[0].Attachment == nil {
			t.Fatalf("typed output not stored as scoped attachment: %+v %v", stored, err)
		}
	}
	encoded, err := json.Marshal(ctx.outputs)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{"request-context", "review-output", "verify-output", "adversarial-output", "writer-output"} {
		if strings.Contains(string(encoded), value) {
			t.Fatal("workflow output leaks payloads")
		}
	}
	for _, id := range []string{"resolve", "review", "verify", "adversarial", "writer", "result"} {
		repeated, err := callbacks[id](ctx)
		if err != nil || !reflect.DeepEqual(repeated, ctx.outputs[id]) {
			t.Fatalf("%s replay changed output: %+v %v", id, repeated, err)
		}
	}
	for id, count := range fixture.calls {
		if count != 1 {
			t.Errorf("%s executed %d times", id, count)
		}
	}
}

func TestMessageDAGRejectsUntrustedParents(t *testing.T) {
	client := offlineLifecycleClient(t)
	for _, mode := range []string{"workflow-revision", "actor-revision", "missing-actor", "extra-actor", "request-scope", "parent-scope", "parent-actor", "message-actor", "parent-revision", "parent-node", "missing-parent"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newDAGTestFixture(t)
			ctx, callbacks := dagTestResolve(t, client, fixture)
			reviewed, err := callbacks["review"](ctx)
			if err != nil {
				t.Fatal(err)
			}
			state := ctx.outputs["resolve"].(MessageDAGState)
			parent := reviewed.(MessageNodeResult)
			switch mode {
			case "workflow-revision":
				state.Revision = "old-graph"
			case "actor-revision":
				state.Actors["verify"] = messages.Actor{Agent: "verify-agent", Revision: "old-agent"}
			case "missing-actor":
				delete(state.Actors, "writer")
			case "extra-actor":
				state.Actors["extra"] = parent.Actor
			case "parent-actor":
				parent.Actor.Revision = "old-agent"
			case "parent-revision":
				parent.Revision = "old-graph"
			case "parent-node":
				parent.Node = "adversarial"
			case "request-scope", "parent-scope", "message-actor":
				ref := parent.Message
				if mode == "request-scope" {
					ref = state.Request
				}
				message, readErr := fixture.service.Read(ctx.GetContext(), ctx.run, ref)
				if readErr != nil {
					t.Fatal(readErr)
				}
				delivery, receiveErr := fixture.service.Receive(ctx.GetContext(), ctx.run, ref, fixture.dag.Name+".outputs")
				if receiveErr != nil {
					t.Fatal(receiveErr)
				}
				message.Parts, message.InReplyTo, message.ID = delivery.Message.Parts, nil, "forged-message"
				scope := "other-run"
				if mode == "message-actor" {
					scope = ctx.run
					message.From = messages.Actor{Agent: "untrusted", Revision: "v1"}
				}
				forged, publishErr := fixture.service.Publish(ctx.GetContext(), scope, message)
				if publishErr != nil {
					t.Fatal(publishErr)
				}
				if mode == "request-scope" {
					state.Request = forged
				} else {
					parent.Message = forged
				}
			}
			ctx.outputs["resolve"], ctx.outputs["review"] = state, parent
			if mode == "missing-parent" {
				delete(ctx.outputs, "review")
			}
			if _, err := callbacks["verify"](ctx); err == nil {
				t.Fatal("untrusted parent accepted")
			}
			if fixture.calls["verify"] != 0 {
				t.Fatal("executor ran before parent validation")
			}
		})
	}
}

func TestMessageDAGRejectsInvalidAdapterData(t *testing.T) {
	client := offlineLifecycleClient(t)
	for _, mode := range []string{"malformed-data", "destination-contract", "adapter-error"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newDAGTestFixture(t)
			fixture.dag.Nodes[2].BuildInput = func(context.Context, MessageNodeInput) ([]messages.Part, error) {
				switch mode {
				case "malformed-data":
					return []messages.Part{{Name: "context", Kind: messages.Data, Schema: "test-value", Data: json.RawMessage(`{"value":42}`)}}, nil
				case "destination-contract":
					return []messages.Part{dagTestPart("context", "request-context"), dagTestPart("wrong-name", "candidate")}, nil
				default:
					return nil, errors.New("adapter failed")
				}
			}
			ctx, callbacks := dagTestResolve(t, client, fixture)
			output, err := callbacks["review"](ctx)
			if err != nil {
				t.Fatal(err)
			}
			ctx.outputs["review"] = output
			if _, err := callbacks["verify"](ctx); err == nil {
				t.Fatal("invalid adapter input accepted")
			}
			if fixture.calls["verify"] != 0 {
				t.Fatal("executor received invalid adapter input")
			}
		})
	}
}

func TestMessageDAGResultRejectsStalePins(t *testing.T) {
	client := offlineLifecycleClient(t)
	for _, mode := range []string{"redeployed-workflow", "output-revision", "output-actor", "output-node"} {
		t.Run(mode, func(t *testing.T) {
			fixture := newDAGTestFixture(t)
			ctx, callbacks := dagTestResolve(t, client, fixture)
			for _, id := range []string{"review", "verify", "adversarial", "writer"} {
				output, err := callbacks[id](ctx)
				if err != nil {
					t.Fatal(err)
				}
				ctx.outputs[id] = output
			}
			output := ctx.outputs["writer"].(MessageNodeResult)
			switch mode {
			case "redeployed-workflow":
				fixture.dag.Revision = "graph-v2"
				workflow, err := RegisterMessageDAG(client, fixture.service, fixture.dag)
				if err != nil {
					t.Fatal(err)
				}
				callbacks = dagTestCallbacks(t, workflow)
			case "output-revision":
				output.Revision = "old-graph"
			case "output-actor":
				output.Actor.Revision = "old-agent"
			case "output-node":
				output.Node = "review"
			}
			ctx.outputs["writer"] = output
			if _, err := callbacks["result"](ctx); err == nil {
				t.Fatal("result accepted stale or mismatched final output")
			}
		})
	}
}
