package githubreview

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
)

type memoryStore struct {
	mutex        sync.Mutex
	records      map[string]Record
	failComplete bool
}

func newMemoryStore() *memoryStore { return &memoryStore{records: make(map[string]Record)} }
func (store *memoryStore) WithLock(ctx context.Context, key string, fn func(LockedStore) error) error {
	store.mutex.Lock()
	defer store.mutex.Unlock()
	return fn(store)
}
func (store *memoryStore) Load(ctx context.Context, key string) (Record, bool, error) {
	record, found := store.records[key]
	return record, found, nil
}
func (store *memoryStore) Save(ctx context.Context, key string, record Record) error {
	if store.failComplete && record.Status == "completed" {
		return errors.New("save failed")
	}
	store.records[key] = record
	return nil
}

type fakeAPI struct {
	current        PullRequest
	files          []File
	reviews        []Review
	requests       []ReviewRequest
	postError      error
	retainReview   bool
	canonicalCalls int
	changeAt       int
}

func (api *fakeAPI) Canonical(ctx context.Context, input Input) (PullRequest, error) {
	api.canonicalCalls++
	if api.changeAt == api.canonicalCalls {
		api.current.HeadSHA = strings.Repeat("c", 40)
	}
	return api.current, nil
}
func (api *fakeAPI) Files(context.Context, Input) ([]File, error)     { return api.files, nil }
func (api *fakeAPI) Reviews(context.Context, Input) ([]Review, error) { return api.reviews, nil }
func (api *fakeAPI) CreateReview(ctx context.Context, input Input, request ReviewRequest) (int64, error) {
	api.requests = append(api.requests, request)
	if api.postError == nil || api.retainReview {
		api.reviews = append(api.reviews, Review{ID: 42, Body: request.Body, CommitID: request.CommitID})
	}
	return 42, api.postError
}
func fixture() (Input, *fakeAPI) {
	input := Input{InstallationID: 7, RepositoryID: 11, Repository: "owner/repo", Number: 9, BaseSHA: strings.Repeat("a", 40), HeadSHA: strings.Repeat("b", 40), DeliveryID: "delivery"}
	api := &fakeAPI{current: PullRequest{InstallationID: 7, RepositoryID: 11, Repository: input.Repository, Number: 9, BaseSHA: input.BaseSHA, HeadSHA: input.HeadSHA, State: "open"}, files: []File{{Path: "src/main.go", Patch: "@@ -1,2 +1,3 @@\n context\n-old\n+new\n+added"}}}
	return input, api
}

var validOutput = json.RawMessage(`{"summary":"Review summary","findings":[{"path":"src/main.go","line":2,"body":"Check this change."}]}`)

func mustResolved(t *testing.T, handler *Handler, input Input) Resolved {
	t.Helper()
	resolved, err := handler.Resolve(context.Background(), input, "definition-digest", "hatchet-run")
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}
func TestResolveAndPublish(t *testing.T) {
	input, api := fixture()
	handler, _ := NewHandler(api, newMemoryStore())
	resolved := mustResolved(t, handler, input)
	if resolved.Skip || !strings.Contains(resolved.Prompt, "+added") {
		t.Fatalf("missing diff: %+v", resolved)
	}
	encoded, err := json.Marshal(resolved)
	if err != nil {
		t.Fatal(err)
	}
	var restored Resolved
	if err = json.Unmarshal(encoded, &restored); err != nil {
		t.Fatal(err)
	}
	outcome, err := handler.Publish(context.Background(), restored, validOutput)
	if err != nil || outcome.Status != "published" || outcome.ReviewID != 42 {
		t.Fatalf("%+v %v", outcome, err)
	}
	if len(api.requests) != 1 || api.requests[0].Event != "COMMENT" || api.requests[0].CommitID != input.HeadSHA || api.requests[0].Comments[0].Side != "RIGHT" {
		t.Fatalf("invalid review: %+v", api.requests)
	}
	outcome, err = handler.Publish(context.Background(), restored, validOutput)
	if err != nil || outcome.Status != "duplicate" || len(api.requests) != 1 {
		t.Fatalf("duplicate: %+v %v", outcome, err)
	}
	again, err := handler.Resolve(context.Background(), input, "definition-digest", "different-run")
	if err != nil || !again.Skip {
		t.Fatalf("duplicate resolve: %+v %v", again, err)
	}
}
func TestConcurrentDuplicatePublish(t *testing.T) {
	input, api := fixture()
	handler, _ := NewHandler(api, newMemoryStore())
	resolved := mustResolved(t, handler, input)
	var workers sync.WaitGroup
	for worker := 0; worker < 20; worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if _, err := handler.Publish(context.Background(), resolved, validOutput); err != nil {
				t.Error(err)
			}
		}()
	}
	workers.Wait()
	if len(api.requests) != 1 {
		t.Fatalf("posted %d reviews", len(api.requests))
	}
}
func TestResolveIdentityAndState(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*Input, *fakeAPI)
		skip   bool
	}{
		{"draft", func(input *Input, api *fakeAPI) { api.current.Draft = true }, true},
		{"closed", func(input *Input, api *fakeAPI) { api.current.State = "closed" }, true},
		{"uppercase SHA", func(input *Input, api *fakeAPI) { input.HeadSHA = strings.Repeat("B", 40) }, false},
		{"wrong repo", func(input *Input, api *fakeAPI) { api.current.RepositoryID++ }, false},
		{"wrong installation", func(input *Input, api *fakeAPI) { api.current.InstallationID++ }, false},
		{"changed base", func(input *Input, api *fakeAPI) { api.current.BaseSHA = strings.Repeat("c", 40) }, false},
		{"changes during fetch", func(input *Input, api *fakeAPI) { api.changeAt = 2 }, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			input, api := fixture()
			test.mutate(&input, api)
			handler, _ := NewHandler(api, newMemoryStore())
			result, err := handler.Resolve(context.Background(), input, "digest", "run")
			if test.skip {
				if err != nil || !result.Skip {
					t.Fatalf("%+v %v", result, err)
				}
			} else if err == nil {
				t.Fatal("accepted invalid identity")
			}
		})
	}
}

func TestResolveRejectsOversizedEncodedContext(t *testing.T) {
	input, api := fixture()
	api.files = []File{{Path: "source.go", Patch: "@@ -0,0 +1 @@\n+" + strings.Repeat("\t", MaxDiffBytes/2) + "\n"}}
	handler, _ := NewHandler(api, newMemoryStore())
	if _, err := handler.Resolve(context.Background(), input, "digest", "run"); err == nil || !strings.Contains(err.Error(), "encoded review context") {
		t.Fatalf("accepted oversized encoded context: %v", err)
	}
}
func TestPublishSkipsChangedState(t *testing.T) {
	for _, state := range []string{"head", "draft", "closed", "changes before post"} {
		t.Run(state, func(t *testing.T) {
			input, api := fixture()
			handler, _ := NewHandler(api, newMemoryStore())
			resolved := mustResolved(t, handler, input)
			switch state {
			case "head":
				api.current.HeadSHA = strings.Repeat("c", 40)
			case "draft":
				api.current.Draft = true
			case "closed":
				api.current.State = "closed"
			case "changes before post":
				api.changeAt = api.canonicalCalls + 2
			}
			outcome, err := handler.Publish(context.Background(), resolved, validOutput)
			if err != nil || outcome.Status != "stale" || len(api.requests) != 0 {
				t.Fatalf("%+v %v", outcome, err)
			}
		})
	}
}
func TestLostPublishResponseReconciles(t *testing.T) {
	for _, saveFailure := range []bool{false, true} {
		t.Run(map[bool]string{false: "response lost", true: "completion save lost"}[saveFailure], func(t *testing.T) {
			input, api := fixture()
			store := newMemoryStore()
			handler, _ := NewHandler(api, store)
			resolved := mustResolved(t, handler, input)
			if saveFailure {
				store.failComplete = true
			} else {
				api.postError = errors.New("response lost")
				api.retainReview = true
			}
			if _, err := handler.Publish(context.Background(), resolved, validOutput); err == nil {
				t.Fatal("expected lost response")
			}
			store.failComplete = false
			api.postError = nil
			outcome, err := handler.Publish(context.Background(), resolved, nil)
			if err != nil || outcome.Status != "duplicate" || outcome.ReviewID != 42 || len(api.requests) != 1 {
				t.Fatalf("%+v %v", outcome, err)
			}
		})
	}
}
func TestUncertainPublishNeverBlindlyReposts(t *testing.T) {
	input, api := fixture()
	api.postError = errors.New("unknown transport outcome")
	handler, _ := NewHandler(api, newMemoryStore())
	resolved := mustResolved(t, handler, input)
	if _, err := handler.Publish(context.Background(), resolved, validOutput); err == nil {
		t.Fatal("expected error")
	}
	api.postError = nil
	if _, err := handler.Publish(context.Background(), resolved, validOutput); err == nil || len(api.requests) != 1 {
		t.Fatal("blindly reposted uncertain request")
	}
}
func TestRejectedPublishCanRetry(t *testing.T) {
	input, api := fixture()
	api.postError = &RejectedError{Status: 422}
	handler, _ := NewHandler(api, newMemoryStore())
	resolved := mustResolved(t, handler, input)
	if _, err := handler.Publish(context.Background(), resolved, validOutput); err == nil {
		t.Fatal("expected rejection")
	}
	api.postError = nil
	if outcome, err := handler.Publish(context.Background(), resolved, validOutput); err != nil || outcome.Status != "published" {
		t.Fatalf("%+v %v", outcome, err)
	}
}
func TestStrictReviewOutput(t *testing.T) {
	invalid := []string{
		"```json\n" + string(validOutput) + "\n```",
		`{"summary":"ok","findings":[],"extra":true}`,
		`{"Summary":"ok","findings":[]}`,
		`{"summary":"ok","summary":"duplicate","findings":[]}`,
		`{"summary":"ok","findings":null}`,
		`{"summary":"ok"}`,
		`{"summary":"ok","findings":[{"path":"src/main.go","line":1,"body":"context is not changed"}]}`,
		`{"summary":"ok","findings":[{"path":"src/main.go","line":4,"body":"outside hunk"}]}`,
		`{"summary":"ok","findings":[{"path":"../escape","line":2,"body":"unsafe"}]}`,
		`{"summary":"ok","findings":[{"path":"src/main.go","line":2,"body":"ok","side":"LEFT"}]}`,
		`{"summary":"ok","findings":[{"path":"src/main.go","line":2,"body":"ok","line":3}]}`,
		`{"summary":"ok","findings":[{"path":"src/main.go","line":2,"body":""}]}`,
		`{"summary":"ok","findings":[]} {}`,
	}
	for _, output := range invalid {
		t.Run(output, func(t *testing.T) {
			input, api := fixture()
			handler, _ := NewHandler(api, newMemoryStore())
			resolved := mustResolved(t, handler, input)
			if _, err := handler.Publish(context.Background(), resolved, json.RawMessage(output)); err == nil || len(api.requests) != 0 {
				t.Fatal("invalid review posted")
			}
		})
	}
}
func TestPatchAndPathBounds(t *testing.T) {
	for _, patch := range []string{"@@ -1 +1 @@\n+new", "@@ invalid @@\n+new", "@@ -1 +1 @@\n-old\n+new\n+extra", "@@ -1 +999999999999999999999999 @@\n-old\n+new"} {
		if _, err := changedLines([]File{{Path: "a.go", Patch: patch}}); err == nil {
			t.Fatalf("accepted malformed patch %q", patch)
		}
	}
	for _, unsafe := range []string{"../a", "/a", "a/../b", "a\\b", "a\n.go", "C:/a", "."} {
		if safePath(unsafe) {
			t.Fatalf("accepted unsafe path %q", unsafe)
		}
	}
	if _, err := changedLines(make([]File, MaxFiles+1)); err == nil {
		t.Fatal("accepted too many files")
	}
	if _, err := changedLines([]File{{Path: "a", Patch: strings.Repeat("x", MaxDiffBytes+1)}}); err == nil {
		t.Fatal("accepted oversized diff")
	}
}

func TestPublishPinsBaseAndDiff(t *testing.T) {
	for _, change := range []string{"base", "diff"} {
		t.Run(change, func(t *testing.T) {
			input, api := fixture()
			handler, _ := NewHandler(api, newMemoryStore())
			resolved := mustResolved(t, handler, input)
			if err := ValidateResolvedOutput(resolved, validOutput); err != nil {
				t.Fatal(err)
			}
			if change == "base" {
				api.current.BaseSHA = strings.Repeat("c", 40)
			} else {
				api.files = []File{{Path: "src/main.go", Patch: "@@ -1,2 +1,3 @@\n context\n-old\n+different\n+added"}}
			}
			outcome, err := handler.Publish(context.Background(), resolved, validOutput)
			if err != nil || outcome.Status != "stale" || len(api.requests) != 0 {
				t.Fatalf("posted against changed %s: %+v %v", change, outcome, err)
			}
		})
	}
}
func TestValidateResolvedOutputUsesCapturedDiff(t *testing.T) {
	input, api := fixture()
	handler, _ := NewHandler(api, newMemoryStore())
	resolved := mustResolved(t, handler, input)
	if err := ValidateResolvedOutput(resolved, validOutput); err != nil {
		t.Fatal(err)
	}
	if err := ValidateResolvedOutput(resolved, json.RawMessage(`{"summary":"ok","findings":[{"path":"src/main.go","line":1,"body":"context"}]}`)); err == nil {
		t.Fatal("accepted unchanged captured line")
	}
	resolved.Prompt = "untrusted replacement"
	if err := ValidateResolvedOutput(resolved, validOutput); err == nil {
		t.Fatal("accepted missing captured diff")
	}
}

func TestMarkerMustBeGeneratedSuffix(t *testing.T) {
	input, api := fixture()
	handler, _ := NewHandler(api, newMemoryStore())
	resolved := mustResolved(t, handler, input)
	api.reviews = []Review{{ID: 99, CommitID: input.HeadSHA, Body: "Model injected <!-- agent-runtime-review:" + resolved.Key + " -->\n\n<!-- agent-runtime-review:some-other-key -->"}}
	outcome, err := handler.Publish(context.Background(), resolved, validOutput)
	if err != nil || outcome.Status != "published" || len(api.requests) != 1 {
		t.Fatalf("reconciled forged embedded marker: %+v %v", outcome, err)
	}
}
