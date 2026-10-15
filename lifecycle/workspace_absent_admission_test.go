package lifecycle

import (
	"context"
	"errors"
	"net/http"
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

type absentAdmissionFixture struct {
	controller *Controller
	api        *fake.FakeDynamicClient
	tasks      *fakeTasks
	handler    http.Handler
	resource   Workspace
	state      State
}

func runningAbsentAdmissionFixture(t *testing.T) absentAdmissionFixture {
	t.Helper()
	c, api, _ := fixture(t)
	tasks := &fakeTasks{}
	handler, res := registerTestWorkspace(t, c, tasks)
	_, res = setTestState(t, handler, "running", res)
	state, err := c.Execute(t.Context(), tasks.request)
	if err != nil {
		t.Fatal(err)
	}
	_, res = workspaceCall(t, handler, "GET", workspacePath(), "", "")
	return absentAdmissionFixture{c, api, tasks, handler, res, state}
}
func TestAbsentSavedAllocationCanBeAdmittedForStop(t *testing.T) {
	for _, desired := range []string{"suspended", "released"} {
		t.Run(desired, func(t *testing.T) {
			f := runningAbsentAdmissionFixture(t)
			h := f.state.Handle
			registryName := "ar-wb-" + digest([]string{f.controller.PoolID, testWorkspace})[:32]
			registry, err := f.api.Resource(transitionLeases).Namespace(f.controller.RegistryNamespace).Get(t.Context(), registryName, metav1.GetOptions{})
			if err != nil {
				t.Fatal("missing retained registry", err)
			}
			if err = f.api.Resource(sandbox.Sandboxes).Namespace(h.Namespace).Delete(t.Context(), h.Sandbox, metav1.DeleteOptions{}); err != nil {
				t.Fatal(err)
			}
			out, pending := setTestState(t, f.handler, desired, f.resource)
			if out.Code != 202 || pending.DesiredState != desired || pending.Execution != nil || pending.Progress == nil {
				t.Fatal("absent allocation stop not admitted", out.Code, out.Body)
			}
			if err = NewRunner(f.controller).execute(t.Context(), f.tasks.request); err != nil {
				t.Fatal(err)
			}
			out, settled := workspaceCall(t, f.handler, "GET", workspacePath(), "", "")
			if settled.State != desired || settled.Progress != nil || settled.Identity != nil || settled.Version != pending.Version {
				t.Fatal("absent allocation did not settle", out.Code, out.Body)
			}
			credentials, err := f.api.Resource(secrets).Namespace(h.Namespace).List(t.Context(), metav1.ListOptions{})
			if err != nil || len(credentials.Items) != 0 {
				t.Fatal("orphan credential remains", err)
			}
			pvc, err := f.api.Resource(sandbox.WorkspacePVCs).Namespace(h.Namespace).Get(t.Context(), h.PVC, metav1.GetOptions{})
			if err != nil || pvc.GetUID() != h.PVCUID {
				t.Fatal("workspace storage changed", err)
			}
			binding, err := f.api.Resource(transitionLeases).Namespace(f.controller.RegistryNamespace).Get(t.Context(), registry.GetName(), metav1.GetOptions{})
			if err != nil || binding.GetUID() != registry.GetUID() {
				t.Fatal("registry binding changed", err)
			}
			out, _ = setTestState(t, f.handler, desired, f.resource)
			if out.Code != 412 {
				t.Fatal("old ETag accepted after cleanup", out.Code, out.Body)
			}
		})
	}
}

type unreachableAdmissionWorker struct{ Worker }

func (w unreachableAdmissionWorker) Identity(context.Context, string, string) (Identity, error) {
	return Identity{}, errors.New("worker unreachable")
}

func TestStopAdmissionRequiresSandboxAbsenceNotObservationFailure(t *testing.T) {
	for _, failure := range []string{"unreachable-worker", "missing-pvc-observation", "missing-pod-observation", "replacement-uid", "sandbox-read-forbidden"} {
		t.Run(failure, func(t *testing.T) {
			f := runningAbsentAdmissionFixture(t)
			h := f.state.Handle
			switch failure {
			case "unreachable-worker":
				f.controller.Worker = unreachableAdmissionWorker{f.controller.Worker}
			case "missing-pvc-observation":
				gets := 0
				f.api.PrependReactor("get", "persistentvolumeclaims", func(ktesting.Action) (bool, runtime.Object, error) {
					gets++
					if gets == 3 {
						return true, nil, apierrors.NewNotFound(schema.GroupResource{Resource: "persistentvolumeclaims"}, h.PVC)
					}
					return false, nil, nil
				})
			case "missing-pod-observation":
				f.api.PrependReactor("list", "pods", func(ktesting.Action) (bool, runtime.Object, error) {
					return true, nil, apierrors.NewNotFound(schema.GroupResource{Resource: "pods"}, "missing")
				})
			case "replacement-uid":
				obj, err := f.api.Resource(sandbox.Sandboxes).Namespace(h.Namespace).Get(t.Context(), h.Sandbox, metav1.GetOptions{})
				if err != nil {
					t.Fatal(err)
				}
				obj.SetUID(types.UID("replacement"))
				if _, err = f.api.Resource(sandbox.Sandboxes).Namespace(h.Namespace).Update(t.Context(), obj, metav1.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
			case "sandbox-read-forbidden":
				f.api.PrependReactor("get", "sandboxes", func(ktesting.Action) (bool, runtime.Object, error) {
					return true, nil, apierrors.NewForbidden(sandbox.Sandboxes.GroupResource(), h.Sandbox, errors.New("denied"))
				})
			}
			before := f.tasks.submissions
			out, _ := setTestState(t, f.handler, "released", f.resource)
			if out.Code != 409 || f.tasks.submissions != before {
				t.Fatal("failed observation admitted destructive intent", out.Code, out.Body)
			}
			current, _, err := f.controller.load(t.Context(), "default", testWorkspace)
			if err != nil || current.Request.Action != EnsureRunning {
				t.Fatal("failed observation changed intent", err)
			}
			credentials, err := f.api.Resource(secrets).Namespace(h.Namespace).List(t.Context(), metav1.ListOptions{})
			if err != nil || len(credentials.Items) != 1 {
				t.Fatal("failed observation removed credential", err)
			}
		})
	}
}

func TestStopRunnerRechecksAbsenceAfterAdmission(t *testing.T) {
	f := runningAbsentAdmissionFixture(t)
	h := f.state.Handle
	original, err := f.api.Resource(sandbox.Sandboxes).Namespace(h.Namespace).Get(t.Context(), h.Sandbox, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err = f.api.Resource(sandbox.Sandboxes).Namespace(h.Namespace).Delete(t.Context(), h.Sandbox, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	out, _ := setTestState(t, f.handler, "released", f.resource)
	if out.Code != 202 {
		t.Fatal(out.Code, out.Body)
	}
	original.SetUID(types.UID("replacement-after-admission"))
	if err = f.api.Tracker().Create(sandbox.Sandboxes, original, h.Namespace); err != nil {
		t.Fatal(err)
	}
	if _, err = f.controller.Execute(t.Context(), f.tasks.request); err == nil {
		t.Fatal("admission absence reused after replacement appeared")
	}
	replacement, err := f.api.Resource(sandbox.Sandboxes).Namespace(h.Namespace).Get(t.Context(), h.Sandbox, metav1.GetOptions{})
	if err != nil || replacement.GetUID() != original.GetUID() {
		t.Fatal("replacement removed", err)
	}
	credentials, err := f.api.Resource(secrets).Namespace(h.Namespace).List(t.Context(), metav1.ListOptions{})
	if err != nil || len(credentials.Items) != 1 {
		t.Fatal("replacement credential removed", err)
	}
}
