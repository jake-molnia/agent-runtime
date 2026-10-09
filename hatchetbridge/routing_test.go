package hatchetbridge

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
	"github.com/jake-molnia/agent-runtime/githubreview"
)

func TestConfiguredRoutingIsolatesEveryRevision(t *testing.T) {
	name := strings.Repeat("a", 128)
	first := ConfiguredWorkflowName(name, "first")
	if len(first) > 128 || first == ConfiguredWorkflowName(name, "second") || first == ConfiguredWorkflowName(name+"other", "first") {
		t.Fatal("revision routing collides or exceeds name limit")
	}
	fixture := newConfiguredTestFixture(t)
	config := reviewTestPolicy()
	config.NativeEvents = true
	config.WorkerLabels = map[string]string{"cluster": "portal"}
	ingress, err := RegisterReviewIngress(offlineLifecycleClient(t), config, fixture.workflow, fixture.plan.Digest, func() (githubreview.Integration, error) { return config, nil }, &githubreview.Handler{})
	if err != nil {
		t.Fatal(err)
	}
	declaration, _, _, _ := ingress.Dump()
	if len(declaration.EventTriggers) != 1 || len(declaration.Tasks) != 1 {
		t.Fatal("native event dispatch not registered")
	}
	originalAction := declaration.Tasks[0].Action
	changed, err := RegisterReviewIngress(offlineLifecycleClient(t), config, fixture.workflow, "new-digest", func() (githubreview.Integration, error) { return config, nil }, &githubreview.Handler{})
	if err != nil {
		t.Fatal(err)
	}
	next, _, _, _ := changed.Dump()
	if next.Name != declaration.Name || next.Tasks[0].Action == originalAction {
		t.Fatal("admission identity changed or incompatible workers share action")
	}
	labels := ReviewLabels(config)
	if labels["cluster"].Value != "portal" || !labels["cluster"].Required {
		t.Fatal("placement labels became optional")
	}
}
func TestAwaitChildCancelsSchedulerRunWhenParentBecomesObsolete(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	stopped := make(chan struct{})
	called := false
	result := func() (*hatchet.WorkflowResult, error) { <-stopped; return nil, context.Canceled }
	cancel(ErrReviewObsolete)
	_, err := awaitChild(ctx, result, func(cleanup context.Context) error {
		called = true
		if cleanup.Err() != nil {
			t.Fatal("scheduler cancellation inherited cancelled context")
		}
		deadline, ok := cleanup.Deadline()
		if !ok || time.Until(deadline) > 15*time.Second {
			t.Fatal("unbounded cancellation")
		}
		close(stopped)
		return nil
	})
	if !called || !errors.Is(err, ErrReviewObsolete) {
		t.Fatalf("scheduler child not cancelled: %v %v", called, err)
	}
}
