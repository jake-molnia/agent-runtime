package githubreview

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestParityLocationsAndBaseRefresh(t *testing.T) {
	input, api := fixture()
	api.current.BaseSHA = strings.Repeat("c", 40)
	handler, _ := NewHandler(api, newMemoryStore())
	resolved := mustResolved(t, handler, input)
	if resolved.Input.BaseSHA != api.current.BaseSHA {
		t.Fatal("base was not refreshed")
	}
	api.current.BaseSHA = strings.Repeat("d", 40)
	output := json.RawMessage(`{"summary":"ok","findings":[{"path":"src/main.go","line":1,"body":"context"},{"path":"unchanged.go","line":42,"body":"supporting file"}]}`)
	outcome, err := handler.Publish(context.Background(), resolved, output)
	if err != nil || outcome.Status != "published" {
		t.Fatalf("%+v %v", outcome, err)
	}
	if len(api.requests[0].Comments) != 1 || api.requests[0].Comments[0].Line != 1 || !strings.Contains(api.requests[0].Body, "/blob/"+input.HeadSHA+"/unchanged.go#L42") {
		t.Fatalf("bad placement: %+v", api.requests)
	}
}
func TestParityLegacyAndDigestIndependentCompletion(t *testing.T) {
	input, api := fixture()
	handler, _ := NewHandler(api, newMemoryStore())
	resolved := mustResolved(t, handler, input)
	api.reviews = []Review{{ID: 99, CommitID: input.HeadSHA, Body: "<!-- homelab-pr-review:v1 -->\n<!-- head:" + input.HeadSHA + " -->\n\nCompleted"}}
	outcome, err := handler.Publish(context.Background(), resolved, validOutput)
	if err != nil || outcome.Status != "duplicate" || len(api.requests) != 0 {
		t.Fatalf("%+v %v", outcome, err)
	}
	again, err := handler.Resolve(context.Background(), input, "new-digest", "new-run")
	if err != nil || !again.Skip {
		t.Fatalf("completion not shared: %+v %v", again, err)
	}
}
func TestParityUnicodeChunkedPublication(t *testing.T) {
	input, api := fixture()
	handler, _ := NewHandler(api, newMemoryStore())
	resolved := mustResolved(t, handler, input)
	findings := []Finding{}
	for i := 0; i < 30; i++ {
		findings = append(findings, Finding{Path: "outside.go", Line: i + 1, Body: strings.Repeat("界", 2000)})
	}
	output, _ := json.Marshal(struct {
		Summary  string    `json:"summary"`
		Findings []Finding `json:"findings"`
	}{"summary", findings})
	outcome, err := handler.Publish(context.Background(), resolved, output)
	if err != nil || outcome.Status != "published" {
		t.Fatalf("%+v %v", outcome, err)
	}
	if len(api.requests) < 2 {
		t.Fatal("expected chunks")
	}
	for _, r := range api.requests {
		if len(r.Body) > 60000 || !utf8.ValidString(r.Body) {
			t.Fatal("invalid chunk")
		}
	}
	before := len(api.requests)
	outcome, err = handler.Publish(context.Background(), resolved, output)
	if err != nil || outcome.Status != "duplicate" || before != len(api.requests) {
		t.Fatalf("duplicate chunks: %+v %v", outcome, err)
	}
}

func TestChunkRetryResumesAfterLostMiddleResponse(t *testing.T) {
	input, api := fixture()
	store := newMemoryStore()
	handler, _ := NewHandler(api, store)
	resolved := mustResolved(t, handler, input)
	findings := []Finding{}
	for i := 0; i < 30; i++ {
		findings = append(findings, Finding{Path: "outside.go", Line: i + 1, Body: strings.Repeat("界", 2000)})
	}
	output, _ := json.Marshal(struct {
		Summary  string    `json:"summary"`
		Findings []Finding `json:"findings"`
	}{"summary", findings})
	api.failPostAt = 2
	api.postError = errors.New("lost middle response")
	api.retainReview = true
	if _, err := handler.Publish(context.Background(), resolved, output); err == nil {
		t.Fatal("expected lost response")
	}
	if len(api.requests) != 2 {
		t.Fatalf("requests %d", len(api.requests))
	}
	api.postError = nil
	// Retry uses the durable plan even if placement patches and model output change.
	api.files = []File{{Path: "outside.go", Patch: "@@ -0,0 +1 @@\n+x"}}
	outcome, err := handler.Publish(context.Background(), resolved, nil)
	if err != nil || outcome.Status != "published" {
		t.Fatalf("%+v %v", outcome, err)
	}
	seen := map[string]bool{}
	for _, request := range api.requests {
		if seen[request.Body] {
			t.Fatal("reposted a completed chunk")
		}
		seen[request.Body] = true
	}
	if len(api.requests) < 3 {
		t.Fatal("did not finish chunks")
	}
}
func TestTruncatedPatchFallsBack(t *testing.T) {
	input, api := fixture()
	api.files[0].Patch = "@@ -1,2 +1,3 @@\n context\n+truncated"
	handler, _ := NewHandler(api, newMemoryStore())
	resolved := mustResolved(t, handler, input)
	outcome, err := handler.Publish(context.Background(), resolved, validOutput)
	if err != nil || outcome.Status != "published" || len(api.requests[0].Comments) != 0 || !strings.Contains(api.requests[0].Body, "src/main.go#L2") {
		t.Fatalf("%+v %v %+v", outcome, err, api.requests)
	}
}
func TestMaximumUnicodeCandidateWriteup(t *testing.T) {
	input, api := fixture()
	handler, _ := NewHandler(api, newMemoryStore())
	resolved := mustResolved(t, handler, input)
	title := strings.Repeat("😀", 200)
	explanation := strings.Repeat("😀", 1800)
	report, _ := json.Marshal(CandidateReview{Summary: "summary", Limitations: "none", Findings: []Candidate{{Priority: "P1", Path: "unchanged.go", Line: 3, Title: title, Explanation: explanation}}})
	wording, _ := json.Marshal(Writeup{Assessment: "summary", Findings: []FindingWording{{Sources: []int{0}, Title: title, Explanation: explanation}}})
	output, err := Reduce([]json.RawMessage{report}, wording)
	if err != nil {
		t.Fatal(err)
	}
	if err = ValidateResolvedOutput(resolved, output); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyMarkersInPartialRuntimeBodyDoNotCompleteReview(t *testing.T) {
	input, api := fixture()
	handler, _ := NewHandler(api, newMemoryStore())
	resolved := mustResolved(t, handler, input)
	findings := []Finding{}
	for i := 0; i < 30; i++ {
		findings = append(findings, Finding{Path: "outside.go", Line: i + 1, Body: strings.Repeat("界", 2000)})
	}
	output, _ := json.Marshal(struct {
		Summary  string    `json:"summary"`
		Findings []Finding `json:"findings"`
	}{legacyMarker + "\n<!-- head:" + input.HeadSHA + " -->\n\nsummary", findings})
	api.failPostAt = 2
	api.postError = &RejectedError{Status: 422}
	if _, err := handler.Publish(context.Background(), resolved, output); err == nil {
		t.Fatal("expected partial publication")
	}
	api.postError = nil
	outcome, err := handler.Publish(context.Background(), resolved, nil)
	if err != nil || outcome.Status != "published" || len(api.requests) < 4 {
		t.Fatalf("treated model prose as legacy completion: %+v %v requests=%d", outcome, err, len(api.requests))
	}
}
