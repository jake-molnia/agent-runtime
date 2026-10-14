package hatchetbridge

import (
	"context"
	"encoding/json"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/google/cel-go/cel"
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
	workflow, err := RegisterReview(offlineLifecycleClient(t), config, func() (githubreview.Integration, error) { return config, nil }, &githubreview.Handler{}, fixture.root, fixture.plan, fixture.backend, fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	declaration, regular, durable, _ := workflow.Dump()
	if len(regular) != 3 || len(durable) != len(fixture.plan.Workflow.Steps) || len(declaration.EventTriggers) != 0 {
		t.Fatalf("incorrect review graph %+v", declaration)
	}
	if len(declaration.ConcurrencyArr) != 1 || declaration.ConcurrencyArr[0].GetLimitStrategy().String() != "GROUP_ROUND_ROBIN" || declaration.Name != "_"+config.Name {
		t.Fatalf("invalid admission %+v", declaration)
	}
	revision, err := ReviewRevision(config, fixture.plan.Digest)
	if err != nil {
		t.Fatal(err)
	}
	taskName := func(role string) string { return ConfiguredWorkflowName(role, revision) }
	parents := map[string][]string{
		taskName("resolve"):           nil,
		taskName("agent-review"):      {taskName("resolve")},
		taskName("agent-adversarial"): {taskName("resolve")},
		taskName("agent-verify"):      {taskName("resolve"), taskName("agent-review"), taskName("agent-adversarial")},
		taskName("reduce"):            {taskName("resolve"), taskName("agent-review"), taskName("agent-adversarial"), taskName("agent-verify")},
		taskName("publish"):           {taskName("reduce")},
	}
	for _, task := range declaration.Tasks {
		want, exists := parents[task.ReadableId]
		actual := append([]string(nil), task.Parents...)
		slices.Sort(actual)
		slices.Sort(want)
		if !exists || !reflect.DeepEqual(actual, want) {
			t.Errorf("%s parents = %v, want %v", task.ReadableId, actual, want)
		}
		if task.IsDurable != strings.HasPrefix(task.ReadableId, "agent-") {
			t.Errorf("only configured agent stages may consume durable slots: %+v", task)
		}
	}
	raw, err := json.Marshal(declaration)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "delivery_id") || !strings.Contains(string(raw), "2592000") {
		t.Fatalf("missing 30-day idempotency %s", raw)
	}
	config.CandidateSteps = []string{"missing"}
	if _, err = RegisterReview(offlineLifecycleClient(t), config, func() (githubreview.Integration, error) { return config, nil }, &githubreview.Handler{}, fixture.root, fixture.plan, fixture.backend, fixture.store); err == nil {
		t.Fatal("missing evidence step accepted")
	}
}

type reviewTaskContext struct {
	configuredTestContext
	raw  json.RawMessage
	base context.Context
}

func (ctx reviewTaskContext) GetContext() context.Context {
	if ctx.base != nil {
		return ctx.base
	}
	return context.Background()
}

func (ctx reviewTaskContext) WorkflowInput(target any) error { return json.Unmarshal(ctx.raw, target) }
func TestReviewPinsResolutionThroughConfiguredGraph(t *testing.T) {
	for _, scenario := range []string{"dry-run", "publish", "skip"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := newConfiguredTestFixture(t)
			fixture.backend.outputs["review"] = json.RawMessage(`{"summary":"Review","findings":[{"priority":"P2","path":"a.go","line":1,"title":"Fix","explanation":"This breaks the caller."}],"limitations":""}`)
			fixture.backend.outputs["adversarial"] = json.RawMessage(`{"summary":"Checked","findings":[],"limitations":""}`)
			fixture.backend.outputs["verify"] = json.RawMessage(`{"assessment":"Review complete","findings":[{"sources":[0],"title":"Fix","explanation":"This breaks the caller."}]}`)
			config := reviewTestPolicy()
			config.Workflow = fixture.plan.Workflow.Name
			input := githubreview.Input{RepositoryID: 2, InstallationID: 1, Repository: "owner/repo", Number: 7, BaseSHA: strings.Repeat("a", 40), HeadSHA: strings.Repeat("b", 40), DeliveryID: "request-1"}
			api := &reviewTestAPI{current: githubreview.PullRequest{InstallationID: 1, RepositoryID: 2, Repository: "owner/repo", Number: 7, BaseSHA: input.BaseSHA, HeadSHA: input.HeadSHA, State: "open", Draft: scenario == "skip"}}
			handler, _ := githubreview.NewHandler(api, reviewTestStore{})
			workflow, err := RegisterReview(offlineLifecycleClient(t), config, func() (githubreview.Integration, error) { return config, nil }, handler, fixture.root, fixture.plan, fixture.backend, fixture.store)
			if err != nil {
				t.Fatal(err)
			}
			revision, err := ReviewRevision(config, fixture.plan.Digest)
			if err != nil {
				t.Fatal(err)
			}
			taskName := func(role string) string { return ConfiguredWorkflowName(role, revision) }
			callbacks := dagTestCallbacks(t, workflow)
			raw, _ := json.Marshal(githubreview.Request{Review: input, Publish: scenario == "publish"})
			ctx := reviewTaskContext{configuredTestContext: configuredTestContext{outputs: map[string]any{}, run: "review-run"}, raw: raw}
			run := func(role string) any {
				t.Helper()
				output, err := callbacks[taskName(role)](ctx)
				if err != nil {
					t.Fatalf("%s: %v", role, err)
				}
				ctx.outputs[taskName(role)] = output
				return output
			}
			result := run("resolve").(ReviewResult)
			if result.Configuration != revision || result.State.Digest != fixture.plan.Digest || result.State.Skip != (scenario == "skip") || result.Publish != (scenario == "publish") {
				t.Fatalf("incorrect pinned state %+v", result)
			}
			changed := result
			changed.Configuration = "different-registration"
			ctx.outputs[taskName("resolve")] = changed
			if _, err = callbacks[taskName("agent-review")](ctx); err == nil || !strings.Contains(err.Error(), "pinned review configuration changed") {
				t.Fatalf("accepted replay under changed configuration: %v", err)
			}
			ctx.outputs[taskName("resolve")] = result
			run("agent-review")
			run("agent-adversarial")
			run("agent-verify")
			if scenario != "skip" {
				original := ctx.outputs[taskName("agent-review")]
				ctx.outputs[taskName("agent-review")] = ctx.outputs[taskName("agent-adversarial")]
				if _, err = callbacks[taskName("reduce")](ctx); err == nil || !strings.Contains(err.Error(), "step output identity mismatch") {
					t.Fatalf("accepted another stage's message: %v", err)
				}
				ctx.outputs[taskName("agent-review")] = original
			}
			reduced := run("reduce").(ReviewResult)
			if scenario != "skip" {
				configuredTestJSON(t, reduced.Output, json.RawMessage(`{"summary":"Review complete","findings":[{"path":"a.go","line":1,"body":"**[P2] Fix**\n\nThis breaks the caller."}]}`))
			}
			outcome := run("publish").(githubreview.Outcome)
			want, calls, publications := "dry-run", 3, 0
			if scenario == "publish" {
				want, publications = "published", 1
			} else if scenario == "skip" {
				want, calls = "skipped", 0
			}
			if outcome.Status != want || len(fixture.backend.calls) != calls || api.created != publications {
				t.Fatalf("outcome %+v, agent calls %d, publications %d", outcome, len(fixture.backend.calls), api.created)
			}
			for _, call := range fixture.backend.calls {
				if call.message.ContextID != ctx.run || call.message.TaskID != "review" && call.message.TaskID != "adversarial" && call.message.TaskID != "verify" {
					t.Fatalf("logical message identity changed: %+v", call.message)
				}
			}
		})
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
