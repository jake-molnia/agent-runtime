package lifecycle

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"github.com/jake-molnia/agent-runtime/sandbox"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var transitionLeases = schema.GroupVersionResource{Group: "coordination.k8s.io", Version: "v1", Resource: "leases"}

func (c *Controller) acquireTransition(ctx context.Context, request Request, now time.Time) (func(), error) {
	api := c.Control.API.Resource(transitionLeases).Namespace(c.Profiles[request.Profile].Namespace)
	name := sandbox.WorkspacePVCName(request.WorkspaceID)
	holder := uuid.NewString()
	lease, err := api.Get(ctx, name, metav1.GetOptions{})
	create := apierrors.IsNotFound(err)
	if err != nil && !create {
		return nil, err
	}
	if create {
		lease = &unstructured.Unstructured{Object: map[string]any{"apiVersion": "coordination.k8s.io/v1", "kind": "Lease", "metadata": map[string]any{"name": name, "labels": map[string]any{"app.kubernetes.io/managed-by": "agent-runtime"}}}}
	} else {
		if lease.GetLabels()["app.kubernetes.io/managed-by"] != "agent-runtime" {
			return nil, errors.New("workspace lease ownership mismatch")
		}
		renewed, _, _ := unstructured.NestedString(lease.Object, "spec", "renewTime")
		duration, _, _ := unstructured.NestedInt64(lease.Object, "spec", "leaseDurationSeconds")
		at, parseErr := time.Parse(time.RFC3339Nano, renewed)
		if parseErr != nil || duration <= 0 || duration > 300 {
			return nil, errors.New("invalid workspace lease")
		}
		if now.Before(at.Add(time.Duration(duration) * time.Second)) {
			return nil, nil
		}
	}
	lease.Object["spec"] = map[string]any{"holderIdentity": holder, "leaseDurationSeconds": int64(300), "renewTime": now.UTC().Format(metav1.RFC3339Micro)}
	if create {
		lease, err = api.Create(ctx, lease, metav1.CreateOptions{})
	} else {
		lease, err = api.Update(ctx, lease, metav1.UpdateOptions{})
	}
	if apierrors.IsAlreadyExists(err) || apierrors.IsConflict(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	uid, rv := lease.GetUID(), lease.GetResourceVersion()
	return func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		// Preconditions prevent an old process from deleting its successor's lease.
		_ = api.Delete(cleanup, name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &rv}})
	}, nil
}
