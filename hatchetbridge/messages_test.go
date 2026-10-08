package hatchetbridge

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
	"github.com/jake-molnia/agent-runtime/messages"
)

type messageContext struct {
	hatchet.Context
	ctx     context.Context
	run     string
	outputs map[string]MessageChainState
}

func (ctx messageContext) GetContext() context.Context { return ctx.ctx }
func (ctx messageContext) WorkflowRunId() string       { return ctx.run }
func (ctx messageContext) StepOutput(name string, target any) error {
	state, exists := ctx.outputs[name]
	if !exists {
		return errors.New("parent output unavailable")
	}
	data, err := json.Marshal(state)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, target)
}

func chainService(t *testing.T, root string, calls *int, revision string) (*messages.Service, MessageChain) {
	t.Helper()
	chain := MessageChain{Name: "review-verify", Producer: messages.Actor{Agent: "reviewer", Revision: revision}, Verifier: messages.Actor{Agent: "verifier", Revision: "v1"}, Timeout: time.Minute}
	contract := messages.Contract{Name: "body", Kind: messages.Text, Required: true}
	service, err := messages.New(messages.Directory{Root: root}, nil, []messages.Stage{
		{Actor: chain.Producer, Inputs: []messages.Contract{contract}, Outputs: []messages.Contract{contract}, Execute: func(ctx context.Context, input messages.Delivery) ([]messages.Part, error) {
			*calls++
			return []messages.Part{{Name: "body", Kind: messages.Text, Text: "candidate"}}, nil
		}},
		{Actor: chain.Verifier, Inputs: []messages.Contract{contract}, Outputs: []messages.Contract{contract}, Execute: func(ctx context.Context, input messages.Delivery) ([]messages.Part, error) {
			*calls++
			if input.Message.Parts[0].Text != "candidate" || input.Message.From != chain.Producer {
				t.Errorf("verifier received wrong candidate: %+v", input.Message)
			}
			return []messages.Part{{Name: "body", Kind: messages.Text, Text: "confirmed"}}, nil
		}},
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	return service, chain
}

func TestMessageChainPassesReferencesAndPinsRevisions(t *testing.T) {
	root := t.TempDir()
	var calls int
	service, chain := chainService(t, root, &calls, "v1")
	ctx := messageContext{ctx: context.Background(), run: "workflow-id", outputs: make(map[string]MessageChainState)}
	input := MessageChainInput{Parts: []messages.Part{{Name: "body", Kind: messages.Text, Text: "private initial context"}}}
	resolved, err := messageResolve(service, chain)(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	ctx.outputs["resolve"] = resolved
	produced, err := messageProduce(service)(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	ctx.outputs["produce"] = produced
	verified, err := messageVerify(service)(ctx, input)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(ctx.outputs)
	if strings.Contains(string(data), "private initial context") || strings.Contains(string(data), "candidate") {
		t.Fatal("task outputs contain agent payloads instead of references")
	}
	verdict, err := service.Read(ctx.ctx, ctx.run, verified.Message)
	if err != nil || verdict.InReplyTo == nil || *verdict.InReplyTo != produced.Message || verdict.Parts[0].Text != "confirmed" {
		t.Fatalf("incorrect verification output: %+v %v", verdict, err)
	}
	if _, err := messageProduce(service)(ctx, input); err != nil {
		t.Fatal(err)
	}
	if _, err := messageVerify(service)(ctx, input); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("retry executed agents again: %d", calls)
	}
	redeployed, _ := chainService(t, root, &calls, "v2")
	if _, err := messageProduce(redeployed)(ctx, input); err == nil {
		t.Fatal("in-flight run silently changed producer revision")
	}
}

func TestMessageChainRejectsMissingParentAndBadConfiguration(t *testing.T) {
	var calls int
	service, chain := chainService(t, t.TempDir(), &calls, "v1")
	ctx := messageContext{ctx: context.Background(), run: "workflow-id"}
	if _, err := messageProduce(service)(ctx, MessageChainInput{}); err == nil {
		t.Fatal("missing producer parent accepted")
	}
	if _, err := messageVerify(service)(ctx, MessageChainInput{}); err == nil {
		t.Fatal("missing verifier parent accepted")
	}
	for _, invalid := range []MessageChain{{Name: "bad", Timeout: time.Minute}, {Name: "bad", Timeout: 25 * time.Hour}, {Timeout: time.Minute}} {
		if _, err := RegisterMessageChain(new(hatchet.Client), service, invalid); err == nil {
			t.Fatal("invalid chain registered")
		}
	}
	if _, err := RegisterMessageChain(nil, service, chain); err == nil {
		t.Fatal("nil client accepted")
	}
}

func TestMessageChainDeclarationPinnedSDK(t *testing.T) {
	t.Setenv("HATCHET_CLIENT_HOST_PORT", "127.0.0.1:1")
	t.Setenv("HATCHET_CLIENT_SERVER_URL", "http://127.0.0.1:1")
	t.Setenv("HATCHET_CLIENT_TLS_STRATEGY", "none")
	claims, err := json.Marshal(map[string]any{
		"server_url":             "http://127.0.0.1:1",
		"grpc_broadcast_address": "127.0.0.1:1",
		"sub":                    "00000000-0000-4000-8000-000000000001",
		"exp":                    time.Now().Add(time.Hour).Unix(),
	})
	if err != nil {
		t.Fatal(err)
	}
	token := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none"}`)) + "." + base64.RawURLEncoding.EncodeToString(claims) + ".invalid-test-signature"
	client, err := hatchet.NewClient(hatchet.WithToken(token), hatchet.WithNamespace("messages-test"), hatchet.WithTLSConfig(nil))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close(context.Background()) })
	var calls int
	service, chain := chainService(t, t.TempDir(), &calls, "v1")
	workflow, err := RegisterMessageChain(client, service, chain, hatchet.WithWorkflowEvents("review:requested"))
	if err != nil {
		t.Fatal(err)
	}
	declaration, functions, durable, failure := workflow.Dump()
	if declaration.Name != "messages-test_"+chain.Name || !reflect.DeepEqual(declaration.EventTriggers, []string{"messages-test_review:requested"}) || len(declaration.Tasks) != 3 || len(functions) != 3 || len(durable) != 0 || failure != nil {
		t.Fatalf("unexpected SDK declaration: %+v", declaration)
	}
	parents := make(map[string][]string)
	for _, task := range declaration.Tasks {
		parents[task.ReadableId] = task.Parents
		if task.Retries != 2 || task.IsDurable || task.Timeout != "60s" {
			t.Fatalf("unexpected task policy: %+v", task)
		}
	}
	if len(parents["resolve"]) != 0 || !reflect.DeepEqual(parents["produce"], []string{"resolve"}) || !reflect.DeepEqual(parents["verify"], []string{"produce"}) {
		t.Fatalf("incorrect DAG: %+v", parents)
	}
}
