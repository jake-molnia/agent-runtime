package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jake-molnia/agent-runtime/sandbox"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestStopRecoversExpandedUnpublishedAllocation(t *testing.T) {
	for _, action := range []Action{Suspend, ReleaseCompute} {
		t.Run(string(action), func(t *testing.T) {
			ctx := context.Background()
			c, api, worker := runnerFixture(t, nil)
			reject := true
			api.PrependReactor("patch", "persistentvolumeclaims", func(a ktesting.Action) (bool, runtime.Object, error) {
				var patch []struct {
					Path  string
					Value json.RawMessage
				}
				_ = json.Unmarshal(a.(ktesting.PatchAction).GetPatch(), &patch)
				for _, operation := range patch {
					if operation.Path != "/metadata/annotations/agent-runtime~1t3-lifecycle" {
						continue
					}
					var encoded string
					var next State
					_ = json.Unmarshal(operation.Value, &encoded)
					_ = json.Unmarshal([]byte(encoded), &next)
					if reject && next.Handle != nil {
						reject = false
						return true, nil, errors.New("handle save failed after compute creation")
					}
				}
				return false, nil, nil
			})
			runner := NewRunner(c)
			start := request(1, EnsureRunning)
			submitInternal(t, c, runner, start)
			if err := runner.execute(ctx, start); err != nil {
				t.Fatal(err)
			}
			s, _, err := c.load(ctx, "default", start.WorkspaceID)
			if err != nil || s.Handle != nil || s.Epoch != 1 || s.Phase != "starting" {
				t.Fatal("did not reproduce unpublished allocation", s, err)
			}
			spec, _ := c.spec("default", start.WorkspaceID, s.Epoch)
			existing, err := c.Control.FindWorkspace(ctx, spec)
			if err != nil {
				t.Fatal(err)
			}
			pvc, err := api.Resource(sandbox.WorkspacePVCs).Namespace(existing.Namespace).Get(ctx, existing.PVC, metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			_ = unstructured.SetNestedField(pvc.Object, "2Gi", "spec", "resources", "requests", "storage")
			if _, err = api.Resource(sandbox.WorkspacePVCs).Namespace(existing.Namespace).Update(ctx, pvc, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			if _, err = c.Control.FindWorkspace(ctx, spec); err == nil {
				t.Fatal("normal discovery accepted expanded storage")
			}
			beforeDiscovery := len(api.Actions())
			found, err := c.Control.FindWorkspaceForStop(ctx, spec)
			if err != nil || found.UID != existing.UID || found.PVCUID != existing.PVCUID {
				t.Fatal("stop discovery lost allocation or retained PVC identity", found, err)
			}
			for _, action := range api.Actions()[beforeDiscovery:] {
				if action.GetVerb() != "get" {
					t.Fatal("stop discovery changed resources", action)
				}
			}
			stop := request(2, action)
			stop.ResolvePendingAllocation = true
			id := submitInternal(t, c, runner, stop)
			if err = runner.execute(ctx, stop); err != nil {
				t.Fatal(err)
			}
			requireStatus(t, runner, id, "COMPLETED")
			if strings.Join(worker.calls, ",") != "quiesce" {
				t.Fatal("unpublished worker was not quiesced", worker.calls)
			}
			retained, err := api.Resource(sandbox.WorkspacePVCs).Namespace(existing.Namespace).Get(ctx, existing.PVC, metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			size, _, _ := unstructured.NestedString(retained.Object, "spec", "resources", "requests", "storage")
			if retained.GetUID() != existing.PVCUID || size != "2Gi" {
				t.Fatal("recovery changed retained volume")
			}
		})
	}
}

func TestStopDiscoveryRejectsChangedRetainedStorage(t *testing.T) {
	for _, change := range []string{"uid", "class", "access-modes", "owner", "shrink"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			c, api, _ := fixture(t)
			s := run(t, c, request(1, EnsureRunning))
			spec, _ := c.spec("default", testWorkspace, s.Epoch)
			pvc, err := api.Resource(sandbox.WorkspacePVCs).Namespace(s.Handle.Namespace).Get(ctx, s.Handle.PVC, metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			_ = unstructured.SetNestedField(pvc.Object, "2Gi", "spec", "resources", "requests", "storage")
			switch change {
			case "uid":
				pvc.SetUID("replacement-pvc")
			case "class":
				_ = unstructured.SetNestedField(pvc.Object, "other", "spec", "storageClassName")
			case "access-modes":
				_ = unstructured.SetNestedStringSlice(pvc.Object, []string{"ReadWriteMany"}, "spec", "accessModes")
			case "owner":
				pvc.SetLabels(map[string]string{"app.kubernetes.io/managed-by": "other"})
			case "shrink":
				_ = unstructured.SetNestedField(pvc.Object, "512Mi", "spec", "resources", "requests", "storage")
			}
			if _, err = api.Resource(sandbox.WorkspacePVCs).Namespace(s.Handle.Namespace).Update(ctx, pvc, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			if _, err = c.Control.FindWorkspaceForStop(ctx, spec); err == nil {
				t.Fatal("stop discovery accepted changed retained storage")
			}
		})
	}
}

func expandedReleaseRetry(t *testing.T) (*Controller, *fake.FakeDynamicClient, *fakeWorker, *Runner, State, Request) {
	t.Helper()
	ctx := context.Background()
	c, api, worker := runnerFixture(t, nil)
	s := run(t, c, request(1, EnsureRunning))
	runner := NewRunner(c)
	stop := stopRequest(2, ReleaseCompute, s)
	submitInternal(t, c, runner, stop)
	failDelete := true
	api.PrependReactor("delete", "sandboxes", func(a ktesting.Action) (bool, runtime.Object, error) {
		if !failDelete {
			return false, nil, nil
		}
		failDelete = false
		return true, nil, errors.New("temporary delete failure")
	})
	if err := runner.execute(ctx, stop); err != nil {
		t.Fatal(err)
	}
	interrupted, _, err := c.load(ctx, "default", stop.WorkspaceID)
	if err != nil || interrupted.Phase != "releasing" || interrupted.Handle == nil {
		t.Fatal("did not reach releasing retry", interrupted, err)
	}
	sb, err := api.Resource(sandbox.Sandboxes).Namespace(s.Handle.Namespace).Get(ctx, s.Handle.Sandbox, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	sb.SetResourceVersion("100")
	if _, err = api.Resource(sandbox.Sandboxes).Namespace(s.Handle.Namespace).Update(ctx, sb, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	pvc, err := api.Resource(sandbox.WorkspacePVCs).Namespace(s.Handle.Namespace).Get(ctx, s.Handle.PVC, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_ = unstructured.SetNestedField(pvc.Object, "2Gi", "spec", "resources", "requests", "storage")
	if _, err = api.Resource(sandbox.WorkspacePVCs).Namespace(s.Handle.Namespace).Update(ctx, pvc, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Control.ObserveWorkspace(ctx, *interrupted.Handle); err == nil {
		t.Fatal("strict observation unexpectedly accepted expansion")
	}
	if err = c.Control.DeleteWorkspaceSandbox(ctx, *interrupted.Handle); err == nil {
		t.Fatal("saved handle unexpectedly authorized changed resource version")
	}
	if _, err = runner.changeOperation(ctx, stop, func(s *State) bool {
		s.Operation.RetryAt = time.Time{}
		return true
	}); err != nil {
		t.Fatal(err)
	}
	return c, api, worker, runner, s, stop
}

func TestReleaseRetryRefreshesExpandedAllocation(t *testing.T) {
	ctx := context.Background()
	_, api, worker, runner, s, stop := expandedReleaseRetry(t)
	if err := runner.execute(ctx, stop); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, runner, taskID(stop), "COMPLETED")
	if strings.Join(worker.calls, ",") != "resume,quiesce" {
		t.Fatal("release retry repeated or skipped worker quiescence", worker.calls)
	}
	retained, err := api.Resource(sandbox.WorkspacePVCs).Namespace(s.Handle.Namespace).Get(ctx, s.Handle.PVC, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	size, _, _ := unstructured.NestedString(retained.Object, "spec", "resources", "requests", "storage")
	if retained.GetUID() != s.Handle.PVCUID || size != "2Gi" {
		t.Fatal("release retry changed retained volume")
	}
}

func TestReleaseRetryWaitsForDeletingExpandedAllocation(t *testing.T) {
	ctx := context.Background()
	_, api, worker, runner, s, stop := expandedReleaseRetry(t)
	sb, err := api.Resource(sandbox.Sandboxes).Namespace(s.Handle.Namespace).Get(ctx, s.Handle.Sandbox, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	now := metav1.Now()
	sb.SetDeletionTimestamp(&now)
	if _, err = api.Resource(sandbox.Sandboxes).Namespace(s.Handle.Namespace).Update(ctx, sb, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	reads := 0
	api.PrependReactor("get", "sandboxes", func(a ktesting.Action) (bool, runtime.Object, error) {
		reads++
		if reads == 3 {
			if err := api.Tracker().Delete(sandbox.Sandboxes, s.Handle.Namespace, s.Handle.Sandbox); err != nil {
				t.Error(err)
			}
		}
		return false, nil, nil
	})
	if err = runner.execute(ctx, stop); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, runner, taskID(stop), "COMPLETED")
	if reads < 3 || strings.Join(worker.calls, ",") != "resume,quiesce" {
		t.Fatal("deletion recovery did not preserve the original stop", reads, worker.calls)
	}
}

func TestReleaseRetryRejectsChangedAllocation(t *testing.T) {
	for _, change := range []string{"sandbox-uid", "pvc-uid", "missing-pvc", "class", "access-modes", "shrink", "running"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			c, api, _, _, s, stop := expandedReleaseRetry(t)
			sb, err := api.Resource(sandbox.Sandboxes).Namespace(s.Handle.Namespace).Get(ctx, s.Handle.Sandbox, metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			pvc, err := api.Resource(sandbox.WorkspacePVCs).Namespace(s.Handle.Namespace).Get(ctx, s.Handle.PVC, metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			switch change {
			case "sandbox-uid":
				sb.SetUID("replacement-sandbox")
			case "pvc-uid":
				pvc.SetUID("replacement-pvc")
			case "missing-pvc":
				api.PrependReactor("get", "sandboxes", func(a ktesting.Action) (bool, runtime.Object, error) {
					_ = api.Tracker().Delete(sandbox.WorkspacePVCs, s.Handle.Namespace, s.Handle.PVC)
					return false, nil, nil
				})
			case "class":
				_ = unstructured.SetNestedField(pvc.Object, "other", "spec", "storageClassName")
			case "access-modes":
				_ = unstructured.SetNestedStringSlice(pvc.Object, []string{"ReadWriteMany"}, "spec", "accessModes")
			case "shrink":
				_ = unstructured.SetNestedField(pvc.Object, "512Mi", "spec", "resources", "requests", "storage")
			case "running":
				_ = unstructured.SetNestedField(sb.Object, "Running", "spec", "operatingMode")
			}
			if _, err = api.Resource(sandbox.Sandboxes).Namespace(s.Handle.Namespace).Update(ctx, sb, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			if _, err = api.Resource(sandbox.WorkspacePVCs).Namespace(s.Handle.Namespace).Update(ctx, pvc, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			if _, err = c.Execute(ctx, stop); err == nil {
				t.Fatal("release ignored changed allocation")
			}
			if _, err = api.Tracker().Get(sandbox.Sandboxes, s.Handle.Namespace, s.Handle.Sandbox); err != nil {
				t.Fatal("unsafe allocation was deleted", err)
			}
		})
	}
}

func TestExpandedReleaseFinishesBeforeSupersedingStart(t *testing.T) {
	ctx := context.Background()
	c, api, worker, runner, s, _ := expandedReleaseRetry(t)
	h := Handler(c, runner, "control")
	_, resource := workspaceCall(t, h, "GET", workspacePath(), "", "")
	out, _ := setTestState(t, h, "running", resource)
	if out.Code != 202 {
		t.Fatal(out.Code, out.Body)
	}
	pending, _, err := c.load(ctx, "default", testWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	if err = runner.execute(ctx, pending.Request); err != nil {
		t.Fatal(err)
	}
	if _, err = api.Tracker().Get(sandbox.Sandboxes, s.Handle.Namespace, s.Handle.Sandbox); !apierrors.IsNotFound(err) {
		t.Fatal("old allocation not released before start", err)
	}
	objects, err := api.Resource(sandbox.Sandboxes).Namespace(s.Handle.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil || len(objects.Items) != 0 {
		t.Fatal("superseding start bypassed strict storage check", objects, err)
	}
	retained, err := api.Resource(sandbox.WorkspacePVCs).Namespace(s.Handle.Namespace).Get(ctx, s.Handle.PVC, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	size, _, _ := unstructured.NestedString(retained.Object, "spec", "resources", "requests", "storage")
	if retained.GetUID() != s.Handle.PVCUID || size != "2Gi" || strings.Join(worker.calls, ",") != "resume,quiesce" {
		t.Fatal("superseding start changed storage or executed worker")
	}
}

func TestExpandedReleaseFinishesBeforeSupersedingSuspend(t *testing.T) {
	c, api, _, runner, before, _ := expandedReleaseRetry(t)
	h := Handler(c, runner, "control")
	_, resource := workspaceCall(t, h, "GET", workspacePath(), "", "")
	out, _ := setTestState(t, h, "suspended", resource)
	if out.Code != 202 {
		t.Fatal(out.Code, out.Body)
	}
	pending, _, err := c.load(t.Context(), "default", testWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	if err = runner.execute(t.Context(), pending.Request); err != nil {
		t.Fatal(err)
	}
	final, _, err := c.load(t.Context(), "default", testWorkspace)
	if err != nil || final.Phase != "suspended" || final.CompletedOperation != pending.Request.OperationID || final.Handle != nil {
		t.Fatalf("superseding suspend did not converge: %+v %v", final, err)
	}
	if _, err = api.Tracker().Get(sandbox.Sandboxes, before.Handle.Namespace, before.Handle.Sandbox); !apierrors.IsNotFound(err) {
		t.Fatal("old allocation not released", err)
	}
	retained, err := api.Resource(sandbox.WorkspacePVCs).Namespace(before.Handle.Namespace).Get(t.Context(), before.Handle.PVC, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	size, _, _ := unstructured.NestedString(retained.Object, "spec", "resources", "requests", "storage")
	if retained.GetUID() != before.Handle.PVCUID || size != "2Gi" {
		t.Fatal("retained storage changed")
	}
}
