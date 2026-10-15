package lifecycle

import (
	"encoding/json"
	"errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"strconv"
	"testing"

	"github.com/jake-molnia/agent-runtime/sandbox"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

func pendingDisappearanceFixture(t *testing.T, action Action) (*Controller, *fake.FakeDynamicClient, *fakeWorker, State, Request) {
	t.Helper()
	c, api, worker := fixture(t)
	if err := c.reserveWorkspace(t.Context(), testWorkspace, "test"); err != nil {
		t.Fatal(err)
	}
	s := run(t, c, request(1, EnsureRunning))
	s, obj, err := c.load(t.Context(), s.Request.Profile, s.Request.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	s.Identity = nil
	s.Endpoint = ""
	s.Phase = "starting"
	s.CompletedOperation = ""
	if err = c.save(t.Context(), obj, s); err != nil {
		t.Fatal(err)
	}
	r := request(2, action)
	r.ResolvePendingAllocation = true
	if _, err = c.Admit(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	return c, api, worker, s, r
}
func assertAbsentStopPreservesStorage(t *testing.T, c *Controller, api *fake.FakeDynamicClient, before, after State, r Request) {
	t.Helper()
	if after.CompletedOperation != r.OperationID || after.Phase != desiredState(r.Action) || after.Handle != nil || after.Identity != nil || after.Endpoint != "" {
		t.Fatal("absent stop did not settle", after)
	}
	pvc, err := api.Resource(sandbox.WorkspacePVCs).Namespace(before.Handle.Namespace).Get(t.Context(), before.Handle.PVC, metav1.GetOptions{})
	if err != nil || pvc.GetUID() != before.Handle.PVCUID {
		t.Fatal("retained workspace changed", err)
	}
	for _, action := range api.Actions() {
		if action.GetVerb() == "delete" && (action.GetResource() == sandbox.WorkspacePVCs || action.GetResource() == transitionLeases) {
			t.Fatal("stop deleted retained storage or binding", action)
		}
	}
	secretList, err := api.Resource(secrets).Namespace(before.Handle.Namespace).List(t.Context(), metav1.ListOptions{})
	if err != nil || len(secretList.Items) != 0 {
		t.Fatal("old credential remains", err)
	}
}
func TestPendingStopSettlesWhenPublishedAllocationDisappearsBeforeAuthentication(t *testing.T) {
	for _, action := range []Action{Suspend, ReleaseCompute} {
		t.Run(string(action), func(t *testing.T) {
			c, api, _, before, r := pendingDisappearanceFixture(t, action)
			if err := api.Resource(sandbox.Sandboxes).Namespace(before.Handle.Namespace).Delete(t.Context(), before.Handle.Sandbox, metav1.DeleteOptions{}); err != nil {
				t.Fatal(err)
			}
			after, err := c.Execute(t.Context(), r)
			if err != nil {
				t.Fatal(err)
			}
			assertAbsentStopPreservesStorage(t, c, api, before, after, r)
		})
	}
}
func TestPendingStopSettlesAfterBusyAllocationDisappearsWithoutChangingFrozenVersion(t *testing.T) {
	c, api, worker, before, r := pendingDisappearanceFixture(t, ReleaseCompute)
	worker.busy = true
	if _, err := c.Execute(t.Context(), r); !errors.Is(err, ErrWorkerBusy) {
		t.Fatal("expected busy authenticated target", err)
	}
	frozen, _, err := c.load(t.Context(), r.Profile, r.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	version := workspaceVersion(frozen)
	if err = api.Resource(sandbox.Sandboxes).Namespace(before.Handle.Namespace).Delete(t.Context(), before.Handle.Sandbox, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	after, err := c.Execute(t.Context(), r)
	if err != nil {
		t.Fatal(err)
	}
	assertAbsentStopPreservesStorage(t, c, api, before, after, r)
	if workspaceVersion(after) != version {
		t.Fatal("absence cleanup changed frozen stop version")
	}
	if len(worker.calls) != 2 {
		t.Fatal("absence cleanup retried worker calls", worker.calls)
	}
}
func TestFencedStopSettlesWhenItsAllocationDisappears(t *testing.T) {
	c, api, _ := fixture(t)
	before := run(t, c, request(1, EnsureRunning))
	r := stopRequest(2, ReleaseCompute, before)
	if _, err := c.Admit(t.Context(), r); err != nil {
		t.Fatal(err)
	}
	if err := api.Resource(sandbox.Sandboxes).Namespace(before.Handle.Namespace).Delete(t.Context(), before.Handle.Sandbox, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	after, err := c.Execute(t.Context(), r)
	if err != nil {
		t.Fatal(err)
	}
	assertAbsentStopPreservesStorage(t, c, api, before, after, r)
}
func TestPendingStopDoesNotConfuseMissingDependencyWithMissingSandbox(t *testing.T) {
	for _, dependency := range []string{"persistentvolumeclaims", "pods"} {
		t.Run(dependency, func(t *testing.T) {
			c, api, _, before, r := pendingDisappearanceFixture(t, ReleaseCompute)
			gets := 0
			if dependency == "persistentvolumeclaims" {
				api.PrependReactor("get", dependency, func(ktesting.Action) (bool, runtime.Object, error) {
					gets++
					if gets == 2 {
						return true, nil, apierrors.NewNotFound(schema.GroupResource{Resource: dependency}, before.Handle.PVC)
					}
					return false, nil, nil
				})
			} else {
				api.PrependReactor("list", dependency, func(ktesting.Action) (bool, runtime.Object, error) {
					return true, nil, apierrors.NewNotFound(schema.GroupResource{Resource: dependency}, "missing-pod-list")
				})
			}
			if _, err := c.Execute(t.Context(), r); err == nil {
				t.Fatal("missing dependency completed stop")
			}
			current, _, err := c.load(t.Context(), r.Profile, r.WorkspaceID)
			if err != nil {
				t.Fatal(err)
			}
			if current.CompletedOperation == r.OperationID || current.Handle == nil {
				t.Fatal("missing dependency cleared allocation")
			}
			if _, err = api.Resource(sandbox.Sandboxes).Namespace(before.Handle.Namespace).Get(t.Context(), before.Handle.Sandbox, metav1.GetOptions{}); err != nil {
				t.Fatal("sandbox deleted", err)
			}
			secretList, err := api.Resource(secrets).Namespace(before.Handle.Namespace).List(t.Context(), metav1.ListOptions{})
			if err != nil || len(secretList.Items) != 1 {
				t.Fatal("credential deleted", err)
			}
		})
	}
}
func TestPendingStopRejectsReplacementAtSameSandboxName(t *testing.T) {
	c, api, _, before, r := pendingDisappearanceFixture(t, ReleaseCompute)
	obj, err := api.Resource(sandbox.Sandboxes).Namespace(before.Handle.Namespace).Get(t.Context(), before.Handle.Sandbox, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	obj.SetUID(types.UID("replacement-allocation"))
	if _, err = api.Resource(sandbox.Sandboxes).Namespace(before.Handle.Namespace).Update(t.Context(), obj, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Execute(t.Context(), r); err == nil {
		t.Fatal("replacement admitted as stopped")
	}
	actual, err := api.Resource(sandbox.Sandboxes).Namespace(before.Handle.Namespace).Get(t.Context(), before.Handle.Sandbox, metav1.GetOptions{})
	if err != nil || actual.GetUID() != obj.GetUID() {
		t.Fatal("replacement removed", err)
	}
	secretList, err := api.Resource(secrets).Namespace(before.Handle.Namespace).List(t.Context(), metav1.ListOptions{})
	if err != nil || len(secretList.Items) != 1 {
		t.Fatal("replacement credential removed", err)
	}
}

func TestPendingStopDoesNotCleanCredentialsAfterIntentChangesDuringAbsenceCheck(t *testing.T) {
	c, api, _, before, r := pendingDisappearanceFixture(t, ReleaseCompute)
	if err := api.Resource(sandbox.Sandboxes).Namespace(before.Handle.Namespace).Delete(t.Context(), before.Handle.Sandbox, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	gets := 0
	api.PrependReactor("get", "sandboxes", func(ktesting.Action) (bool, runtime.Object, error) {
		gets++
		if gets == 2 {
			raw, err := api.Tracker().Get(sandbox.WorkspacePVCs, before.Handle.Namespace, before.Handle.PVC)
			if err != nil {
				return true, nil, err
			}
			obj := raw.(*unstructured.Unstructured).DeepCopy()
			annotations := obj.GetAnnotations()
			var current State
			if err = json.Unmarshal([]byte(annotations[stateAnnotation]), &current); err != nil {
				return true, nil, err
			}
			current.Request = request(3, EnsureRunning)
			encoded, err := json.Marshal(current)
			if err != nil {
				return true, nil, err
			}
			annotations[stateAnnotation] = string(encoded)
			obj.SetAnnotations(annotations)
			revision, _ := strconv.Atoi(obj.GetResourceVersion())
			obj.SetResourceVersion(strconv.Itoa(revision + 1))
			if err = api.Tracker().Update(sandbox.WorkspacePVCs, obj, before.Handle.Namespace); err != nil {
				return true, nil, err
			}
		}
		return false, nil, nil
	})
	if _, err := c.Execute(t.Context(), r); err == nil {
		t.Fatal("superseded stop completed")
	}
	secretList, err := api.Resource(secrets).Namespace(before.Handle.Namespace).List(t.Context(), metav1.ListOptions{})
	if err != nil || len(secretList.Items) != 1 {
		t.Fatal("superseded stop removed credential", err)
	}
	current, _, err := c.load(t.Context(), r.Profile, r.WorkspaceID)
	if err != nil || current.Request.Action != EnsureRunning || current.CompletedOperation == r.OperationID {
		t.Fatal("superseded stop overwrote new intent", err)
	}
}
