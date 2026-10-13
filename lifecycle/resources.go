package lifecycle

import (
	"context"
	"errors"
	"math"
	"time"

	nodev1 "k8s.io/api/node/v1"
	"k8s.io/apimachinery/pkg/labels"
	resourcehelper "k8s.io/component-helpers/resource"
	scheduling "k8s.io/component-helpers/scheduling/corev1"
	"k8s.io/klog/v2"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
)

type RequestCapacity struct {
	Profile        string `json:"profile"`
	SampledAt      int64  `json:"sampledAt"`
	AvailableSlots int64  `json:"availableSlots"`
}

type ResourceSnapshot struct {
	RequestCapacity      *RequestCapacity `json:"requestCapacity,omitempty"`
	SampledAt            int64            `json:"sampledAt"`
	CPUUtilization       float64          `json:"cpuUtilization"`
	CPUCount             int64            `json:"cpuCount"`
	AvailableMemoryBytes int64            `json:"availableMemoryBytes"`
	TotalMemoryBytes     int64            `json:"totalMemoryBytes"`
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

// ProfileRequestCapacity estimates CPU, memory and pod-count fit for simple profiles.
// It is not a scheduling guarantee: storage/admission and concurrent submissions still apply.
func ProfileRequestCapacity(ctx context.Context, api dynamic.Interface, name string, profile Profile) (*RequestCapacity, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	started := time.Now().UnixMilli()
	spec := profile.PodTemplate.Spec.DeepCopy()
	if spec.Affinity != nil || len(spec.TopologySpreadConstraints) > 0 || len(spec.SchedulingGates) > 0 || len(spec.ResourceClaims) > 0 || (spec.SchedulerName != "" && spec.SchedulerName != "default-scheduler") || spec.HostNetwork {
		return nil, nil
	}
	for _, c := range append(append([]corev1.Container{}, spec.Containers...), spec.InitContainers...) {
		for _, p := range c.Ports {
			if p.HostPort != 0 {
				return nil, nil
			}
		}
	}
	if spec.RuntimeClassName != nil {
		raw, err := api.Resource(schema.GroupVersionResource{Group: "node.k8s.io", Version: "v1", Resource: "runtimeclasses"}).Get(ctx, *spec.RuntimeClassName, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		var class nodev1.RuntimeClass
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(raw.Object, &class); err != nil {
			return nil, err
		}
		if class.Overhead != nil {
			if len(spec.Overhead) > 0 {
				return nil, nil
			}
			spec.Overhead = class.Overhead.PodFixed
		}
		if class.Scheduling != nil {
			if spec.NodeSelector == nil {
				spec.NodeSelector = map[string]string{}
			}
			for k, v := range class.Scheduling.NodeSelector {
				if existing, ok := spec.NodeSelector[k]; ok && existing != v {
					return nil, nil
				}
				spec.NodeSelector[k] = v
			}
			spec.Tolerations = append(spec.Tolerations, class.Scheduling.Tolerations...)
		}
	}
	requested := resourcehelper.PodRequests(&corev1.Pod{Spec: *spec}, resourcehelper.PodResourcesOptions{})
	for key, q := range requested {
		if key != corev1.ResourceCPU && key != corev1.ResourceMemory && q.Sign() != 0 {
			return nil, nil
		}
	}
	if requested.Cpu().MilliValue() <= 0 || requested.Memory().Value() <= 0 {
		return nil, nil
	}
	nodeAPI := api.Resource(schema.GroupVersionResource{Version: "v1", Resource: "nodes"})
	var nodes []unstructured.Unstructured
	nodeName := spec.NodeName
	if nodeName == "" {
		nodeName = spec.NodeSelector[corev1.LabelHostname]
	}
	if nodeName != "" {
		node, err := nodeAPI.Get(ctx, nodeName, metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, *node)
	} else {
		if len(spec.NodeSelector) == 0 {
			return nil, nil
		}
		list, err := nodeAPI.List(ctx, metav1.ListOptions{LabelSelector: labels.SelectorFromSet(spec.NodeSelector).String(), Limit: 129})
		if err != nil {
			return nil, err
		}
		if list.GetContinue() != "" || len(list.Items) > 128 {
			return nil, errors.New("incomplete node capacity inventory")
		}
		nodes = list.Items
	}
	result := &RequestCapacity{Profile: name, SampledAt: started}
	for _, raw := range nodes {
		var node corev1.Node
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(raw.Object, &node); err != nil {
			return nil, err
		}
		if !labels.SelectorFromSet(spec.NodeSelector).Matches(labels.Set(node.Labels)) || node.Spec.Unschedulable || node.DeletionTimestamp != nil {
			continue
		}
		ready := false
		pressure := false
		for _, condition := range node.Status.Conditions {
			if condition.Type == corev1.NodeReady && condition.Status == corev1.ConditionTrue {
				ready = true
			}
			if (condition.Type == corev1.NodeMemoryPressure || condition.Type == corev1.NodeDiskPressure || condition.Type == corev1.NodePIDPressure) && condition.Status == corev1.ConditionTrue {
				pressure = true
			}
		}
		if !ready || pressure {
			continue
		}
		_, untolerated := scheduling.FindMatchingUntoleratedTaint(klog.Background(), node.Spec.Taints, spec.Tolerations, func(t *corev1.Taint) bool {
			return t.Effect == corev1.TaintEffectNoSchedule || t.Effect == corev1.TaintEffectNoExecute
		}, false)
		if untolerated {
			continue
		}
		list, err := api.Resource(schema.GroupVersionResource{Version: "v1", Resource: "pods"}).List(ctx, metav1.ListOptions{FieldSelector: "spec.nodeName=" + node.Name, Limit: 10001})
		if err != nil {
			return nil, err
		}
		if list.GetContinue() != "" || len(list.Items) > 10000 {
			return nil, errors.New("incomplete pod capacity inventory")
		}
		pods := make([]corev1.Pod, 0, len(list.Items))
		for _, rawPod := range list.Items {
			var pod corev1.Pod
			if err := runtime.DefaultUnstructuredConverter.FromUnstructured(rawPod.Object, &pod); err != nil {
				return nil, err
			}
			pods = append(pods, pod)
		}
		slots, err := nodeRequestSlots(node, requested, pods)
		if err != nil {
			return nil, err
		}
		result.AvailableSlots += slots
	}
	return result, nil
}

func nodeRequestSlots(node corev1.Node, requested corev1.ResourceList, pods []corev1.Pod) (int64, error) {
	cpu := node.Status.Allocatable.Cpu().MilliValue()
	memory := node.Status.Allocatable.Memory().Value()
	count := node.Status.Allocatable.Pods().Value()
	if cpu <= 0 || memory <= 0 || count <= 0 {
		return 0, errors.New("node allocatable requests unavailable")
	}
	for _, pod := range pods {
		if pod.Spec.NodeName != node.Name || pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}
		used := resourcehelper.PodRequests(&pod, resourcehelper.PodResourcesOptions{
			UseStatusResources: true,
			// Populated status allocations must remain reserved during a pod-level resize.
			InPlacePodLevelResourcesVerticalScalingEnabled: pod.Status.Resources != nil || len(pod.Status.AllocatedResources) > 0,
		})
		cpu -= used.Cpu().MilliValue()
		memory -= used.Memory().Value()
		count--
	}
	return max(int64(0), min(cpu/requested.Cpu().MilliValue(), memory/requested.Memory().Value(), count)), nil
}
