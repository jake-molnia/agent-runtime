package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/jake-molnia/agent-runtime/messages"
)

type candidate struct {
	Text string `json:"text"`
}

type verdict struct {
	Accepted  bool               `json:"accepted"`
	Candidate messages.Reference `json:"candidate"`
}

func main() {
	root := flag.String("store", "", "persistent message directory")
	flag.Parse()
	if *root == "" {
		fmt.Fprintln(os.Stderr, "usage: go run ./examples/message-handoff --store DIRECTORY")
		os.Exit(2)
	}
	if err := run(*root); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(root string) error {
	ctx := context.Background()
	producer := messages.Actor{Agent: "draft-writer", Revision: "demo-v1"}
	verifier := messages.Actor{Agent: "draft-verifier", Revision: "demo-v1"}
	candidateContract := messages.Contract{Name: "candidate", Kind: messages.Data, Schema: "candidate/v1", Required: true}
	service, err := messages.New(messages.Directory{Root: root}, map[string]messages.Validator{
		"candidate/v1": messages.Typed(func(value candidate) error {
			if strings.TrimSpace(value.Text) == "" {
				return errors.New("candidate text required")
			}
			return nil
		}),
		"verdict/v1": messages.Typed(func(value verdict) error { return value.Candidate.Validate() }),
	}, []messages.Stage{
		{Actor: producer, Inputs: []messages.Contract{{Name: "request", Kind: messages.Text, Required: true}}, Outputs: []messages.Contract{candidateContract}, Execute: func(ctx context.Context, input messages.Delivery) ([]messages.Part, error) {
			fmt.Fprintln(os.Stderr, "producer executed")
			data, err := json.Marshal(candidate{Text: "A typed result passed directly to the verifier."})
			return []messages.Part{{Name: "candidate", Kind: messages.Data, Schema: "candidate/v1", Data: data}}, err
		}},
		{Actor: verifier, Inputs: []messages.Contract{candidateContract}, Outputs: []messages.Contract{{Name: "verdict", Kind: messages.Data, Schema: "verdict/v1", Required: true}}, Execute: func(ctx context.Context, input messages.Delivery) ([]messages.Part, error) {
			fmt.Fprintln(os.Stderr, "verifier executed")
			var proposed candidate
			if err := json.Unmarshal(input.Message.Parts[0].Data, &proposed); err != nil {
				return nil, err
			}
			data, err := json.Marshal(verdict{Accepted: strings.Contains(proposed.Text, "verifier"), Candidate: input.Reference})
			return []messages.Part{{Name: "verdict", Kind: messages.Data, Schema: "verdict/v1", Data: data}}, err
		}},
	}, 0)
	if err != nil {
		return err
	}
	seed, err := service.Publish(ctx, "demo-run", messages.Message{Version: messages.Version, ID: "request", ContextID: "demo-review", TaskID: "request", From: messages.Actor{Agent: "caller", Revision: "v1"}, To: producer.Agent, Parts: []messages.Part{{Name: "request", Kind: messages.Text, Text: "Write a draft for verification."}}})
	if err != nil {
		return err
	}
	proposed, err := service.Invoke(ctx, "demo-run", messages.Invocation{Stage: producer, Input: seed, TaskID: "produce", To: verifier.Agent})
	if err != nil {
		return err
	}
	confirmed, err := service.Invoke(ctx, "demo-run", messages.Invocation{Stage: verifier, Input: proposed, TaskID: "verify", To: "caller"})
	if err != nil {
		return err
	}
	message, err := service.Read(ctx, "demo-run", confirmed)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	return encoder.Encode(message)
}
