package hatchetbridge

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jake-molnia/agent-runtime/githubreview"
	"strings"
	"testing"
	"time"
)

func TestConfiguredHookReceivesCanonicalInitialInputBeforeEveryStage(t *testing.T) {
	fixture := newConfiguredTestFixture(t)
	var seen []ConfiguredStep
	denied := errors.New("disabled")
	workflow, err := RegisterConfiguredWorkflow(offlineLifecycleClient(t), fixture.backend, fixture.store, fixture.root, fixture.plan, ConfiguredHooks{BeforeStep: func(ctx context.Context, step ConfiguredStep) (context.Context, func(), error) {
		seen = append(seen, step)
		if step.Step == "verify" {
			return nil, nil, denied
		}
		return ctx, func() {}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	fixture.callbacks = dagTestCallbacks(t, workflow)
	fixture.ctx.input.Input = json.RawMessage(`{"canonical":"initial"}`)
	fixture.runStep(t, "resolve")
	fixture.runStep(t, "review")
	fixture.runStep(t, "adversarial")
	_, err = fixture.callbacks["verify"](fixture.ctx)
	if !errors.Is(err, denied) {
		t.Fatalf("guard error = %v", err)
	}
	if len(seen) != 3 || len(fixture.backend.calls) != 2 {
		t.Fatalf("hooks=%d agent calls=%d", len(seen), len(fixture.backend.calls))
	}
	for _, step := range seen {
		if string(step.Initial) != `{"canonical":"initial"}` || step.RunID != fixture.ctx.run || step.Workflow != fixture.plan.Workflow.Name {
			t.Fatalf("incorrect trusted context: %+v", step)
		}
	}
}

func TestReviewGuardRejectsStaleAndDisabledBeforeStage(t *testing.T) {
	input := reviewTestInput()
	api := &reviewTestAPI{current: reviewTestCurrent(input)}
	handler, _ := githubreview.NewHandler(api, reviewTestStore{})
	config := reviewTestPolicy()
	reload := func() (githubreview.Integration, error) { return config, nil }
	raw, _ := json.Marshal(struct {
		Review githubreview.Input `json:"review"`
	}{input})
	hook := ReviewBeforeStep(reload, handler)
	ctx, finish, err := hook.BeforeStep(context.Background(), ConfiguredStep{Initial: raw})
	if err != nil {
		t.Fatal(err)
	}
	finish()
	if ctx.Err() == nil {
		t.Fatal("cleanup did not release monitor")
	}
	api.current.HeadSHA = strings.Repeat("c", 40)
	if _, _, err = hook.BeforeStep(context.Background(), ConfiguredStep{Initial: raw}); !errors.Is(err, ErrReviewObsolete) {
		t.Fatalf("stale stage admitted: %v", err)
	}
	api.current = reviewTestCurrent(input)
	delete(config.Repositories, 2)
	if _, _, err = hook.BeforeStep(context.Background(), ConfiguredStep{Initial: raw}); !errors.Is(err, ErrReviewObsolete) {
		t.Fatalf("disabled stage admitted: %v", err)
	}
}
func TestReviewGuardCancelsActiveStageAndPreservesMovedBase(t *testing.T) {
	input := reviewTestInput()
	api := &reviewTestAPI{current: reviewTestCurrent(input)}
	handler, _ := githubreview.NewHandler(api, reviewTestStore{})
	config := reviewTestPolicy()
	reload := func() (githubreview.Integration, error) { return config, nil }
	api.current.BaseSHA = strings.Repeat("c", 40)
	if err := CheckReview(context.Background(), reload, handler, input); err != nil {
		t.Fatalf("base movement should not obsolete unchanged head: %v", err)
	}
	checks := make(chan struct{}, 1)
	release := make(chan struct{})
	ctx, finish, err := watchStage(context.Background(), time.Millisecond, func(ctx context.Context) error {
		select {
		case checks <- struct{}{}:
			return nil
		default:
			<-release
			return ErrReviewObsolete
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	defer finish()
	<-checks
	// The first check has completed. Release the next periodic check deterministically.
	checks <- struct{}{}
	close(release)
	select {
	case <-ctx.Done():
		if !errors.Is(context.Cause(ctx), ErrReviewObsolete) {
			t.Fatalf("cause %v", context.Cause(ctx))
		}
	case <-time.After(time.Second):
		t.Fatal("active execution was not cancelled")
	}
}
func reviewTestInput() githubreview.Input {
	return githubreview.Input{RepositoryID: 2, InstallationID: 1, Repository: "owner/repo", Number: 7, BaseSHA: strings.Repeat("a", 40), HeadSHA: strings.Repeat("b", 40)}
}
func reviewTestCurrent(input githubreview.Input) githubreview.PullRequest {
	return githubreview.PullRequest{RepositoryID: input.RepositoryID, InstallationID: input.InstallationID, Repository: input.Repository, Number: input.Number, BaseSHA: input.BaseSHA, HeadSHA: input.HeadSHA, State: "open"}
}
