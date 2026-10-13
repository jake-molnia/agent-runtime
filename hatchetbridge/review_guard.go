package hatchetbridge

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jake-molnia/agent-runtime/githubreview"
)

var ErrReviewObsolete = errors.New("review is stale or disabled")

// ReviewStepInput reads only the trusted original caller input. Later model
// stages can change their own data, but cannot select another checkout or PR.
func ReviewStepInput(step ConfiguredStep) (githubreview.Input, error) {
	var input struct {
		Review githubreview.Input `json:"review"`
	}
	if err := json.Unmarshal(step.Initial, &input); err != nil {
		return input.Review, err
	}
	if input.Review.RepositoryID == 0 || input.Review.Number == 0 || input.Review.HeadSHA == "" {
		return input.Review, errors.New("trusted review input missing")
	}
	return input.Review, nil
}

func CheckReview(ctx context.Context, reload func() (githubreview.Integration, error), handler *githubreview.Handler, input githubreview.Input) error {
	config, err := reload()
	if err != nil {
		return err
	}
	if _, err = config.Authorize(input); err != nil {
		return ErrReviewObsolete
	}
	current, err := handler.Client.Canonical(ctx, input)
	if err != nil {
		return err
	}
	if current.InstallationID != input.InstallationID || current.RepositoryID != input.RepositoryID || current.Repository != input.Repository || current.Number != input.Number {
		return errors.New("canonical pull request identity mismatch")
	}
	if current.Draft || current.State != "open" || current.HeadSHA != input.HeadSHA {
		return ErrReviewObsolete
	}
	return nil
}

func ReviewBeforeStep(reload func() (githubreview.Integration, error), handler *githubreview.Handler) ConfiguredHooks {
	return ConfiguredHooks{BeforeStep: func(ctx context.Context, step ConfiguredStep) (context.Context, func(), error) {
		input, err := ReviewStepInput(step)
		if err != nil {
			return nil, nil, err
		}
		return watchStage(ctx, 15*time.Second, func(ctx context.Context) error { return CheckReview(ctx, reload, handler, input) })
	}}
}

func watchStage(parent context.Context, interval time.Duration, check func(context.Context) error) (context.Context, func(), error) {
	checkNow := func(ctx context.Context) error {
		bounded, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return check(bounded)
	}
	if err := checkNow(parent); err != nil {
		return nil, nil, err
	}
	ctx, cancel := context.WithCancelCause(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := checkNow(ctx); err != nil {
					cancel(err)
					return
				}
			}
		}
	}()
	return ctx, func() { cancel(context.Canceled); <-done }, nil
}
