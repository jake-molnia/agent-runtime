package githubreview

import (
	"context"
	"errors"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const selectionYAML = `repositories:
  include: [acme/api, acme/web]
  exclude: [acme/legacy]
pull_requests:
  base_branches: [main, "release/*"]
  drafts: false
  labels:
    require_any: [agent-review]
    exclude_any: [skip-agent-review]
  authors:
    exclude: ["dependabot[bot]", "renovate[bot]"]
  changed_paths:
    include: ["src/**", "packages/**"]
    exclude: ["docs/**", "**/*.md"]
`

func selected(t *testing.T) Selection {
	t.Helper()
	var selection Selection
	if err := yaml.Unmarshal([]byte(selectionYAML), &selection); err != nil {
		t.Fatal(err)
	}
	if err := selection.Validate(); err != nil {
		t.Fatal(err)
	}
	return selection
}

func TestSelectionRulesAndGlobSemantics(t *testing.T) {
	selection := selected(t)
	base := PullRequest{Repository: "acme/api", State: "open", BaseBranch: "main", Author: "human", Labels: []string{"agent-review"}}
	files := []File{{Path: "src/deep/main.go"}, {Path: "docs/notes.md"}}
	for _, test := range []struct {
		name   string
		mutate func(*PullRequest)
		files  []File
		reason string
	}{
		{name: "mixed code and docs", reason: "eligible"},
		{name: "release branch", mutate: func(pull *PullRequest) { pull.BaseBranch = "release/v1" }, reason: "eligible"},
		{name: "repository not selected", mutate: func(pull *PullRequest) { pull.Repository = "acme/other" }, reason: "repository_not_selected"},
		{name: "excluded repo", mutate: func(pull *PullRequest) { pull.Repository = "acme/legacy" }, reason: "excluded_repository"},
		{name: "draft", mutate: func(pull *PullRequest) { pull.Draft = true }, reason: "draft_pull_request"},
		{name: "closed", mutate: func(pull *PullRequest) { pull.State = "closed" }, reason: "closed_pull_request"},
		{name: "wrong branch", mutate: func(pull *PullRequest) { pull.BaseBranch = "feature/work" }, reason: "base_branch_not_selected"},
		{name: "excluded author", mutate: func(pull *PullRequest) { pull.Author = "dependabot[bot]" }, reason: "excluded_author"},
		{name: "unknown author", mutate: func(pull *PullRequest) { pull.Author = "" }, reason: "missing_author"},
		{name: "missing label", mutate: func(pull *PullRequest) { pull.Labels = nil }, reason: "missing_required_label"},
		{name: "exclusion wins", mutate: func(pull *PullRequest) { pull.Labels = []string{"agent-review", "skip-agent-review"} }, reason: "excluded_label"},
		{name: "docs only", files: []File{{Path: "docs/notes.md"}}, reason: "no_matching_changed_paths"},
		{name: "root markdown", files: []File{{Path: "src/README.md"}}, reason: "no_matching_changed_paths"},
	} {
		t.Run(test.name, func(t *testing.T) {
			pull := base
			if test.mutate != nil {
				test.mutate(&pull)
			}
			changed := files
			if test.files != nil {
				changed = test.files
			}
			decision := selection.Evaluate(pull, changed)
			if decision.Reason != test.reason || decision.Eligible != (test.reason == "eligible") {
				t.Fatalf("%+v", decision)
			}
		})
	}
	for _, test := range []struct {
		pattern, value string
		match          bool
	}{{"**/*.md", "README.md", true}, {"src/**", "src/a/b.go", true}, {"release/*", "release/a/b", false}, {"packages/**/test?.go", "packages/test1.go", true}, {"src/é*", "src/étude.go", true}} {
		compiled, err := glob(test.pattern)
		if err != nil || compiled.MatchString(test.value) != test.match {
			t.Fatalf("glob %s failed: %v", test.pattern, err)
		}
	}
	selection.PullRequests.ChangedPaths.Include = []string{"src/**broken"}
	if selection.Validate() == nil {
		t.Fatal("malformed glob accepted")
	}
}

func TestResolveSkipsBeforeAgentAndPublishRechecksMetadata(t *testing.T) {
	input, api := fixture()
	input.Repository, api.current.Repository = "acme/api", "acme/api"
	api.current.BaseBranch, api.current.Author = "main", "human"
	rule := Rule{Automation: "github-pr-review", Selection: selected(t)}
	handler, err := NewHandler(api, newMemoryStore(), rule)
	if err != nil {
		t.Fatal(err)
	}
	resolved := mustResolved(t, handler, input)
	if !resolved.Skip || resolved.Reason != "missing_required_label" || resolved.Prompt != "" {
		t.Fatalf("%+v", resolved)
	}
	api.current.Labels = []string{"agent-review"}
	resolved = mustResolved(t, handler, input)
	if resolved.Skip || !strings.Contains(resolved.Prompt, "src/main.go") {
		t.Fatalf("%+v", resolved)
	}
	if err := ValidateResolvedOutput(resolved, validOutput); err != nil {
		t.Fatal(err)
	}
	api.current.Labels = []string{"agent-review", "skip-agent-review"}
	outcome, err := handler.Publish(context.Background(), resolved, validOutput)
	if err != nil || outcome.Status != "ineligible" || len(api.requests) != 0 {
		t.Fatalf("%+v %v", outcome, err)
	}
}

func TestDraftOptInAndAutomationDedupIsolation(t *testing.T) {
	input, api := fixture()
	allowed := true
	selection := Selection{}
	selection.PullRequests.Drafts = &allowed
	api.current.Draft = true
	first, _ := NewHandler(api, newMemoryStore(), Rule{Automation: "first", Selection: selection})
	second, _ := NewHandler(api, newMemoryStore(), Rule{Automation: "second", Selection: selection})
	one, two := mustResolved(t, first, input), mustResolved(t, second, input)
	if one.Skip || two.Skip || one.Key == two.Key {
		t.Fatal("draft opt-in or automation isolation failed")
	}
	outcome, err := first.Publish(context.Background(), one, validOutput)
	if err != nil || outcome.Status != "published" {
		t.Fatalf("%+v %v", outcome, err)
	}
}

func TestEnrollmentRevocationBlocksResolveAndPublish(t *testing.T) {
	input, api := fixture()
	handler, _ := NewHandler(api, newMemoryStore())
	allowed := true
	handler.Enrolled = func(Input) (bool, error) { return allowed, nil }
	resolved := mustResolved(t, handler, input)
	allowed = false
	outcome, err := handler.Publish(context.Background(), resolved, validOutput)
	if err != nil || outcome.Status != "repository_not_enrolled" || len(api.requests) != 0 {
		t.Fatalf("%+v %v", outcome, err)
	}
	resolved = mustResolved(t, handler, input)
	if !resolved.Skip || resolved.Reason != "repository_not_enrolled" {
		t.Fatalf("%+v", resolved)
	}
	handler.Enrolled = func(Input) (bool, error) { return false, errors.New("configuration unavailable") }
	if _, err := handler.Resolve(context.Background(), input, "digest", "run"); err == nil {
		t.Fatal("missing enrollment policy allowed work")
	}
}

func TestSelectionChangesAtFinalPublicationCheck(t *testing.T) {
	for _, change := range []string{"label", "branch", "author"} {
		t.Run(change, func(t *testing.T) {
			input, api := fixture()
			input.Repository, api.current.Repository = "acme/api", "acme/api"
			api.current.BaseBranch, api.current.Author, api.current.Labels = "main", "human", []string{"agent-review"}
			handler, _ := NewHandler(api, newMemoryStore(), Rule{Automation: "review", Selection: selected(t)})
			resolved := mustResolved(t, handler, input)
			final := api.canonicalCalls + 2
			api.onCanonical = func(count int, pull *PullRequest) {
				if count == final {
					switch change {
					case "label":
						pull.Labels = []string{"skip-agent-review"}
					case "branch":
						pull.BaseBranch = "feature/work"
					case "author":
						pull.Author = "dependabot[bot]"
					}
				}
			}
			outcome, err := handler.Publish(context.Background(), resolved, validOutput)
			if err != nil || outcome.Status != "stale" || len(api.requests) != 0 {
				t.Fatalf("%+v %v", outcome, err)
			}
		})
	}
}
