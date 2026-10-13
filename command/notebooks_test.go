package command

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jake-molnia/agent-runtime/workflows"
)

func TestNotebookWorkersRequireDeploymentIsolation(t *testing.T) {
	t.Setenv("AGENT_NOTEBOOK_DIR", t.TempDir())
	t.Setenv("AGENT_DEPLOYMENT_ID", "")
	plans := map[string]workflows.Snapshot{"news": {Workflow: workflows.Workflow{Notebook: true}}}
	if _, err := configuredNotebooks(plans); err == nil {
		t.Fatal("accepted unscoped notebook worker")
	}
	for _, invalid := range []string{"../other", "/absolute", "contains spaces"} {
		t.Setenv("AGENT_DEPLOYMENT_ID", invalid)
		if _, err := configuredNotebooks(plans); err == nil {
			t.Fatalf("unsafe deployment ID accepted: %s", invalid)
		}
	}
	t.Setenv("AGENT_DEPLOYMENT_ID", "team-a")
	a, err := configuredNotebooks(plans)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := a.Begin(ctx, "news", "first", "digest", json.RawMessage(`{}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Commit(ctx, "news", "first", "digest", json.RawMessage(`{"status":"completed","notebook":"team-a private notes"}`)); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_DEPLOYMENT_ID", "team-b")
	b, err := configuredNotebooks(plans)
	if err != nil {
		t.Fatal(err)
	}
	notes, err := b.Begin(ctx, "news", "first", "digest", json.RawMessage(`{}`))
	if err != nil || notes.Notebook != "" || notes.Base != 0 {
		t.Fatalf("cross-deployment notebook leak: %+v %v", notes, err)
	}
}
