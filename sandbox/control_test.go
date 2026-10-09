package sandbox

import (
	"context"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestWaitReadyPreservesIdentityAcrossWatchReconnect(t *testing.T) {
	for _, reconnect := range []string{"closed", "expired", "error-event"} {
		for _, uid := range []types.UID{"original", "replacement"} {
			t.Run(reconnect+"/"+string(uid), func(t *testing.T) {
				original := &unstructured.Unstructured{Object: map[string]any{
					"metadata": map[string]any{"name": "claim", "uid": "original", "resourceVersion": "1"},
				}}
				relisted := original.DeepCopy()
				relisted.SetUID(uid)
				relisted.SetResourceVersion("2")
				relisted.Object["status"] = map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True"}}}
				api := fake.NewSimpleDynamicClient(runtime.NewScheme())
				api.PrependWatchReactor("sandboxclaims", func(ktesting.Action) (bool, watch.Interface, error) {
					if reconnect == "expired" {
						return true, nil, apierrors.NewResourceExpired("expired")
					}
					w := watch.NewRaceFreeFake()
					if reconnect == "error-event" {
						w.Error(&metav1.Status{Reason: metav1.StatusReasonExpired, Code: 410})
					} else {
						w.Stop()
					}
					return true, w, nil
				})
				api.PrependReactor("get", "sandboxclaims", func(ktesting.Action) (bool, runtime.Object, error) {
					return true, relisted, nil
				})
				ctx, cancel := context.WithTimeout(context.Background(), time.Second)
				defer cancel()
				got, err := waitReady(ctx, api.Resource(Claims).Namespace("test"), original)
				if uid != original.GetUID() {
					if err == nil || err.Error() != "sandbox resource replaced" {
						t.Fatalf("accepted replacement resource: %v, %v", got, err)
					}
				} else if err != nil || got.GetUID() != uid {
					t.Fatalf("failed to reconnect to original resource: %v, %v", got, err)
				}
			})
		}
	}
}
