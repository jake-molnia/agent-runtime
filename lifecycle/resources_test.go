package lifecycle

import (
	"context"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clienttesting "k8s.io/client-go/testing"
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

func capacityNode(name, cpu, memory string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Node", "metadata": map[string]any{"name": name, "labels": map[string]any{"execution": "yes", "kubernetes.io/hostname": name, "kubernetes.io/arch": "amd64"}}, "status": map[string]any{"allocatable": map[string]any{"cpu": cpu, "memory": memory, "pods": "10"}, "conditions": []any{map[string]any{"type": "Ready", "status": "True"}}}}}
}
func capacityPod(name, node, cpu, memory string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": name, "namespace": "other-app"}, "spec": map[string]any{"nodeName": node, "containers": []any{map[string]any{"name": "app", "resources": map[string]any{"requests": map[string]any{"cpu": cpu, "memory": memory}}}}}, "status": map[string]any{"phase": "Running"}}}
}
func capacityAPI(objects ...runtime.Object) *fake.FakeDynamicClient {
	return fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		{Version: "v1", Resource: "nodes"}: "NodeList", {Version: "v1", Resource: "pods"}: "PodList", {Group: "node.k8s.io", Version: "v1", Resource: "runtimeclasses"}: "RuntimeClassList",
	}, objects...)
}
func capacityProfile() Profile {
	return Profile{PodTemplate: corev1.PodTemplateSpec{Spec: corev1.PodSpec{NodeSelector: map[string]string{"execution": "yes"}, Containers: []corev1.Container{{Name: "worker", Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourceMemory: resource.MustParse("1Gi")}}}}}}}
}
func TestRequestCapacityFitsPerNodeAndCountsOtherNamespaces(t *testing.T) {
	api := capacityAPI(capacityNode("a", "2", "4Gi"), capacityNode("b", "2", "4Gi"), capacityPod("cpu", "a", "2", "0"), capacityPod("memory", "b", "0", "4Gi"))
	got, err := ProfileRequestCapacity(context.Background(), api, "exact-profile", capacityProfile())
	if err != nil || got == nil || got.AvailableSlots != 0 || got.Profile != "exact-profile" || got.SampledAt == 0 {
		t.Fatalf("fragmented capacity: %+v %v", got, err)
	}
	api = capacityAPI(capacityNode("a", "2", "4Gi"), capacityPod("other", "a", "1500m", "1Gi"))
	got, err = ProfileRequestCapacity(context.Background(), api, "exact-profile", capacityProfile())
	if err != nil || got == nil || got.AvailableSlots != 0 {
		t.Fatalf("reserved CPU ignored: %+v %v", got, err)
	}
}
func TestRequestCapacityIncludesInitAndRuntimeOverhead(t *testing.T) {
	p := capacityProfile()
	p.PodTemplate.Spec.NodeSelector = map[string]string{"kubernetes.io/hostname": "a"}
	name := "agent-sandbox"
	p.PodTemplate.Spec.RuntimeClassName = &name
	p.PodTemplate.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU] = resource.MustParse("100m")
	p.PodTemplate.Spec.InitContainers = []corev1.Container{{Name: "setup", Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("800m")}}}}
	rc := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "node.k8s.io/v1", "kind": "RuntimeClass", "metadata": map[string]any{"name": name}, "handler": name, "overhead": map[string]any{"podFixed": map[string]any{"cpu": "200m"}}, "scheduling": map[string]any{"nodeSelector": map[string]any{"kubernetes.io/arch": "amd64"}}}}
	occupied := capacityPod("other", "a", "100m", "1Gi")
	unstructured.SetNestedField(occupied.Object, []any{map[string]any{"name": "setup", "resources": map[string]any{"requests": map[string]any{"cpu": "800m"}}}}, "spec", "initContainers")
	unstructured.SetNestedField(occupied.Object, map[string]any{"cpu": "200m"}, "spec", "overhead")
	got, err := ProfileRequestCapacity(context.Background(), capacityAPI(capacityNode("a", "2", "4Gi"), occupied, rc), "profile", p)
	if err != nil || got == nil || got.AvailableSlots != 1 {
		t.Fatalf("init/overhead fit: %+v %v", got, err)
	}
}
func TestRequestCapacityUnknownForIncompleteInventoryOrUnsupportedProfile(t *testing.T) {
	p := capacityProfile()
	api := capacityAPI(capacityNode("a", "2", "4Gi"))
	api.PrependReactor("list", "pods", func(action clienttesting.Action) (bool, runtime.Object, error) {
		return true, &unstructured.UnstructuredList{Object: map[string]any{"metadata": map[string]any{"continue": "more"}}}, nil
	})
	got, err := ProfileRequestCapacity(context.Background(), api, "profile", p)
	if got != nil || err == nil {
		t.Fatalf("accepted partial inventory: %+v %v", got, err)
	}
	p.PodTemplate.Spec.Affinity = &corev1.Affinity{}
	got, err = ProfileRequestCapacity(context.Background(), api, "profile", p)
	if got != nil || err != nil {
		t.Fatalf("unsupported must be unknown: %+v %v", got, err)
	}
	p = capacityProfile()
	name := "missing"
	p.PodTemplate.Spec.RuntimeClassName = &name
	got, err = ProfileRequestCapacity(context.Background(), capacityAPI(capacityNode("a", "2", "4Gi")), "profile", p)
	if got != nil || err == nil {
		t.Fatalf("accepted unknown RuntimeClass: %+v %v", got, err)
	}
}

func TestRequestCapacityReservesPodLevelResizeStatus(t *testing.T) {
	for _, test := range []struct{ name, spec, status string }{
		{"resize down retains allocated resources", "1", "3"},
		{"resize up reserves requested resources", "3", "1"},
	} {
		t.Run(test.name, func(t *testing.T) {
			resources := func(cpu string) corev1.ResourceList {
				return corev1.ResourceList{corev1.ResourceCPU: resource.MustParse(cpu), corev1.ResourceMemory: resource.MustParse("1Gi")}
			}
			pod := corev1.Pod{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Pod"}, ObjectMeta: metav1.ObjectMeta{Name: "resizing", Namespace: "other-app"}, Spec: corev1.PodSpec{NodeName: "a", Resources: &corev1.ResourceRequirements{Requests: resources(test.spec)}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, AllocatedResources: resources(test.status), Resources: &corev1.ResourceRequirements{Requests: resources(test.status)}}}
			object, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&pod)
			if err != nil {
				t.Fatal(err)
			}
			profile := capacityProfile()
			profile.PodTemplate.Spec.Containers[0].Resources.Requests[corev1.ResourceCPU] = resource.MustParse("2")
			got, err := ProfileRequestCapacity(context.Background(), capacityAPI(capacityNode("a", "4", "8Gi"), &unstructured.Unstructured{Object: object}), "profile", profile)
			if err != nil || got == nil || got.AvailableSlots != 0 {
				t.Fatalf("resize reservation ignored: %+v %v", got, err)
			}
		})
	}
}
