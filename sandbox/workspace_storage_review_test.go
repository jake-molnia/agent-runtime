package sandbox

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestWorkspaceObservationRejectsExpandedStorageWithUnchangedIdentity(t *testing.T) {
	c, api := workspaceTestControl()
	spec := workspaceTestSpec()
	ctx := context.Background()
	handle, err := c.EnsureWorkspace(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	pvc, err := api.Resource(WorkspacePVCs).Namespace(handle.Namespace).Get(ctx, handle.PVC, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err = unstructured.SetNestedField(pvc.Object, "2Gi", "spec", "resources", "requests", "storage"); err != nil {
		t.Fatal(err)
	}
	if _, err = api.Resource(WorkspacePVCs).Namespace(handle.Namespace).Update(ctx, pvc, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if pvc.GetUID() != handle.PVCUID || pvc.GetAnnotations()["agent-runtime/spec-sha256"] != handle.PVCSpecHash {
		t.Fatal("test changed identity instead of live storage request")
	}
	if _, err = c.ObserveWorkspace(ctx, handle); err == nil {
		t.Fatal("expanded claim advertised as usable")
	}
	if _, err = c.FindWorkspace(ctx, spec); err == nil {
		t.Fatal("expanded claim recovered")
	}
	if _, err = c.EnsureWorkspaceStorage(ctx, spec); err == nil {
		t.Fatal("expanded claim admitted")
	}
}

func TestWorkspaceStorageAcceptsEquivalentQuantitiesAndControllerDefaults(t *testing.T) {
	for _, size := range []string{"1Gi", "1073741824"} {
		for _, liveSize := range []string{"1Gi", "1024Mi", "1073741824", "1073741824e0"} {
			t.Run(size+"_as_"+liveSize, func(t *testing.T) {
				c, api := workspaceTestControl()
				spec := workspaceTestSpec()
				spec.Storage.Size = size
				ctx := context.Background()
				handle, err := c.EnsureWorkspace(ctx, spec)
				if err != nil {
					t.Fatal(err)
				}
				pvc, err := api.Resource(WorkspacePVCs).Namespace(handle.Namespace).Get(ctx, handle.PVC, metav1.GetOptions{})
				if err != nil {
					t.Fatal(err)
				}
				for _, field := range []struct {
					path  []string
					value string
				}{
					{[]string{"spec", "resources", "requests", "storage"}, liveSize},
					{[]string{"spec", "volumeName"}, "bound-volume"},
					{[]string{"spec", "volumeMode"}, "Filesystem"},
					{[]string{"status", "phase"}, "Bound"},
					{[]string{"status", "capacity", "storage"}, "2Gi"},
				} {
					if err = unstructured.SetNestedField(pvc.Object, field.value, field.path...); err != nil {
						t.Fatal(err)
					}
				}
				if _, err = api.Resource(WorkspacePVCs).Namespace(handle.Namespace).Update(ctx, pvc, metav1.UpdateOptions{}); err != nil {
					t.Fatal(err)
				}
				if _, err = c.ObserveWorkspace(ctx, handle); err != nil {
					t.Fatal("equivalent/defaulted observed storage rejected", err)
				}
				if _, err = c.FindWorkspace(ctx, spec); err != nil {
					t.Fatal("equivalent/defaulted recovered storage rejected", err)
				}
				if _, err = c.EnsureWorkspaceStorage(ctx, spec); err != nil {
					t.Fatal("equivalent/defaulted ensured storage rejected", err)
				}
			})
		}
	}
}
