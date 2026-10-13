package hatchetbridge

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jake-molnia/agent-runtime/githubreview"
	"github.com/jake-molnia/agent-runtime/workflows"
)

func TestConfiguredRoutingIsolatesEveryRevision(t *testing.T) {
	name := strings.Repeat("a", 128)
	first := ConfiguredWorkflowName(name, "first")
	if len(first) > 128 || first == ConfiguredWorkflowName(name, "second") || first == ConfiguredWorkflowName(name+"other", "first") {
		t.Fatal("revision routing collides or exceeds name limit")
	}
	fixture := newConfiguredTestFixture(t)
	config := reviewTestPolicy()
	config.Workflow = fixture.plan.Workflow.Name
	config.NativeEvents = true
	config.WorkerLabels = map[string]string{"cluster": "portal"}
	ingress, err := RegisterReview(offlineLifecycleClient(t), config, func() (githubreview.Integration, error) { return config, nil }, &githubreview.Handler{}, fixture.root, fixture.plan, fixture.backend, fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	declaration, _, _, _ := ingress.Dump()
	if len(declaration.EventTriggers) != 1 || len(declaration.Tasks) != len(fixture.plan.Workflow.Steps)+3 {
		t.Fatal("native review graph not registered")
	}
	if len(declaration.DefaultFilters) != 1 || string(declaration.DefaultFilters[0].Payload) != "{}" || declaration.DefaultFilters[0].Scope != config.Name {
		t.Fatalf("native filter requires an empty JSON payload and integration scope: %+v", declaration.DefaultFilters)
	}
	originalActions := map[string]bool{}
	for _, task := range declaration.Tasks {
		originalActions[task.Action] = true
	}
	catalog := configuredTestCatalog()
	agent := catalog.Agents["code-review"]
	agent.Instructions += " Updated instructions"
	catalog.Agents["code-review"] = agent
	changedPlan := configuredTestCapture(t, configuredTestWorkflow(), catalog)
	if err := workflows.Save(fixture.root, changedPlan); err != nil {
		t.Fatal(err)
	}
	changed, err := RegisterReview(offlineLifecycleClient(t), config, func() (githubreview.Integration, error) { return config, nil }, &githubreview.Handler{}, fixture.root, changedPlan, fixture.backend, fixture.store)
	if err != nil {
		t.Fatal(err)
	}
	next, _, _, _ := changed.Dump()
	if next.Name != declaration.Name || !reflect.DeepEqual(next.ConcurrencyArr, declaration.ConcurrencyArr) {
		t.Fatal("admission identity changed across revisions")
	}
	for _, task := range next.Tasks {
		if originalActions[task.Action] {
			t.Fatalf("incompatible workers share action %s", task.Action)
		}
	}
	labels := ReviewLabels(config)
	if labels["cluster"].Value != "portal" || !labels["cluster"].Required {
		t.Fatal("placement labels became optional")
	}
}
func TestReviewStagesRejectCancelledOrObsoleteRunsBeforeProvision(t *testing.T) {
	for _, scenario := range []string{"cancelled", "stale"} {
		t.Run(scenario, func(t *testing.T) {
			fixture := newConfiguredTestFixture(t)
			config := reviewTestPolicy()
			config.Workflow = fixture.plan.Workflow.Name
			input := githubreview.Input{RepositoryID: 2, InstallationID: 1, Repository: "owner/repo", Number: 7, BaseSHA: strings.Repeat("a", 40), HeadSHA: strings.Repeat("b", 40), DeliveryID: "request-1"}
			api := &reviewTestAPI{current: githubreview.PullRequest{InstallationID: 1, RepositoryID: 2, Repository: "owner/repo", Number: 7, BaseSHA: input.BaseSHA, HeadSHA: input.HeadSHA, State: "open"}}
			handler, _ := githubreview.NewHandler(api, reviewTestStore{})
			workflow, err := RegisterReview(offlineLifecycleClient(t), config, func() (githubreview.Integration, error) { return config, nil }, handler, fixture.root, fixture.plan, fixture.backend, fixture.store)
			if err != nil {
				t.Fatal(err)
			}
			revision, err := ReviewRevision(config, fixture.plan.Digest)
			if err != nil {
				t.Fatal(err)
			}
			callbacks := dagTestCallbacks(t, workflow)
			raw, _ := json.Marshal(githubreview.Request{Review: input})
			base, cancel := context.WithCancel(context.Background())
			defer cancel()
			ctx := reviewTaskContext{configuredTestContext: configuredTestContext{outputs: map[string]any{}, run: "review-run"}, raw: raw, base: base}
			resolveName := ConfiguredWorkflowName("resolve", revision)
			resolved, err := callbacks[resolveName](ctx)
			if err != nil {
				t.Fatal(err)
			}
			ctx.outputs[resolveName] = resolved
			want := ErrReviewObsolete
			if scenario == "cancelled" {
				cancel()
				want = context.Canceled
			} else {
				api.current.HeadSHA = strings.Repeat("c", 40)
			}
			_, err = callbacks[ConfiguredWorkflowName("agent-review", revision)](ctx)
			if !errors.Is(err, want) || len(fixture.backend.calls) != 0 {
				t.Fatalf("obsolete run started agent: %v, calls %d", err, len(fixture.backend.calls))
			}
		})
	}
}
