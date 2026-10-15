package lifecycle

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jake-molnia/agent-runtime/sandbox"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
)

type reviewAPI struct {
	dynamic.Interface
	afterCreate func(schema.GroupVersionResource, *unstructured.Unstructured)
	beforePatch func(schema.GroupVersionResource, []byte) error
}

func (a reviewAPI) Resource(g schema.GroupVersionResource) dynamic.NamespaceableResourceInterface {
	return reviewNamespace{NamespaceableResourceInterface: a.Interface.Resource(g), api: a, resource: g}
}

type reviewNamespace struct {
	dynamic.NamespaceableResourceInterface
	api      reviewAPI
	resource schema.GroupVersionResource
}

func (n reviewNamespace) Namespace(ns string) dynamic.ResourceInterface {
	return reviewResource{ResourceInterface: n.NamespaceableResourceInterface.Namespace(ns), api: n.api, resource: n.resource}
}

type reviewResource struct {
	dynamic.ResourceInterface
	api      reviewAPI
	resource schema.GroupVersionResource
}

func (r reviewResource) Create(ctx context.Context, obj *unstructured.Unstructured, opts metav1.CreateOptions, subresources ...string) (*unstructured.Unstructured, error) {
	result, err := r.ResourceInterface.Create(ctx, obj, opts, subresources...)
	if err == nil && r.api.afterCreate != nil {
		r.api.afterCreate(r.resource, result)
	}
	return result, err
}
func (r reviewResource) Patch(ctx context.Context, name string, kind types.PatchType, data []byte, opts metav1.PatchOptions, subresources ...string) (*unstructured.Unstructured, error) {
	if r.api.beforePatch != nil {
		if err := r.api.beforePatch(r.resource, data); err != nil {
			return nil, err
		}
	}
	return r.ResourceInterface.Patch(ctx, name, kind, data, opts, subresources...)
}
func reviewWait(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal("allocation hook did not run")
	}
}

func TestReviewStopWaitsForInFlightAllocationPublication(t *testing.T) {
	for _, desired := range []string{"suspended", "released"} {
		t.Run(desired, func(t *testing.T) {
			c, api, _ := fixture(t)
			tasks := &fakeTasks{}
			h, res := registerTestWorkspace(t, c, tasks)
			_, res = setTestState(t, h, "running", res)
			start := tasks.request
			created, proceed := make(chan struct{}), make(chan struct{})
			c.Control.API = reviewAPI{Interface: api, afterCreate: func(g schema.GroupVersionResource, _ *unstructured.Unstructured) {
				if g == sandbox.Sandboxes {
					close(created)
					<-proceed
				}
			}}
			runner := NewRunner(c)
			done := make(chan error, 1)
			go func() { done <- runner.execute(context.Background(), start) }()
			reviewWait(t, created)
			_, res = workspaceCall(t, h, "GET", workspacePath(), "", "")
			out, stop := setTestState(t, h, desired, res)
			if out.Code != 202 || stop.Progress == nil {
				close(proceed)
				<-done
				t.Fatalf("stop completed before startup returned: %d %s", out.Code, out.Body)
			}
			current, _, err := c.load(context.Background(), "default", testWorkspace)
			if err != nil {
				t.Fatal(err)
			}
			if current.CompletedOperation == current.Request.OperationID {
				t.Fatal("stop marked complete")
			}
			if err = runner.execute(context.Background(), current.Request); err != nil {
				t.Fatal(err)
			}
			close(proceed)
			if err = <-done; err != nil {
				t.Fatal(err)
			}
			current, _, err = c.load(context.Background(), "default", testWorkspace)
			if err != nil {
				t.Fatal(err)
			}
			if err = runner.execute(context.Background(), current.Request); err != nil {
				t.Fatal(err)
			}
			out, settled := workspaceCall(t, h, "GET", workspacePath(), "", "")
			if settled.State != desired || settled.Progress != nil || settled.Execution != nil {
				t.Fatalf("stop did not settle: %d %s", out.Code, out.Body)
			}
			if desired == "released" {
				objects, err := api.Resource(sandbox.Sandboxes).Namespace("test").List(context.Background(), metav1.ListOptions{})
				if err != nil || len(objects.Items) != 0 {
					t.Fatal("orphan compute remains", err)
				}
				credentials, err := api.Resource(secrets).Namespace("test").List(context.Background(), metav1.ListOptions{})
				if err != nil || len(credentials.Items) != 0 {
					t.Fatal("orphan credentials remain", err)
				}
			}
		})
	}
}

func TestReviewStopRecoversCreatedAllocationAfterHandleSaveFails(t *testing.T) {
	c, api, worker := fixture(t)
	tasks := &fakeTasks{}
	h, res := registerTestWorkspace(t, c, tasks)
	_, res = setTestState(t, h, "running", res)
	created, failSave := false, true
	c.Control.API = reviewAPI{Interface: api, afterCreate: func(g schema.GroupVersionResource, _ *unstructured.Unstructured) {
		if g == sandbox.Sandboxes {
			created = true
		}
	}, beforePatch: func(g schema.GroupVersionResource, data []byte) error {
		if g == sandbox.WorkspacePVCs && created && failSave && strings.Contains(string(data), `\"handle\"`) {
			failSave = false
			return errors.New("simulated handle persistence failure")
		}
		return nil
	}}
	if _, err := c.Execute(context.Background(), tasks.request); err == nil {
		t.Fatal("expected handle save failure")
	}
	s, _, err := c.load(context.Background(), "default", testWorkspace)
	if err != nil || s.Handle != nil {
		t.Fatal("fixture did not leave an unpublished allocation", err)
	}
	_, res = workspaceCall(t, h, "GET", workspacePath(), "", "")
	out, _ := setTestState(t, h, "released", res)
	if out.Code != 202 {
		t.Fatal(out.Code, out.Body)
	}
	worker.busy = true
	if _, err = c.Execute(context.Background(), tasks.request); !errors.Is(err, ErrWorkerBusy) {
		t.Fatal("recovered worker was not quiesced", err)
	}
	worker.busy = false
	if _, err = c.Execute(context.Background(), tasks.request); err != nil {
		t.Fatal(err)
	}
	out, res = workspaceCall(t, h, "GET", workspacePath(), "", "")
	if res.State != "released" || res.Progress != nil {
		t.Fatal(out.Code, out.Body)
	}
	for _, g := range []schema.GroupVersionResource{sandbox.Sandboxes, secrets} {
		objects, err := api.Resource(g).Namespace("test").List(context.Background(), metav1.ListOptions{})
		if err != nil || len(objects.Items) != 0 {
			t.Fatal("orphan resource remains", g, err)
		}
	}
}

func TestReviewFreshBindingFencesAnotherReplicaCachedAccess(t *testing.T) {
	c, _, _ := fixture(t)
	tasks := &fakeTasks{}
	h, res := registerTestWorkspace(t, c, tasks)
	_, res = setTestState(t, h, "running", res)
	if _, err := c.Execute(context.Background(), tasks.request); err != nil {
		t.Fatal(err)
	}
	_, res = workspaceCall(t, h, "GET", workspacePath(), "", "")
	replica, err := New(c.Control, c.Profiles, c.SecretKey)
	if err != nil {
		t.Fatal(err)
	}
	replica.PoolID = c.PoolID
	replica.Worker = c.Worker
	other := Handler(replica, tasks, "control")
	out, pending := setTestState(t, other, "suspended", res)
	if out.Code != 202 {
		t.Fatal(out.Code, out.Body)
	}
	out, observed := workspaceCall(t, h, "GET", workspacePath(), "", "")
	if observed.DesiredState != "suspended" || observed.Version != pending.Version || observed.Execution != nil || observed.Progress == nil {
		t.Fatalf("stale cached intent/credentials: %d %s", out.Code, out.Body)
	}
}

func TestReviewStopRetryRefencesOnlyAfterCurrentIncarnationETag(t *testing.T) {
	c, _, worker := fixture(t)
	tasks := &fakeTasks{}
	h, res := registerTestWorkspace(t, c, tasks)
	_, res = setTestState(t, h, "running", res)
	if _, err := c.Execute(context.Background(), tasks.request); err != nil {
		t.Fatal(err)
	}
	_, res = workspaceCall(t, h, "GET", workspacePath(), "", "")
	_, accepted := setTestState(t, h, "suspended", res)
	first := tasks.request
	worker.identity.Incarnation = "restarted"
	if _, err := c.Execute(context.Background(), first); err == nil {
		t.Fatal("old incarnation stop succeeded")
	}
	out, _ := setTestState(t, h, "suspended", accepted)
	if out.Code != 412 {
		t.Fatal("old ETag refenced stop", out.Code, out.Body)
	}
	_, current := workspaceCall(t, h, "GET", workspacePath(), "", "")
	out, pending := setTestState(t, h, "suspended", current)
	if out.Code != 202 || tasks.request.OperationID == first.OperationID || tasks.request.ExpectedIncarnation != "restarted" {
		t.Fatal("current ETag did not refence", out.Code, out.Body, tasks.request)
	}
	refenced := tasks.request
	out, again := setTestState(t, h, "suspended", pending)
	if out.Code != 202 || again.Version != pending.Version || tasks.request != refenced {
		t.Fatal("same incarnation retry changed intent", out.Code, out.Body)
	}
	if _, err := c.Execute(context.Background(), refenced); err != nil {
		t.Fatal(err)
	}
}

func TestReviewPendingStopKeepsFirstAuthenticatedTargetUntilRefenced(t *testing.T) {
	c, _, worker := fixture(t)
	tasks := &fakeTasks{}
	h, res := registerTestWorkspace(t, c, tasks)
	_, res = setTestState(t, h, "running", res)
	started, err := c.Execute(context.Background(), tasks.request)
	if err != nil {
		t.Fatal(err)
	}
	// Model startup interrupted after publishing its handle but before authenticating the worker.
	s, obj, err := c.load(context.Background(), "default", testWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	s.Identity = nil
	s.Endpoint = ""
	s.Phase = "starting"
	s.CompletedOperation = ""
	if err = c.save(context.Background(), obj, s); err != nil {
		t.Fatal(err)
	}
	_, res = workspaceCall(t, h, "GET", workspacePath(), "", "")
	out, _ := setTestState(t, h, "released", res)
	if out.Code != 202 {
		t.Fatal(out.Code, out.Body)
	}
	pending := tasks.request
	worker.busy = true
	if _, err = c.Execute(context.Background(), pending); !errors.Is(err, ErrWorkerBusy) {
		t.Fatal(err)
	}
	s, _, err = c.load(context.Background(), "default", testWorkspace)
	if err != nil || s.PendingStopIdentity == nil || *s.PendingStopIdentity != *started.Identity {
		t.Fatal("first target not frozen", err)
	}
	_, res = workspaceCall(t, h, "GET", workspacePath(), "", "")
	out, again := setTestState(t, h, "released", res)
	if out.Code != 202 || again.Version != res.Version || tasks.request != pending {
		t.Fatal("same resolved identity created another request", out.Code, out.Body)
	}
	worker.identity.Incarnation = "replacement-after-auth"
	if _, err = c.Access(context.Background(), "default", testWorkspace); err != nil {
		t.Fatal(err)
	}
	worker.busy = false
	if _, err = c.Execute(context.Background(), pending); err == nil {
		t.Fatal("pending stop silently retargeted restarted worker")
	}
	_, res = workspaceCall(t, h, "GET", workspacePath(), "", "")
	out, _ = setTestState(t, h, "released", res)
	if out.Code != 202 || tasks.request.ResolvePendingAllocation || tasks.request.ExpectedIncarnation != worker.identity.Incarnation {
		t.Fatal("new ETag did not establish explicit fence", out.Code, out.Body)
	}
	if _, err = c.Execute(context.Background(), tasks.request); err != nil {
		t.Fatal(err)
	}
}

func TestReviewStopBeforeStartupDoesNotAllocateCompute(t *testing.T) {
	c, api, _ := fixture(t)
	tasks := &fakeTasks{}
	h, res := registerTestWorkspace(t, c, tasks)
	_, res = setTestState(t, h, "running", res)
	old := tasks.request
	out, _ := setTestState(t, h, "suspended", res)
	if out.Code != 202 {
		t.Fatal(out.Code, out.Body)
	}
	stop := tasks.request
	if _, err := c.Execute(context.Background(), old); err == nil {
		t.Fatal("superseded startup executed")
	}
	if _, err := c.Execute(context.Background(), stop); err != nil {
		t.Fatal(err)
	}
	out, res = workspaceCall(t, h, "GET", workspacePath(), "", "")
	if res.State != "suspended" || res.Progress != nil {
		t.Fatal(out.Code, out.Body)
	}
	for _, action := range api.Actions() {
		if action.GetVerb() == "create" && (action.GetResource() == sandbox.Sandboxes || action.GetResource() == secrets) {
			t.Fatal("stop created compute", action)
		}
	}
}

func TestReviewPendingStopRequestValidation(t *testing.T) {
	pending := Request{WorkspaceID: "workspace", Profile: "default", OperationID: "operation", Revision: 1, Action: Suspend, ResolvePendingAllocation: true}
	if err := pending.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Request){func(r *Request) { r.Action = EnsureRunning }, func(r *Request) { r.ExpectedIncarnation = "worker" }, func(r *Request) { r.ExpectedGeneration = 1 }, func(r *Request) { r.ExpectedAllocationID = "1" }} {
		candidate := pending
		change(&candidate)
		if candidate.Validate() == nil {
			t.Fatal("mixed pending/fenced request accepted", candidate)
		}
	}
}

func TestReviewFindWorkspaceNeverCreatesAndChecksPinnedSpec(t *testing.T) {
	c, api, _ := fixture(t)
	tasks := &fakeTasks{}
	_, res := registerTestWorkspace(t, c, tasks)
	_ = res
	spec, err := c.spec("default", testWorkspace, 1)
	if err != nil {
		t.Fatal(err)
	}
	before := len(api.Actions())
	if _, err = c.Control.FindWorkspace(context.Background(), spec); err == nil {
		t.Fatal("missing compute found")
	}
	for _, action := range api.Actions()[before:] {
		if action.GetVerb() != "get" {
			t.Fatal("lookup mutated allocation", action)
		}
	}
	started := run(t, c, request(2, EnsureRunning))
	spec, err = c.spec("default", testWorkspace, started.Epoch)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := c.Control.FindWorkspace(context.Background(), spec)
	if err != nil || handle.UID != started.Handle.UID {
		t.Fatal("existing handle not recovered", err)
	}
	obj, err := api.Resource(sandbox.Sandboxes).Namespace("test").Get(context.Background(), handle.Sandbox, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	containers, _, _ := unstructured.NestedSlice(obj.Object, "spec", "podTemplate", "spec", "containers")
	containers[0].(map[string]any)["image"] = "unexpected-image"
	if err = unstructured.SetNestedSlice(obj.Object, containers, "spec", "podTemplate", "spec", "containers"); err != nil {
		t.Fatal(err)
	}
	if _, err = api.Resource(sandbox.Sandboxes).Namespace("test").Update(context.Background(), obj, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	before = len(api.Actions())
	if _, err = c.Control.FindWorkspace(context.Background(), spec); err == nil {
		t.Fatal("drifted allocation admitted")
	}
	for _, action := range api.Actions()[before:] {
		if action.GetVerb() != "get" {
			t.Fatal("lookup mutated drifted allocation", action)
		}
	}
}
