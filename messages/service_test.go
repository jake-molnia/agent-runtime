package messages

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
)

type testResult struct {
	Value string `json:"value"`
}

func resultValidator() Validator {
	return Typed(func(value testResult) error {
		if value.Value == "" {
			return errors.New("value required")
		}
		return nil
	})
}

func resultPart(value string) Part {
	data, _ := json.Marshal(testResult{Value: value})
	return Part{Name: "result", Kind: Data, Schema: "result/v1", Data: data}
}

func resultContract() Contract {
	return Contract{Name: "result", Kind: Data, Schema: "result/v1", Required: true}
}

func testService(t *testing.T, root string, execute Executor, inline int) *Service {
	t.Helper()
	service, err := New(Directory{Root: root}, map[string]Validator{"result/v1": resultValidator()}, []Stage{
		{Actor: Actor{Agent: "producer", Revision: "rev-a"}, Inputs: []Contract{{Name: "request", Kind: Text, Required: true}}, Outputs: []Contract{resultContract()}, Execute: execute},
		{Actor: Actor{Agent: "verifier", Revision: "rev-b"}, Inputs: []Contract{resultContract()}, Outputs: []Contract{resultContract()}, Execute: execute},
	}, inline)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func TestProducerVerifierHandoffAndRestart(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	var calls atomic.Int32
	var received Reference
	execute := func(ctx context.Context, input Delivery) ([]Part, error) {
		calls.Add(1)
		if input.ExecutionID == "" {
			t.Error("missing stable execution identity")
		}
		if input.Message.To == "producer" {
			return []Part{resultPart("candidate")}, nil
		}
		if input.Message.From != (Actor{Agent: "producer", Revision: "rev-a"}) || string(input.Message.Parts[0].Data) != `{"value":"candidate"}` {
			t.Errorf("incorrect handoff: %+v", input.Message)
		}
		received = input.Reference
		prompt, err := input.Prompt()
		if err != nil || !strings.Contains(prompt, `"schema":"result/v1"`) || !strings.Contains(prompt, input.Reference.Digest) || strings.Contains(prompt, "review this") {
			t.Errorf("prompt did not select just producer output: %s %v", prompt, err)
		}
		return []Part{resultPart("confirmed")}, nil
	}
	service := testService(t, root, execute, 0)
	if err := service.CanRoute(Actor{Agent: "producer", Revision: "rev-a"}, Actor{Agent: "verifier", Revision: "rev-b"}); err != nil {
		t.Fatal(err)
	}
	seed, err := service.Publish(ctx, "run", sampleMessage())
	if err != nil {
		t.Fatal(err)
	}
	produce := Invocation{Stage: Actor{Agent: "producer", Revision: "rev-a"}, Input: seed, TaskID: "produce", To: "verifier"}
	candidate, err := service.Invoke(ctx, "run", produce)
	if err != nil {
		t.Fatal(err)
	}
	verify := Invocation{Stage: Actor{Agent: "verifier", Revision: "rev-b"}, Input: candidate, TaskID: "verify", To: "caller"}
	verdict, err := service.Invoke(ctx, "run", verify)
	if err != nil || received != candidate {
		t.Fatalf("handoff reference mismatch: %+v %v", received, err)
	}
	message, err := service.Read(ctx, "run", verdict)
	if err != nil || message.InReplyTo == nil || *message.InReplyTo != candidate || message.ContextID != "context" || message.TaskID != "verify" {
		t.Fatalf("incorrect reply correlation: %+v %v", message, err)
	}
	restarted := testService(t, root, func(context.Context, Delivery) ([]Part, error) {
		t.Error("committed delivery re-executed")
		return nil, nil
	}, 0)
	for _, invocation := range []Invocation{produce, verify} {
		if _, err := restarted.Invoke(ctx, "run", invocation); err != nil {
			t.Fatal(err)
		}
	}
	if calls.Load() != 2 {
		t.Fatalf("expected two agent calls, got %d", calls.Load())
	}
}

func TestLargeDataSpillsAndHydratesTransparently(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	value := strings.Repeat("large-result-", 1000)
	service := testService(t, root, func(ctx context.Context, input Delivery) ([]Part, error) {
		if input.Message.To == "producer" {
			return []Part{resultPart(value)}, nil
		}
		if input.Message.Parts[0].Attachment != nil || !strings.Contains(string(input.Message.Parts[0].Data), value) {
			t.Error("verifier did not receive hydrated structured data")
		}
		return []Part{resultPart("verified")}, nil
	}, 32)
	seed, err := service.Publish(ctx, "scope", sampleMessage())
	if err != nil {
		t.Fatal(err)
	}
	ref, err := service.Invoke(ctx, "scope", Invocation{Stage: Actor{Agent: "producer", Revision: "rev-a"}, Input: seed, TaskID: "produce", To: "verifier"})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := service.Read(ctx, "scope", ref)
	if err != nil || stored.Parts[0].Attachment == nil || len(stored.Parts[0].Data) != 0 || stored.Parts[0].Schema != "result/v1" {
		t.Fatalf("large output was not stored as attachment: %+v %v", stored, err)
	}
	if _, err := service.Invoke(ctx, "scope", Invocation{Stage: Actor{Agent: "verifier", Revision: "rev-b"}, Input: ref, TaskID: "verify", To: "caller"}); err != nil {
		t.Fatal(err)
	}
}

func TestInvalidDeliveriesDoNotExecute(t *testing.T) {
	ctx := context.Background()
	service := testService(t, t.TempDir(), func(context.Context, Delivery) ([]Part, error) { t.Error("invalid delivery executed"); return nil, nil }, 0)
	seed, _ := service.Publish(ctx, "scope", sampleMessage())
	for name, invocation := range map[string]Invocation{
		"wrong recipient":   {Stage: Actor{Agent: "verifier", Revision: "rev-b"}, Input: seed, TaskID: "verify", To: "caller"},
		"unpinned revision": {Stage: Actor{Agent: "producer", Revision: "new-rev"}, Input: seed, TaskID: "produce", To: "verifier"},
		"wrong digest":      {Stage: Actor{Agent: "producer", Revision: "rev-a"}, Input: Reference{ID: seed.ID, Digest: digest([]byte("wrong"))}, TaskID: "produce", To: "verifier"},
		"empty routing":     {Stage: Actor{Agent: "producer", Revision: "rev-a"}, Input: seed, TaskID: "produce"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := service.Invoke(ctx, "scope", invocation); err == nil {
				t.Fatal("invalid delivery accepted")
			}
		})
	}
	message := sampleMessage()
	message.ID = "bad-contract"
	message.Parts[0].Name = "unexpected"
	ref, _ := service.Publish(ctx, "scope", message)
	if _, err := service.Invoke(ctx, "scope", Invocation{Stage: Actor{Agent: "producer", Revision: "rev-a"}, Input: ref, TaskID: "produce", To: "verifier"}); err == nil {
		t.Fatal("contract mismatch accepted")
	}
	if _, err := service.Invoke(ctx, "other-scope", Invocation{Stage: Actor{Agent: "producer", Revision: "rev-a"}, Input: seed, TaskID: "produce", To: "verifier"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-scope delivery accepted: %v", err)
	}
}

func TestInvalidOutputIsNotCommitted(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	var calls int
	service := testService(t, root, func(context.Context, Delivery) ([]Part, error) {
		calls++
		if calls == 1 {
			return []Part{resultPart("")}, nil
		}
		return []Part{resultPart("valid")}, nil
	}, 0)
	seed, _ := service.Publish(ctx, "scope", sampleMessage())
	invocation := Invocation{Stage: Actor{Agent: "producer", Revision: "rev-a"}, Input: seed, TaskID: "produce", To: "verifier"}
	if _, err := service.Invoke(ctx, "scope", invocation); err == nil {
		t.Fatal("invalid structured result committed")
	}
	if _, err := service.Invoke(ctx, "scope", invocation); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("invalid attempt cached: %d calls", calls)
	}
}

func TestBinaryAttachmentRequiresExplicitAdapter(t *testing.T) {
	ctx := context.Background()
	store := Directory{Root: t.TempDir()}
	file, _ := store.PutAttachment(ctx, "scope", "image/png", []byte{0, 1, 2, 3})
	stage := Stage{Actor: Actor{Agent: "image-agent", Revision: "v1"}, Inputs: []Contract{{Name: "image", Kind: File, Required: true}}, Outputs: []Contract{{Name: "result", Kind: Text, Required: true}}, Execute: func(ctx context.Context, input Delivery) ([]Part, error) {
		if _, err := input.Prompt(); err == nil {
			t.Error("binary attachment silently converted to text")
		}
		data, err := input.Attachment(ctx, "image")
		if err != nil || len(data) != 4 {
			t.Errorf("attachment unavailable: %v", err)
		}
		if _, err := input.Attachment(ctx, "other"); err == nil {
			t.Error("undeclared attachment accessible")
		}
		return []Part{{Name: "result", Kind: Text, Text: "inspected"}}, nil
	}}
	service, err := New(store, nil, []Stage{stage}, 0)
	if err != nil {
		t.Fatal(err)
	}
	message := sampleMessage()
	message.To = stage.Actor.Agent
	message.Parts = []Part{{Name: "image", Kind: File, Attachment: &file}}
	ref, err := service.Publish(ctx, "scope", message)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := service.Invoke(ctx, "scope", Invocation{Stage: stage.Actor, Input: ref, TaskID: "inspect", To: "caller"}); err != nil {
		t.Fatal(err)
	}
}

func TestRegistryAndContractsFailClosed(t *testing.T) {
	base := Stage{Actor: Actor{Agent: "producer", Revision: "v1"}, Inputs: []Contract{{Name: "request", Kind: Text}}, Outputs: []Contract{resultContract()}, Execute: func(context.Context, Delivery) ([]Part, error) { return nil, nil }}
	for name, mutate := range map[string]func(*Stage){
		"missing executor":    func(stage *Stage) { stage.Execute = nil },
		"unknown schema":      func(stage *Stage) { stage.Outputs = []Contract{{Name: "output", Kind: Data, Schema: "unknown"}} },
		"missing contracts":   func(stage *Stage) { stage.Inputs = nil },
		"duplicate contracts": func(stage *Stage) { stage.Inputs = append(stage.Inputs, stage.Inputs[0]) },
	} {
		t.Run(name, func(t *testing.T) {
			stage := base
			mutate(&stage)
			if _, err := New(Directory{Root: t.TempDir()}, map[string]Validator{"result/v1": resultValidator()}, []Stage{stage}, 0); err == nil {
				t.Fatal("invalid registry accepted")
			}
		})
	}
	service := testService(t, t.TempDir(), base.Execute, 0)
	if err := service.CanRoute(Actor{Agent: "verifier", Revision: "rev-b"}, Actor{Agent: "producer", Revision: "rev-a"}); err == nil {
		t.Fatal("incompatible route accepted")
	}
}

func TestReceiveRestoresDataAndChecksRecipient(t *testing.T) {
	ctx := context.Background()
	service := testService(t, t.TempDir(), func(context.Context, Delivery) ([]Part, error) { return nil, nil }, 8)
	message := sampleMessage()
	message.Parts = []Part{resultPart("a result exceeding the inline limit")}
	message.To = "publisher"
	ref, err := service.Publish(ctx, "scope", message)
	if err != nil {
		t.Fatal(err)
	}
	received, err := service.Receive(ctx, "scope", ref, "publisher")
	if err != nil || received.Message.Parts[0].Attachment != nil || received.Reference != ref {
		t.Fatalf("receive: %+v %v", received, err)
	}
	if _, err := service.Receive(ctx, "scope", ref, "another-recipient"); err == nil {
		t.Fatal("wrong recipient accepted")
	}
	message.ID = "reply"
	message.InReplyTo = &ref
	message.ContextID = "another-context"
	if _, err := service.Publish(ctx, "scope", message); err == nil {
		t.Fatal("cross-context reply accepted")
	}
}

func TestDataStructureValidationDoesNotDependOnStorage(t *testing.T) {
	ctx := context.Background()
	for name, data := range map[string]json.RawMessage{
		"duplicate keys": json.RawMessage(`{"value":1,"value":2}`),
		"deep nesting":   json.RawMessage(strings.Repeat("[", 65) + "0" + strings.Repeat("]", 65)),
	} {
		for _, storage := range []string{"inline", "spilled", "attachment"} {
			t.Run(name+"/"+storage, func(t *testing.T) {
				store := Directory{Root: t.TempDir()}
				inline := DefaultInlineBytes
				if storage == "spilled" {
					inline = 1
				}
				service, err := New(store, map[string]Validator{"json": func(json.RawMessage) error { return nil }}, nil, inline)
				if err != nil {
					t.Fatal(err)
				}
				message := sampleMessage()
				part := Part{Name: "result", Kind: Data, Schema: "json", Data: data}
				if storage == "attachment" {
					attachment, err := store.PutAttachment(ctx, "scope", "application/json", data)
					if err != nil {
						t.Fatal(err)
					}
					part.Data, part.Attachment = nil, &attachment
				}
				message.Parts = []Part{part}
				if _, err := service.Publish(ctx, "scope", message); err == nil {
					t.Fatal("invalid JSON structure accepted for publication")
				}
				if storage == "attachment" {
					ref, err := store.Put(ctx, "scope", message)
					if err != nil {
						t.Fatal(err)
					}
					if _, err := service.Receive(ctx, "scope", ref, message.To); err == nil {
						t.Fatal("preexisting attachment with invalid JSON structure accepted")
					}
				}
			})
		}
	}
}
