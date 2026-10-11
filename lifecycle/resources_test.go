package lifecycle

import (
	"context"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic/fake"
)

func TestResourceSnapshotsUseSelectedNodeAndRejectStaleMetrics(t *testing.T) {
	node := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Node", "metadata": map[string]any{"name": "dipper", "labels": map[string]any{"execution": "yes"}}, "status": map[string]any{"allocatable": map[string]any{"cpu": "20", "memory": "100Gi"}, "conditions": []any{map[string]any{"type": "Ready", "status": "True"}}}}}
	metric := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "metrics.k8s.io/v1beta1", "kind": "NodeMetrics", "metadata": map[string]any{"name": "dipper"}, "timestamp": time.Now().UTC().Format(time.RFC3339Nano), "usage": map[string]any{"cpu": "5000m", "memory": "40Gi"}}}
	nodes := schema.GroupVersionResource{Version: "v1", Resource: "nodes"}
	metrics := schema.GroupVersionResource{Group: "metrics.k8s.io", Version: "v1beta1", Resource: "nodes"}
	api := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{nodes: "NodeList", metrics: "NodeMetricsList"}, node)
	api.Tracker().Create(metrics, metric, "")
	snapshot, err := Resources(api, "execution=yes", "")(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.CPUCount != 20 || snapshot.CPUUtilization != .25 || snapshot.AvailableMemoryBytes != 60<<30 {
		t.Fatalf("%+v", snapshot)
	}
	metric.Object["timestamp"] = time.Now().Add(-2 * time.Minute).UTC().Format(time.RFC3339Nano)
	api.Tracker().Update(metrics, metric, "")
	if _, err := Resources(api, "", "dipper")(context.Background()); err == nil {
		t.Fatal("accepted stale metrics")
	}
	if _, err := Resources(api, "", "")(context.Background()); err == nil {
		t.Fatal("accepted missing selection")
	}
	if _, err := Resources(api, "execution=no", "")(context.Background()); err == nil {
		t.Fatal("accepted empty pool")
	}
}
