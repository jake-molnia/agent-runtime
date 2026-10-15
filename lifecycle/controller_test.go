package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jake-molnia/agent-runtime/sandbox"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

var pods = schema.GroupVersionResource{Version: "v1", Resource: "pods"}

type fakeWorker struct {
	identity  Identity
	busy      bool
	calls     []string
	onQuiesce func()
}

func (w *fakeWorker) Identity(context.Context, string, string) (Identity, error) {
	return w.identity, nil
}
func (w *fakeWorker) Quiesce(_ context.Context, _, _ string, r WorkerRequest) (Quiescence, error) {
	w.calls = append(w.calls, "quiesce")
	if w.onQuiesce != nil {
		w.onQuiesce()
	}
	if w.busy {
		return Quiescence{}, errors.New("busy")
	}
	return Quiescence{Identity: w.identity, Revision: r.Revision, Quiescent: true}, nil
}
func (w *fakeWorker) Resume(_ context.Context, _, _ string, r WorkerRequest) error {
	w.calls = append(w.calls, "resume")
	return nil
}
func fixture(t *testing.T) (*Controller, *fake.FakeDynamicClient, *fakeWorker) {
	t.Helper()
	api := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{sandbox.Sandboxes: "SandboxList", sandbox.WorkspacePVCs: "PersistentVolumeClaimList", pods: "PodList", secrets: "SecretList"})
	worker := &fakeWorker{}
	api.PrependReactor("create", "*", func(a ktesting.Action) (bool, runtime.Object, error) {
		obj := a.(ktesting.CreateAction).GetObject().(*unstructured.Unstructured)
		obj.SetUID(types.UID(obj.GetName() + "-uid"))
		obj.SetResourceVersion("1")
		obj.SetGeneration(1)
		if a.GetResource() == sandbox.Sandboxes {
			activateFake(t, api, obj, worker)
		}
		return false, nil, nil
	})
	api.PrependReactor("patch", "sandboxes", func(a ktesting.Action) (bool, runtime.Object, error) {
		patch := a.(ktesting.PatchAction)
		raw, err := api.Tracker().Get(sandbox.Sandboxes, a.GetNamespace(), patch.GetName())
		if err != nil {
			return true, nil, err
		}
		obj := raw.(*unstructured.Unstructured)
		var operations []struct {
			Op, Path string
			Value    any
		}
		json.Unmarshal(patch.GetPatch(), &operations)
		mode := ""
		for _, op := range operations {
			if op.Op == "test" && op.Path == "/metadata/uid" && op.Value != string(obj.GetUID()) {
				return true, nil, errors.New("uid changed")
			}
			if op.Op == "test" && op.Path == "/metadata/resourceVersion" && op.Value != obj.GetResourceVersion() {
				return true, nil, errors.New("version changed")
			}
			if op.Path == "/spec/operatingMode" {
				mode = op.Value.(string)
			}
		}
		obj.SetGeneration(obj.GetGeneration() + 1)
		obj.SetResourceVersion(fmt.Sprint(obj.GetGeneration()))
		unstructured.SetNestedField(obj.Object, mode, "spec", "operatingMode")
		if mode == "Suspended" {
			api.Tracker().Delete(pods, obj.GetNamespace(), obj.GetName()+"-pod")
			obj.Object["status"] = map[string]any{"conditions": []any{map[string]any{"type": "Suspended", "status": "True", "observedGeneration": obj.GetGeneration()}}}
		} else {
			activateFake(t, api, obj, worker)
		}
		if err = api.Tracker().Update(sandbox.Sandboxes, obj, obj.GetNamespace()); err != nil {
			return true, nil, err
		}
		return true, obj, nil
	})
	profile := Profile{Namespace: "test", Storage: sandbox.StorageSpec{Size: "1Gi", StorageClass: "fast", AccessModes: []string{"ReadWriteOnce"}}, WorkerContainer: "worker", Port: 8083, PodTemplate: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "worker", Image: "worker:fixed", VolumeMounts: []corev1.VolumeMount{{Name: "workspace", MountPath: "/workspace"}}}}}}}
	c, err := New(&sandbox.Control{API: api}, map[string]Profile{"default": profile}, []byte(strings.Repeat("x", 32)))
	if err != nil {
		t.Fatal(err)
	}
	c.Worker = worker
	return c, api, worker
}
func activateFake(t *testing.T, api *fake.FakeDynamicClient, obj *unstructured.Unstructured, w *fakeWorker) {
	t.Helper()
	podName := obj.GetName() + "-pod"
	podUID := podName + "-uid"
	obj.Object["status"] = map[string]any{"serviceFQDN": obj.GetName() + ".test.svc", "conditions": []any{map[string]any{"type": "Ready", "status": "True", "observedGeneration": obj.GetGeneration()}}}
	p := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": podName, "namespace": obj.GetNamespace(), "uid": podUID, "ownerReferences": []any{map[string]any{"apiVersion": "agents.x-k8s.io/v1beta1", "kind": "Sandbox", "name": obj.GetName(), "uid": string(obj.GetUID())}}}, "status": map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True"}}}}}
	_ = api.Tracker().Add(p)
	containers, _, _ := unstructured.NestedSlice(obj.Object, "spec", "podTemplate", "spec", "containers")
	env := containers[0].(map[string]any)["env"].([]any)
	identity := Identity{Incarnation: "process-" + fmt.Sprint(obj.GetGeneration()), ProtocolVersion: 1, PodUID: podUID}
	for _, raw := range env {
		e := raw.(map[string]any)
		switch e["name"] {
		case "T3_WORKSPACE_ID":
			identity.WorkspaceID = e["value"].(string)
		case "T3_ALLOCATION_ID":
			identity.AllocationID = e["value"].(string)
		case "T3_ALLOCATION_GENERATION":
			fmt.Sscan(e["value"].(string), &identity.Generation)
		}
	}
	w.identity = identity
}
func request(revision uint64, action Action) Request {
	return Request{WorkspaceID: "central/root", OperationID: fmt.Sprintf("operation-%d", revision), Revision: revision, Action: action, Profile: "default"}
}
func run(t *testing.T, c *Controller, r Request) State {
	t.Helper()
	if _, err := c.Admit(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	s, err := c.Execute(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	return s
}
func stopRequest(revision uint64, action Action, s State) Request {
	r := request(revision, action)
	r.ExpectedAllocationID = s.Handle.AllocationID
	r.ExpectedGeneration = s.Epoch
	r.ExpectedIncarnation = s.Identity.Incarnation
	return r
}
func TestLifecycleRetainsWorkspaceAcrossSuspendAndRelease(t *testing.T) {
	c, api, _ := fixture(t)
	ctx := context.Background()
	s := run(t, c, request(1, EnsureRunning))
	original := *s.Handle
	if s.Phase != "running" || s.Identity == nil {
		t.Fatal("not ready")
	}
	access, err := c.Access(ctx, "default", s.Request.WorkspaceID)
	if err != nil || access.Token == "" {
		t.Fatal("authorized access missing token")
	}
	encoded, _ := json.Marshal(s)
	if strings.Contains(string(encoded), access.Token) {
		t.Fatal("task output leaked worker token")
	}
	suspended := run(t, c, stopRequest(2, Suspend, s))
	if suspended.Phase != "suspended" {
		t.Fatal("not suspended")
	}
	resumed := run(t, c, request(3, EnsureRunning))
	if resumed.Handle.UID != original.UID || resumed.Handle.PVCUID != original.PVCUID || resumed.Epoch != 1 {
		t.Fatal("resume replaced durable identities")
	}
	released := run(t, c, stopRequest(4, ReleaseCompute, resumed))
	if released.Phase != "released" || released.Handle != nil {
		t.Fatal("compute not released")
	}
	if _, err = api.Resource(sandbox.WorkspacePVCs).Namespace(original.Namespace).Get(ctx, original.PVC, metav1.GetOptions{}); err != nil {
		t.Fatal("retained storage lost")
	}
	replacement := run(t, c, request(5, EnsureRunning))
	if replacement.Epoch != 2 || replacement.Handle.UID == original.UID || replacement.Handle.PVCUID != original.PVCUID {
		t.Fatal("replacement did not preserve storage")
	}
}
func TestLifecycleRejectsStaleAndBusyStop(t *testing.T) {
	for _, kind := range []string{"revision", "incarnation", "busy", "new-wake"} {
		t.Run(kind, func(t *testing.T) {
			c, api, w := fixture(t)
			s := run(t, c, request(1, EnsureRunning))
			r := stopRequest(2, Suspend, s)
			if _, err := c.Admit(context.Background(), r); err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "revision":
				if _, err := c.Admit(context.Background(), request(3, EnsureRunning)); err != nil {
					t.Fatal(err)
				}
			case "incarnation":
				w.identity.Incarnation = "replacement"
			case "busy":
				w.busy = true
			case "new-wake":
				w.onQuiesce = func() {
					if _, err := c.Admit(context.Background(), request(3, EnsureRunning)); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, err := c.Execute(context.Background(), r); err == nil {
				t.Fatal("unsafe stop accepted")
			}
			obj, _ := api.Resource(sandbox.Sandboxes).Namespace(s.Handle.Namespace).Get(context.Background(), s.Handle.Sandbox, metav1.GetOptions{})
			mode, _, _ := unstructured.NestedString(obj.Object, "spec", "operatingMode")
			if mode != "Running" {
				t.Fatal("unsafe stop mutated compute")
			}
		})
	}
}
func TestLifecycleDuplicateRequestAndCreateCrashRecovery(t *testing.T) {
	c, api, _ := fixture(t)
	ctx := context.Background()
	r := request(1, EnsureRunning)
	if _, err := c.Admit(ctx, r); err != nil {
		t.Fatal(err)
	}
	fail := true
	api.PrependReactor("patch", "persistentvolumeclaims", func(a ktesting.Action) (bool, runtime.Object, error) {
		var ops []struct{ Value any }
		json.Unmarshal(a.(ktesting.PatchAction).GetPatch(), &ops)
		last := ops[len(ops)-1].Value
		encoded, ok := last.(string)
		if ok && strings.Contains(encoded, "\"handle\"") && fail {
			fail = false
			return true, nil, errors.New("lost handle persistence")
		}
		return false, nil, nil
	})
	if _, err := c.Execute(ctx, r); err == nil {
		t.Fatal("injected persistence failure absent")
	}
	s, err := c.Execute(ctx, r)
	if err != nil {
		t.Fatal(err)
	}
	same, err := c.Execute(ctx, r)
	if err != nil || same.Handle.UID != s.Handle.UID {
		t.Fatal("duplicate operation changed allocation")
	}
	list, _ := api.Resource(sandbox.Sandboxes).Namespace("test").List(ctx, metav1.ListOptions{})
	if len(list.Items) != 1 {
		t.Fatal("retry created duplicate compute")
	}
	r.Action = Suspend
	if _, err = c.Admit(ctx, r); err == nil {
		t.Fatal("conflicting reused revision accepted")
	}
}

func TestAccessObservesWorkerRestartAndRejectsStaleReadiness(t *testing.T) {
	c, api, worker := fixture(t)
	ctx := context.Background()
	state := run(t, c, request(1, EnsureRunning))
	oldIncarnation := state.Identity.Incarnation
	worker.identity.Incarnation = "restarted-worker"
	access, err := c.Access(ctx, "default", state.Request.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if access.State.Identity.Incarnation == oldIncarnation || access.State.Identity.Incarnation != "restarted-worker" || access.Token == "" {
		t.Fatal("cached worker identity returned")
	}
	persisted, _, err := c.load(ctx, "default", state.Request.WorkspaceID)
	if err != nil || persisted.Identity.Incarnation != "restarted-worker" {
		t.Fatal("worker observation not retained")
	}
	observation, err := c.Control.ObserveWorkspace(ctx, *state.Handle)
	if err != nil {
		t.Fatal(err)
	}
	if err = api.Resource(pods).Namespace(state.Handle.Namespace).Delete(ctx, observation.PodName, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	unavailable, err := c.Access(ctx, "default", state.Request.WorkspaceID)
	if err == nil || unavailable.Token != "" {
		t.Fatal("cached readiness admitted missing pod")
	}
}

func TestBusyStopStillAllowsObservingLiveWorker(t *testing.T) {
	c, _, worker := fixture(t)
	ctx := context.Background()
	state := run(t, c, request(1, EnsureRunning))
	stop := stopRequest(2, Suspend, state)
	if _, err := c.Admit(ctx, stop); err != nil {
		t.Fatal(err)
	}
	worker.busy = true
	if _, err := c.Execute(ctx, stop); err == nil {
		t.Fatal("busy worker was stopped")
	}
	access, err := c.Access(ctx, "default", state.Request.WorkspaceID)
	if err != nil || access.Token == "" || access.State.Identity.Incarnation != state.Identity.Incarnation {
		t.Fatal("failed stop hid a live worker from observers")
	}
	if access.State.CompletedOperation == stop.OperationID {
		t.Fatal("uncompleted stop reported as completed")
	}
	worker.busy = false
	if _, err = c.Execute(ctx, stop); err != nil {
		t.Fatal(err)
	}
}
