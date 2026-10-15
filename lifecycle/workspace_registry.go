package lifecycle

import (
	"context"
	"errors"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// Registrations retain this record alongside workspace storage. Atomic creation
// reserves an ID before any profile namespace can create its PVC.
func (c *Controller) reserveWorkspace(ctx context.Context, id, namespace string) error {
	if c.RegistryNamespace == "" {
		return errors.New("workspace registry namespace unavailable")
	}
	key := digest([]string{c.PoolID, id})
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "coordination.k8s.io/v1", "kind": "Lease",
		"metadata": map[string]any{"name": "ar-wb-" + key[:32], "namespace": c.RegistryNamespace,
			"labels":      map[string]any{"app.kubernetes.io/managed-by": "agent-runtime"},
			"annotations": map[string]any{"agent-runtime/workspace-binding-key": key, "agent-runtime/workspace-namespace": namespace}},
		"spec": map[string]any{},
	}}
	api := c.Control.API.Resource(transitionLeases).Namespace(c.RegistryNamespace)
	actual, err := api.Create(ctx, obj, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		actual, err = api.Get(ctx, obj.GetName(), metav1.GetOptions{})
	}
	if err != nil {
		return err
	}
	if actual.GetDeletionTimestamp() != nil || len(actual.GetOwnerReferences()) != 0 || actual.GetLabels()["app.kubernetes.io/managed-by"] != "agent-runtime" || actual.GetAnnotations()["agent-runtime/workspace-binding-key"] != key {
		return failure(409, "workspace_ownership", "Workspace registry ownership does not match", false)
	}
	if actual.GetAnnotations()["agent-runtime/workspace-namespace"] != namespace {
		return failure(409, "binding_conflict", "Workspace is reserved in another profile namespace", false)
	}
	return nil
}
