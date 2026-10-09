package command

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/hatchet-dev/hatchet/pkg/client"
	"github.com/hatchet-dev/hatchet/pkg/client/rest"
	"github.com/jake-molnia/agent-runtime/workflows"
)

type fakeRuns struct {
	states   []*client.RunDetails
	calls    int
	canceled []uuid.UUID
}

func (fake *fakeRuns) GetDetails(ctx context.Context, id uuid.UUID) (*client.RunDetails, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	index := fake.calls
	fake.calls++
	if index >= len(fake.states) {
		index = len(fake.states) - 1
	}
	return fake.states[index], nil
}
func (fake *fakeRuns) Cancel(ctx context.Context, request rest.V1CancelTaskRequest) (*rest.V1CancelledTasks, error) {
	fake.canceled = append([]uuid.UUID(nil), (*request.ExternalIds)...)
	return &rest.V1CancelledTasks{Ids: &fake.canceled}, nil
}
func completedRun(value string) *client.RunDetails {
	return &client.RunDetails{Status: rest.V1TaskStatusCOMPLETED, Done: true, TaskRuns: map[string]*client.TaskRunDetails{"result": {Status: rest.V1TaskStatusCOMPLETED, Output: json.RawMessage(`{"value":` + value + `}`)}}}
}
func TestRunResultStatesAndRendering(t *testing.T) {
	id := uuid.New()
	for _, value := range []string{`"finished"`, `{"status":"completed","report":"finished","sources":[],"notebook":"notes"}`} {
		fake := &fakeRuns{states: []*client.RunDetails{completedRun(value)}}
		var out bytes.Buffer
		if err := printRunResult(context.Background(), fake, id, false, true, &out); err != nil {
			t.Fatal(err)
		}
		if out.String() != "finished\n" {
			t.Fatalf("text: %q", out.String())
		}
		out.Reset()
		if err := printRunResult(context.Background(), fake, id, false, false, &out); err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(out.String()) != value {
			t.Fatalf("JSON: %q", out.String())
		}
	}
	for _, status := range []rest.V1TaskStatus{rest.V1TaskStatusFAILED, rest.V1TaskStatusCANCELLED, rest.V1TaskStatusRUNNING, rest.V1TaskStatusQUEUED} {
		fake := &fakeRuns{states: []*client.RunDetails{{Status: status}}}
		if _, err := waitRunResult(context.Background(), fake, id, false, time.Millisecond); err == nil {
			t.Fatalf("accepted %s", status)
		}
	}
	fake := &fakeRuns{states: []*client.RunDetails{{Status: rest.V1TaskStatusRUNNING}, completedRun(`null`)}}
	if value, err := waitRunResult(context.Background(), fake, id, true, time.Millisecond); err != nil || string(value) != "null" {
		t.Fatalf("wait: %s %v", value, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	fake = &fakeRuns{states: []*client.RunDetails{{Status: rest.V1TaskStatusQUEUED}}}
	if _, err := waitRunResult(ctx, fake, id, true, time.Hour); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("context wait: %v", err)
	}
}
func TestRunInspectCancelAndInvalidIDs(t *testing.T) {
	id := uuid.New()
	fake := &fakeRuns{states: []*client.RunDetails{completedRun(`true`)}}
	var out bytes.Buffer
	if err := executeRunCommand(context.Background(), fake, "inspect", id, false, false, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "COMPLETED") {
		t.Fatal(out.String())
	}
	if err := executeRunCommand(context.Background(), fake, "cancel", id, false, false, &out); err != nil {
		t.Fatal(err)
	}
	if len(fake.canceled) != 1 || fake.canceled[0] != id {
		t.Fatal("wrong cancellation ID")
	}
	for _, invalid := range []string{"bad", uuid.Nil.String()} {
		if err := runsCommand(context.Background(), []string{"result", invalid}); err == nil || !strings.Contains(err.Error(), "UUID") {
			t.Fatalf("invalid ID: %v", err)
		}
	}
}
func TestInvocationUsesDefaultAndPinsDigest(t *testing.T) {
	plans := map[string]workflows.Snapshot{"brief": {Digest: "pinned", Workflow: workflows.Workflow{DefaultInput: json.RawMessage(`{"brief":"daily"}`)}}}
	input, err := invocation(plans, "brief", nil)
	if err != nil || input.Digest != "pinned" || string(input.Input) != `{"brief":"daily"}` {
		t.Fatalf("default: %+v %v", input, err)
	}
}

func TestRunResultRejectsMissingOrMalformedValue(t *testing.T) {
	id := uuid.New()
	for _, details := range []*client.RunDetails{
		nil,
		{Status: rest.V1TaskStatusCOMPLETED},
		{Status: rest.V1TaskStatusCOMPLETED, TaskRuns: map[string]*client.TaskRunDetails{"result": {Status: rest.V1TaskStatusCOMPLETED, Output: json.RawMessage(`{}`)}}},
		{Status: rest.V1TaskStatusCOMPLETED, TaskRuns: map[string]*client.TaskRunDetails{"result": {Status: rest.V1TaskStatusCOMPLETED, Output: json.RawMessage(`broken`)}}},
	} {
		if _, err := waitRunResult(context.Background(), &fakeRuns{states: []*client.RunDetails{details}}, id, false, time.Millisecond); err == nil {
			t.Fatal("accepted missing/malformed result")
		}
	}
	if err := printRunResult(context.Background(), &fakeRuns{states: []*client.RunDetails{completedRun(`{"report":123}`)}}, id, false, true, &bytes.Buffer{}); err == nil {
		t.Fatal("accepted non-string report")
	}
}

func TestWorkflowTemplatesAndInitWithoutDeployment(t *testing.T) {
	root := t.TempDir()
	t.Setenv("AGENT_DEFINITIONS_DIR", root)
	t.Setenv("AGENT_DEFINITIONS_FILE", "")
	if err := workflowsCommand([]string{"templates"}); err != nil {
		t.Fatal(err)
	}
	if err := workflowsCommand([]string{"init", "daily-brief", "morning"}); err != nil {
		t.Fatal(err)
	}
	if err := workflowsCommand([]string{"init", "daily-brief", "morning"}); err == nil {
		t.Fatal("overwrote existing workflow")
	}
}
