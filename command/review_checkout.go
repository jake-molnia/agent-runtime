package command

import (
	"context"
	"errors"

	"github.com/jake-molnia/agent-runtime/agentexec"
	"github.com/jake-molnia/agent-runtime/githubreview"
	"github.com/jake-molnia/agent-runtime/hatchetbridge"
	"github.com/jake-molnia/agent-runtime/orchestration"
	"github.com/jake-molnia/agent-runtime/sandbox"
)

type checkoutIdentityKey struct{}
type checkoutTokens interface {
	CheckoutToken(context.Context, githubreview.Input) (string, error)
}

type reviewCheckoutBackend struct {
	agentexec.Backend
	tokens checkoutTokens
}

func reviewCheckoutStep(ctx context.Context, step hatchetbridge.ConfiguredStep) (context.Context, func(), error) {
	input, err := hatchetbridge.ReviewStepInput(step)
	if err != nil {
		return nil, nil, err
	}
	return context.WithValue(ctx, checkoutIdentityKey{}, input), func() {}, nil
}

func (backend reviewCheckoutBackend) Provision(ctx context.Context, definition orchestration.Definition, request orchestration.Request) (orchestration.Prepared, error) {
	input, ok := ctx.Value(checkoutIdentityKey{}).(githubreview.Input)
	if !ok {
		return orchestration.Prepared{}, errors.New("trusted checkout identity missing")
	}
	token, err := backend.tokens.CheckoutToken(ctx, input)
	if err != nil {
		return orchestration.Prepared{}, err
	}
	checkout, err := githubreview.PrepareRepository(input, token, definition.Directory)
	if err != nil {
		return orchestration.Prepared{}, err
	}
	prepare := definition.Prepare
	definition.Prepare = func(ctx context.Context, runtime *sandbox.Runtime, secrets map[string]string) error {
		if err := checkout(ctx, runtime, nil); err != nil {
			return err
		}
		if prepare != nil {
			return prepare(ctx, runtime, secrets)
		}
		return nil
	}
	return backend.Backend.Provision(ctx, definition, request)
}
