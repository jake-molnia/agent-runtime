package lifecycle

import (
	"context"
	"errors"
	"math"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

type ResourceSnapshot struct {
	SampledAt            int64   `json:"sampledAt"`
	CPUUtilization       float64 `json:"cpuUtilization"`
	CPUCount             int64   `json:"cpuCount"`
	AvailableMemoryBytes int64   `json:"availableMemoryBytes"`
	TotalMemoryBytes     int64   `json:"totalMemoryBytes"`
}

// Resources reports actual usage of the configured execution nodes, never controller pod capacity.
func Resources(api dynamic.Interface, selector, nodeName string) func(context.Context) (ResourceSnapshot, error) {
	return func(ctx context.Context) (ResourceSnapshot, error) {
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		result := ResourceSnapshot{}
		if selector == "" && nodeName == "" {
			return result, errors.New("execution node selection required")
		}
		nodes := api.Resource(schema.GroupVersionResource{Version: "v1", Resource: "nodes"})
		var objects []unstructured.Unstructured
		if nodeName != "" {
			node, err := nodes.Get(ctx, nodeName, metav1.GetOptions{})
			if err != nil {
				return result, err
			}
			objects = append(objects, *node)
		} else {
			list, err := nodes.List(ctx, metav1.ListOptions{LabelSelector: selector})
			if err != nil {
				return result, err
			}
			if list.GetContinue() != "" {
				return result, errors.New("incomplete node inventory")
			}
			objects = list.Items
		}
		var cpu, usedCPU float64
		now := time.Now()
		for _, object := range objects {
			var node corev1.Node
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(object.Object, &node); err != nil {
				return result, err
			}
			if node.Spec.Unschedulable || node.DeletionTimestamp != nil {
				continue
			}
			ready := false
			for _, condition := range node.Status.Conditions {
				if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
					ready = true
				}
				if (condition.Type == corev1.NodeMemoryPressure || condition.Type == corev1.NodeDiskPressure || condition.Type == corev1.NodePIDPressure) && condition.Status == corev1.ConditionTrue {
					return result, errors.New("execution node under pressure")
				}
			}
			if !ready {
				continue
			}
			metric, err := api.Resource(schema.GroupVersionResource{Group: "metrics.k8s.io", Version: "v1beta1", Resource: "nodes"}).Get(ctx, node.Name, metav1.GetOptions{})
			if err != nil {
				return result, err
			}
			stamp, _, _ := unstructured.NestedString(metric.Object, "timestamp")
			sampled, err := time.Parse(time.RFC3339Nano, stamp)
			if err != nil || now.Sub(sampled) > time.Minute || sampled.After(now.Add(5*time.Second)) {
				return result, errors.New("node metrics stale")
			}
			if result.SampledAt == 0 || sampled.UnixMilli() < result.SampledAt {
				result.SampledAt = sampled.UnixMilli()
			}
			cpuRaw, _, _ := unstructured.NestedString(metric.Object, "usage", "cpu")
			memoryRaw, _, _ := unstructured.NestedString(metric.Object, "usage", "memory")
			cpuUsage, err := resource.ParseQuantity(cpuRaw)
			if err != nil || cpuUsage.Sign() < 0 {
				return result, errors.New("invalid CPU usage")
			}
			memoryUsage, err := resource.ParseQuantity(memoryRaw)
			if err != nil || memoryUsage.Sign() < 0 {
				return result, errors.New("invalid memory usage")
			}
			capacityCPU := node.Status.Allocatable.Cpu().AsApproximateFloat64()
			capacityMemory := node.Status.Allocatable.Memory().Value()
			if capacityCPU <= 0 || capacityMemory <= 0 {
				return result, errors.New("node capacity unavailable")
			}
			cpu += capacityCPU
			usedCPU += cpuUsage.AsApproximateFloat64()
			result.TotalMemoryBytes += capacityMemory
			result.AvailableMemoryBytes += max(int64(0), capacityMemory-memoryUsage.Value())
		}
		if cpu == 0 {
			return result, errors.New("no eligible execution nodes")
		}
		result.CPUCount = int64(math.Floor(cpu))
		result.CPUUtilization = math.Min(1, usedCPU/cpu)
		return result, nil
	}
}
