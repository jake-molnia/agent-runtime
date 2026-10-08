// Package agentexec adapts pinned agent definitions to message stages.
// It runs native sessions, not nested workflows. Execution is not exactly-once:
// a successful session can run again if its message output was not persisted.
package agentexec

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jake-molnia/agent-runtime/definitions"
	"github.com/jake-molnia/agent-runtime/messages"
	"github.com/jake-molnia/agent-runtime/orchestration"
)

type Backend interface {
	Provision(context.Context, orchestration.Definition, orchestration.Request) (orchestration.Prepared, error)
	Execute(context.Context, orchestration.Definition, orchestration.Request, orchestration.Prepared, func(context.Context, string) error) (orchestration.Result, error)
	Collect(context.Context, orchestration.Request, orchestration.Prepared) (string, error)
	ReadOutput(context.Context, orchestration.Request, orchestration.Prepared) (json.RawMessage, error)
	Cleanup(context.Context, orchestration.Request, orchestration.Prepared) error
	Cancel(context.Context, orchestration.Definition, orchestration.Request) error
}

type InteractionWait func(ctx context.Context, sessionID, kind string) error

type interactionWaitKey struct{}

func WithInteractionWait(ctx context.Context, wait InteractionWait) context.Context {
	return context.WithValue(ctx, interactionWaitKey{}, wait)
}

type Executor struct {
	backend Backend
}

func New(backend Backend) (*Executor, error) {
	if backend == nil {
		return nil, errors.New("agent backend required")
	}
	value := reflect.ValueOf(backend)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		if value.IsNil() {
			return nil, errors.New("agent backend required")
		}
	}
	return &Executor{backend: backend}, nil
}

func pin(snapshot definitions.Snapshot) (definitions.Snapshot, orchestration.Definition, error) {
	data, err := json.Marshal(snapshot)
	if err != nil {
		return definitions.Snapshot{}, orchestration.Definition{}, err
	}
	var pinned definitions.Snapshot
	if err := json.Unmarshal(data, &pinned); err != nil {
		return definitions.Snapshot{}, orchestration.Definition{}, err
	}
	definition, err := pinned.Definition()
	if err != nil {
		return definitions.Snapshot{}, orchestration.Definition{}, err
	}
	if len(pinned.Agent.Schema) == 0 {
		return definitions.Snapshot{}, orchestration.Definition{}, errors.New("agent output schema required")
	}
	return pinned, definition, nil
}

func validName(name string) bool {
	return name != "" && len(name) <= 256 && utf8.ValidString(name) && !strings.ContainsAny(name, "\x00\r\n")
}

func (executor *Executor) Stage(snapshot definitions.Snapshot, inputs []messages.Contract, outputName string) (messages.Stage, messages.Validator, error) {
	pinned, _, err := pin(snapshot)
	if err != nil {
		return messages.Stage{}, nil, err
	}
	if !validName(outputName) || len(inputs) == 0 || len(inputs) > messages.MaxParts {
		return messages.Stage{}, nil, errors.New("invalid stage contracts")
	}
	contracts := append([]messages.Contract(nil), inputs...)
	seen := make(map[string]bool)
	for _, contract := range contracts {
		if !validName(contract.Name) || seen[contract.Name] {
			return messages.Stage{}, nil, errors.New("invalid or duplicate input contract")
		}
		seen[contract.Name] = true
		switch contract.Kind {
		case messages.Data:
			if !validName(contract.Schema) {
				return messages.Stage{}, nil, errors.New("input data schema required")
			}
		case messages.Text, messages.File:
			if contract.Schema != "" {
				return messages.Stage{}, nil, errors.New("non-data input cannot declare schema")
			}
		default:
			return messages.Stage{}, nil, errors.New("unknown input kind")
		}
	}
	stage := messages.Stage{
		Actor:   messages.Actor{Agent: pinned.Agent.Name, Revision: pinned.Agent.Digest},
		Inputs:  append([]messages.Contract(nil), contracts...),
		Outputs: []messages.Contract{{Name: outputName, Kind: messages.Data, Schema: pinned.Agent.Digest, Required: true}},
		Execute: func(ctx context.Context, delivery messages.Delivery) ([]messages.Part, error) {
			if err := validateInputs(delivery.Message.Parts, contracts); err != nil {
				return nil, err
			}
			output, err := executor.Run(ctx, pinned, delivery)
			if err != nil {
				return nil, err
			}
			return []messages.Part{{Name: outputName, Kind: messages.Data, Schema: pinned.Agent.Digest, Data: output}}, nil
		},
	}
	return stage, pinned.ValidateOutput, nil
}

func validateInputs(parts []messages.Part, contracts []messages.Contract) error {
	seen := make(map[string]bool)
	for _, part := range parts {
		matched := false
		for _, contract := range contracts {
			if part.Name == contract.Name && part.Kind == contract.Kind && part.Schema == contract.Schema {
				matched = true
				break
			}
		}
		if !matched {
			return errors.New("message does not match stage input contracts")
		}
		seen[part.Name] = true
	}
	for _, contract := range contracts {
		if contract.Required && !seen[contract.Name] {
			return errors.New("message missing required stage input")
		}
	}
	return nil
}

type backendError struct {
	operation string
	cause     error
}

func (err backendError) Error() string { return "agent " + err.operation + " failed" }
func (err backendError) Unwrap() error { return err.cause }

func boundedCleanup(ctx context.Context, operation func(context.Context) error, name string) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 45*time.Second)
	defer cancel()
	if err := operation(cleanupCtx); err != nil {
		return backendError{operation: name, cause: err}
	}
	return nil
}

func (executor *Executor) Run(ctx context.Context, snapshot definitions.Snapshot, delivery messages.Delivery) (json.RawMessage, error) {
	pinned, definition, err := pin(snapshot)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(delivery.ExecutionID) == "" {
		return nil, errors.New("delivery execution ID required")
	}
	if err := delivery.Message.Validate(); err != nil {
		return nil, err
	}
	if delivery.Message.To != pinned.Agent.Name {
		return nil, errors.New("delivery addressed to another agent")
	}
	if err := delivery.Reference.Validate(); err != nil {
		return nil, err
	}
	prompt, err := delivery.Prompt()
	if err != nil {
		return nil, err
	}
	request := orchestration.Request{Key: delivery.ExecutionID, Prompt: prompt, SubmittedAt: time.Now().UTC()}
	provisionCtx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	prepared, err := executor.backend.Provision(provisionCtx, definition, request)
	cancel()
	if err != nil {
		return nil, errors.Join(backendError{operation: "provision", cause: err}, boundedCleanup(ctx, func(cleanupCtx context.Context) error {
			return executor.backend.Cancel(cleanupCtx, definition, request)
		}, "cancel"))
	}
	var wait func(context.Context, string) error
	if interaction, _ := ctx.Value(interactionWaitKey{}).(InteractionWait); interaction != nil {
		wait = func(waitCtx context.Context, kind string) error {
			return interaction(waitCtx, prepared.SessionID, kind)
		}
	}
	executionCtx, cancel := context.WithTimeout(ctx, definition.Timeout)
	_, executeErr := executor.backend.Execute(executionCtx, definition, request, prepared, wait)
	cancel()
	artifactCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Minute)
	_, collectErr := executor.backend.Collect(artifactCtx, request, prepared)
	cancel()
	if collectErr != nil {
		var executionErr error
		if executeErr != nil {
			executionErr = backendError{operation: "execution", cause: executeErr}
		}
		return nil, errors.Join(executionErr, backendError{operation: "collection; lease retained", cause: collectErr})
	}
	var output json.RawMessage
	if executeErr != nil {
		err = backendError{operation: "execution", cause: executeErr}
	} else {
		output, err = executor.backend.ReadOutput(ctx, request, prepared)
		if err != nil {
			err = backendError{operation: "output read", cause: err}
		} else if validationErr := pinned.ValidateOutput(output); validationErr != nil {
			err = errors.New("agent output schema validation failed")
		}
	}
	err = errors.Join(err, boundedCleanup(ctx, func(cleanupCtx context.Context) error {
		return executor.backend.Cleanup(cleanupCtx, request, prepared)
	}, "cleanup"))
	if err != nil {
		return nil, err
	}
	return append(json.RawMessage(nil), output...), nil
}
