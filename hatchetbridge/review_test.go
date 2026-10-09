package hatchetbridge

import (
	"context"
	"encoding/json"
	"github.com/google/cel-go/cel"
	"strings"
	"testing"

	"github.com/jake-molnia/agent-runtime/githubreview"
)

type reviewTestAPI struct {
	current githubreview.PullRequest
	created int
	reviews []githubreview.Review
}

func (a *reviewTestAPI) Canonical(context.Context, githubreview.Input) (githubreview.PullRequest, error) {
	return a.current, nil
}
func (a *reviewTestAPI) Files(context.Context, githubreview.Input) ([]githubreview.File, error) {
	return []githubreview.File{{Path: "a.go", Patch: "@@ -1 +1 @@\n-old\n+new\n"}}, nil
}
func (a *reviewTestAPI) Reviews(context.Context, githubreview.Input) ([]githubreview.Review, error) {
	return a.reviews, nil
}
func (a *reviewTestAPI) CreateReview(_ context.Context, _ githubreview.Input, r githubreview.ReviewRequest) (int64, error) {
	a.created++
	a.reviews = append(a.reviews, githubreview.Review{ID: 1, CommitID: r.CommitID, Body: r.Body})
	return 1, nil
}

type reviewTestStore map[string]githubreview.Record

func (s reviewTestStore) WithLock(_ context.Context, _ string, fn func(githubreview.LockedStore) error) error {
	return fn(s)
}
func (s reviewTestStore) Load(_ context.Context, k string) (githubreview.Record, bool, error) {
	r, ok := s[k]
	return r, ok, nil
}
func (s reviewTestStore) Save(_ context.Context, k string, r githubreview.Record) error {
	s[k] = r
	return nil
}
func reviewTestPolicy() githubreview.Integration {
	return githubreview.Integration{Version: 1, Name: "pr-adapter", Workflow: "review-change", CandidateSteps: []string{"review", "adversarial"}, WriteupStep: "verify", Actions: []string{"opened"}, Repositories: map[int64]githubreview.RepositoryPolicy{2: {Name: "owner/repo", InstallationID: 1, Publish: true}}}
}
func TestReviewPublicationPolicyAndReplay(t *testing.T) {
	for _, scenario := range []string{"publish", "dry-run", "revoked", "disabled", "stale", "cancelled"} {
		t.Run(scenario, func(t *testing.T) {
			input := githubreview.Input{RepositoryID: 2, InstallationID: 1, Repository: "owner/repo", Number: 7, BaseSHA: strings.Repeat("a", 40), HeadSHA: strings.Repeat("b", 40)}
			api := &reviewTestAPI{current: githubreview.PullRequest{InstallationID: 1, RepositoryID: 2, Repository: "owner/repo", Number: 7, BaseSHA: input.BaseSHA, HeadSHA: input.HeadSHA, State: "open"}}
			handler, _ := githubreview.NewHandler(api, reviewTestStore{})
			resolved, err := handler.Resolve(context.Background(), input, "digest", "run")
			if err != nil {
				t.Fatal(err)
			}
			config := reviewTestPolicy()
			result := ReviewResult{Resolved: resolved, Publish: true, Output: json.RawMessage(`{"summary":"Review","findings":[{"path":"a.go","line":1,"body":"Fix"}]}`)}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			expected := scenario
			switch scenario {
			case "publish":
				expected = "published"
			case "dry-run":
				result.Publish = false
			case "revoked":
				delete(config.Repositories, 2)
				expected = "disabled"
			case "disabled":
				p := config.Repositories[2]
				p.Publish = false
				config.Repositories[2] = p
				expected = "dry-run"
			case "stale":
				api.current.HeadSHA = strings.Repeat("c", 40)
			case "cancelled":
				cancel()
			}
			reload := func() (githubreview.Integration, error) { return config, nil }
			outcome, err := PublishReview(ctx, reload, handler, result)
			if scenario == "cancelled" {
				if err == nil || api.created != 0 {
					t.Fatalf("cancelled %v %d", err, api.created)
				}
				return
			}
			if err != nil || outcome.Status != expected {
				t.Fatalf("got %+v %v want %s", outcome, err, expected)
			}
			if scenario == "publish" {
				again, err := PublishReview(ctx, reload, handler, result)
				if err != nil || again.Status != "duplicate" || api.created != 1 {
					t.Fatalf("replay %+v %v creates %d", again, err, api.created)
				}
			} else if api.created != 0 {
				t.Fatal("unexpected publication")
			}
		})
	}
}
func TestReviewRegistration(t *testing.T) {
	fixture := newConfiguredTestFixture(t)
	config := reviewTestPolicy()
	config.Workflow = fixture.plan.Workflow.Name
	workflow, err := RegisterReview(offlineLifecycleClient(t), config, func() (githubreview.Integration, error) { return config, nil }, &githubreview.Handler{}, fixture.workflow, fixture.plan, fixture.backend, fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	declaration, regular, durable, _ := workflow.Dump()
	if len(regular) != 2 || len(durable) != 1 || len(declaration.ConcurrencyArr) != 1 || len(declaration.EventTriggers) != 0 {
		t.Fatalf("incorrect adapter graph %+v", declaration)
	}
	raw, err := json.Marshal(declaration)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "delivery_id") || !strings.Contains(string(raw), "2592000") {
		t.Fatalf("missing 30-day idempotency %s", raw)
	}
	config.CandidateSteps = []string{"missing"}
	if _, err = RegisterReview(offlineLifecycleClient(t), config, func() (githubreview.Integration, error) { return config, nil }, &githubreview.Handler{}, fixture.workflow, fixture.plan, fixture.backend, fixture.store); err == nil {
		t.Fatal("missing evidence step accepted")
	}
}

type reviewTaskContext struct {
	configuredTestContext
	raw json.RawMessage
}

func (ctx reviewTaskContext) WorkflowInput(target any) error { return json.Unmarshal(ctx.raw, target) }
func TestReviewPinsResolutionBeforeDurableChild(t *testing.T) {
	fixture := newConfiguredTestFixture(t)
	config := reviewTestPolicy()
	config.Workflow = fixture.plan.Workflow.Name
	input := githubreview.Input{RepositoryID: 2, InstallationID: 1, Repository: "owner/repo", Number: 7, BaseSHA: strings.Repeat("a", 40), HeadSHA: strings.Repeat("b", 40), DeliveryID: "request-1"}
	api := &reviewTestAPI{current: githubreview.PullRequest{InstallationID: 1, RepositoryID: 2, Repository: "owner/repo", Number: 7, BaseSHA: input.BaseSHA, HeadSHA: input.HeadSHA, State: "open"}}
	handler, _ := githubreview.NewHandler(api, reviewTestStore{})
	workflow, err := RegisterReview(offlineLifecycleClient(t), config, func() (githubreview.Integration, error) { return config, nil }, handler, fixture.workflow, fixture.plan, fixture.backend, fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	callbacks := dagTestCallbacks(t, workflow)
	raw, _ := json.Marshal(githubreview.Request{Review: input})
	ctx := reviewTaskContext{configuredTestContext: configuredTestContext{outputs: map[string]any{}, run: "outer-run"}, raw: raw}
	output, err := callbacks["resolve"](ctx)
	if err != nil {
		t.Fatal(err)
	}
	result := output.(ReviewResult)
	if result.Publish || result.Configuration == "" || result.Resolved.Digest != fixture.plan.Digest {
		t.Fatalf("incorrect pinned state %+v", result)
	}
	result.Configuration = "different-registration"
	ctx.outputs["resolve"] = result
	if _, err = callbacks["review"](ctx); err == nil || !strings.Contains(err.Error(), "pinned review configuration changed") {
		t.Fatalf("accepted replay under changed configuration: %v", err)
	}
	api.current.Draft = true
	output, err = callbacks["resolve"](ctx)
	if err != nil || !output.(ReviewResult).Resolved.Skip {
		t.Fatalf("draft not skipped: %+v %v", output, err)
	}
	ctx.outputs["resolve"] = output
	if _, err = callbacks["review"](ctx); err != nil {
		t.Fatalf("skipped run started child: %v", err)
	}
}

func TestReviewAdmissionIdentities(t *testing.T) {
	env, err := cel.NewEnv(cel.Variable("input", cel.DynType))
	if err != nil {
		t.Fatal(err)
	}
	evaluate := func(expression string, input any) string {
		t.Helper()
		ast, issues := env.Compile(expression)
		if issues.Err() != nil {
			t.Fatal(issues.Err())
		}
		program, err := env.Program(ast)
		if err != nil {
			t.Fatal(err)
		}
		value, _, err := program.Eval(map[string]any{"input": input})
		if err != nil {
			t.Fatal(err)
		}
		return value.Value().(string)
	}
	native := map[string]any{"repository": map[string]any{"id": int64(2)}, "number": int64(7), "pull_request": map[string]any{"head": map[string]any{"sha": "head"}}}
	webhook := map[string]any{"review": map[string]any{"repository_id": int64(2), "number": int64(7), "delivery_id": "github:2:7:head"}}
	if evaluate(reviewIdempotencyExpression, native) != evaluate(reviewIdempotencyExpression, webhook) {
		t.Fatal("native and HMAC webhook use different dedupe keys")
	}
	if evaluate(reviewConcurrencyExpression, native) != evaluate(reviewConcurrencyExpression, webhook) {
		t.Fatal("ingress paths use different PR concurrency groups")
	}
	manual := map[string]any{"review": map[string]any{"repository_id": int64(2), "number": int64(7), "delivery_id": "manual-1"}}
	if evaluate(reviewIdempotencyExpression, manual) == evaluate(reviewIdempotencyExpression, native) {
		t.Fatal("manual request collides with automatic review")
	}
}
