package lifecycle

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jake-molnia/agent-runtime/sandbox"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestWorkspaceExpandedPVCCanStopWithoutExecutionAccess(t *testing.T) {
	for _, desired := range []string{"suspended", "released"} {
		t.Run(desired, func(t *testing.T) {
			ctx := context.Background()
			c, api, worker := runnerFixture(t, nil)
			s := run(t, c, request(1, EnsureRunning))
			runner := NewRunner(c)
			h := Handler(c, runner, "control")
			pvc, err := api.Resource(sandbox.WorkspacePVCs).Namespace(s.Handle.Namespace).Get(ctx, s.Handle.PVC, metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			_ = unstructured.SetNestedField(pvc.Object, "2Gi", "spec", "resources", "requests", "storage")
			if _, err = api.Resource(sandbox.WorkspacePVCs).Namespace(s.Handle.Namespace).Update(ctx, pvc, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			out, unavailable := workspaceCall(t, h, "GET", workspacePath(), "", "")
			if out.Code != 200 || unavailable.State != "unavailable" || unavailable.Execution != nil {
				t.Fatal(out.Code, out.Body)
			}
			worker.identity.Incarnation = "restarted-after-expansion"
			out, _ = setTestState(t, h, desired, unavailable)
			if out.Code != 412 {
				t.Fatalf("old worker ETag admitted stop: %d %s", out.Code, out.Body)
			}
			_, refreshed := workspaceCall(t, h, "GET", workspacePath(), "", "")
			if refreshed.Execution != nil || refreshed.Version == unavailable.Version {
				t.Fatalf("stop observation did not refresh identity without credentials: %+v", refreshed)
			}
			out, accepted := setTestState(t, h, desired, refreshed)
			if out.Code != 202 || accepted.Execution != nil {
				t.Fatalf("expanded workspace stop rejected: %d %s", out.Code, out.Body)
			}
			pending, _, err := c.load(ctx, "default", testWorkspace)
			if err != nil {
				t.Fatal(err)
			}
			worker.busy = true
			if _, err = c.Execute(ctx, pending.Request); !errors.Is(err, ErrWorkerBusy) {
				t.Fatal("expanded workspace bypassed worker quiescence", err)
			}
			worker.busy = false
			if err = runner.execute(ctx, pending.Request); err != nil {
				t.Fatal(err)
			}
			requireStatus(t, runner, taskID(pending.Request), "COMPLETED")
			out, stopped := workspaceCall(t, h, "GET", workspacePath(), "", "")
			if out.Code != 200 || stopped.State != desired || stopped.Execution != nil {
				t.Fatal(out.Code, out.Body)
			}
			retained, err := api.Resource(sandbox.WorkspacePVCs).Namespace(s.Handle.Namespace).Get(ctx, s.Handle.PVC, metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			size, _, _ := unstructured.NestedString(retained.Object, "spec", "resources", "requests", "storage")
			if retained.GetUID() != pvc.GetUID() || size != "2Gi" {
				t.Fatal("shutdown changed retained storage", retained)
			}
			if strings.Join(worker.calls, ",") != "resume,quiesce,quiesce" {
				t.Fatal("shutdown omitted quiescence", worker.calls)
			}
			spec, _ := c.spec("default", testWorkspace, s.Epoch+1)
			if _, err = c.Control.EnsureWorkspace(ctx, spec); err == nil {
				t.Fatal("expansion enabled allocation under the old storage pin")
			}
		})
	}
}

func TestWorkspaceStopRejectsStorageChangesOtherThanExpansion(t *testing.T) {
	for _, change := range []string{"uid", "owner", "class", "modes", "shrink"} {
		t.Run(change, func(t *testing.T) {
			ctx := context.Background()
			c, api, worker := fixture(t)
			s := run(t, c, request(1, EnsureRunning))
			tasks := &fakeTasks{}
			h := Handler(c, tasks, "control")
			_, before := workspaceCall(t, h, "GET", workspacePath(), "", "")
			pvc, err := api.Resource(sandbox.WorkspacePVCs).Namespace(s.Handle.Namespace).Get(ctx, s.Handle.PVC, metav1.GetOptions{})
			if err != nil {
				t.Fatal(err)
			}
			_ = unstructured.SetNestedField(pvc.Object, "2Gi", "spec", "resources", "requests", "storage")
			switch change {
			case "uid":
				pvc.SetUID("replacement-pvc")
			case "owner":
				pvc.SetLabels(map[string]string{"app.kubernetes.io/managed-by": "other"})
			case "class":
				_ = unstructured.SetNestedField(pvc.Object, "other", "spec", "storageClassName")
			case "modes":
				_ = unstructured.SetNestedStringSlice(pvc.Object, []string{"ReadWriteMany"}, "spec", "accessModes")
			case "shrink":
				_ = unstructured.SetNestedField(pvc.Object, "512Mi", "spec", "resources", "requests", "storage")
			}
			if _, err = api.Resource(sandbox.WorkspacePVCs).Namespace(s.Handle.Namespace).Update(ctx, pvc, metav1.UpdateOptions{}); err != nil {
				t.Fatal(err)
			}
			out, _ := setTestState(t, h, "released", before)
			if out.Code != 409 || tasks.submissions != 0 {
				t.Fatalf("unsafe stop accepted: %d %s", out.Code, out.Body)
			}
			if len(worker.calls) != 1 {
				t.Fatal("unsafe target reached worker", worker.calls)
			}
		})
	}
}
