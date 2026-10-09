package sandbox

import (
	"context"
	"errors"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

func workspaceTestControl() (*Control, *fake.FakeDynamicClient) {
	api := fake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{Sandboxes: "SandboxList", WorkspacePVCs: "PersistentVolumeClaimList", workspacePods: "PodList"})
	api.PrependReactor("create", "*", func(a ktesting.Action) (bool, runtime.Object, error) {
		o := a.(ktesting.CreateAction).GetObject().(*unstructured.Unstructured)
		o.SetUID(types.UID(o.GetName() + "-uid"))
		o.SetResourceVersion("1")
		o.SetGeneration(1)
		return false, nil, nil
	})
	return &Control{API: api}, api
}
func workspaceTestSpec() WorkspaceSpec {
	return WorkspaceSpec{Namespace: "test", WorkspaceID: "environment/thread", AllocationID: "1", Storage: StorageSpec{Size: "1Gi", StorageClass: "fast", AccessModes: []string{"ReadWriteOnce"}}, PodTemplate: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "worker", Image: "worker@sha256:abc", VolumeMounts: []corev1.VolumeMount{{Name: "workspace", MountPath: "/workspace"}}}}}}}
}
func TestWorkspaceRetainedPVCAndDuplicateEnsure(t *testing.T) {
	ctx := context.Background()
	c, api := workspaceTestControl()
	s := workspaceTestSpec()
	h, err := c.EnsureWorkspace(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	again, err := c.EnsureWorkspace(ctx, s)
	if err != nil || again.UID != h.UID || again.PVCUID != h.PVCUID {
		t.Fatalf("retry changed identity: %+v %v", again, err)
	}
	sb, _ := api.Resource(Sandboxes).Namespace(s.Namespace).Get(ctx, h.Sandbox, metav1.GetOptions{})
	if v, _, _ := unstructured.NestedBool(sb.Object, "spec", "service"); !v {
		t.Fatal("stable service missing")
	}
	if _, ok, _ := unstructured.NestedFieldNoCopy(sb.Object, "spec", "volumeClaimTemplates"); ok {
		t.Fatal("controller-owned PVC templates present")
	}
	pvc, _ := api.Resource(WorkspacePVCs).Namespace(s.Namespace).Get(ctx, h.PVC, metav1.GetOptions{})
	if len(pvc.GetOwnerReferences()) != 0 {
		t.Fatal("PVC has transient owner")
	}
	if err = c.DeleteWorkspaceSandbox(ctx, h); err != nil {
		t.Fatal(err)
	}
	if err = c.WaitWorkspaceAbsent(ctx, h); err != nil {
		t.Fatal(err)
	}
	if _, err = api.Resource(WorkspacePVCs).Namespace(s.Namespace).Get(ctx, h.PVC, metav1.GetOptions{}); err != nil {
		t.Fatalf("compute deletion removed storage: %v", err)
	}
	s.AllocationID = "2"
	next, err := c.EnsureWorkspace(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	if next.PVCUID != h.PVCUID || next.Sandbox == h.Sandbox {
		t.Fatal("replacement did not retain storage with distinct compute")
	}
	for _, a := range api.Actions() {
		if a.GetVerb() == "delete" && a.GetResource() == WorkspacePVCs {
			t.Fatal("compute operation deletes PVC")
		}
		if a.GetVerb() == "delete" && a.GetResource() == Sandboxes {
			pre := a.(ktesting.DeleteAction).GetDeleteOptions().Preconditions
			if pre == nil || pre.UID == nil || *pre.UID != h.UID || pre.ResourceVersion == nil || *pre.ResourceVersion != h.ResourceVersion {
				t.Fatal("delete lacks UID/resource-version fence")
			}
		}
	}
}
func TestWorkspaceEnsureRejectsMismatchAndDrift(t *testing.T) {
	for _, kind := range []string{"image", "storage", "owner", "spec", "pvc-replaced"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			c, api := workspaceTestControl()
			s := workspaceTestSpec()
			h, err := c.EnsureWorkspace(ctx, s)
			if err != nil {
				t.Fatal(err)
			}
			switch kind {
			case "image":
				s.PodTemplate.Spec.Containers[0].Image = "other"
			case "storage":
				s.Storage.Size = "2Gi"
			case "owner":
				p, _ := api.Resource(WorkspacePVCs).Namespace(h.Namespace).Get(ctx, h.PVC, metav1.GetOptions{})
				p.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "v1", Kind: "Pod", Name: "ephemeral", UID: "pod"}})
				api.Resource(WorkspacePVCs).Namespace(h.Namespace).Update(ctx, p, metav1.UpdateOptions{})
			case "spec":
				p, _ := api.Resource(Sandboxes).Namespace(h.Namespace).Get(ctx, h.Sandbox, metav1.GetOptions{})
				unstructured.SetNestedField(p.Object, false, "spec", "service")
				api.Resource(Sandboxes).Namespace(h.Namespace).Update(ctx, p, metav1.UpdateOptions{})
			case "pvc-replaced":
				p, _ := api.Resource(WorkspacePVCs).Namespace(h.Namespace).Get(ctx, h.PVC, metav1.GetOptions{})
				p.SetUID("replacement")
				api.Resource(WorkspacePVCs).Namespace(h.Namespace).Update(ctx, p, metav1.UpdateOptions{})
			}
			if _, err = c.EnsureWorkspace(ctx, s); err == nil {
				t.Fatal("mismatch accepted")
			}
		})
	}
}
func setWorkspaceCondition(t *testing.T, api *fake.FakeDynamicClient, h WorkspaceHandle, mode WorkspaceMode, kind string, generation, observed int64) WorkspaceHandle {
	t.Helper()
	ctx := context.Background()
	o, err := api.Resource(Sandboxes).Namespace(h.Namespace).Get(ctx, h.Sandbox, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	o.SetGeneration(generation)
	o.SetResourceVersion("2")
	unstructured.SetNestedField(o.Object, string(mode), "spec", "operatingMode")
	o.Object["status"] = map[string]any{"serviceFQDN": "worker.test.svc", "conditions": []any{map[string]any{"type": kind, "status": "True", "observedGeneration": observed}}}
	if _, err = api.Resource(Sandboxes).Namespace(h.Namespace).Update(ctx, o, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	h.Generation = generation
	h.ResourceVersion = "2"
	return h
}
func workspacePod(h WorkspaceHandle) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"name": "worker", "namespace": h.Namespace, "uid": "worker-uid", "ownerReferences": []any{map[string]any{"apiVersion": "agents.x-k8s.io/v1beta1", "kind": "Sandbox", "name": h.Sandbox, "uid": string(h.UID)}}}, "status": map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True"}}}}}
}
func TestWorkspaceReadinessAndSuspensionRequireCurrentFacts(t *testing.T) {
	ctx := context.Background()
	c, api := workspaceTestControl()
	h, err := c.EnsureWorkspace(ctx, workspaceTestSpec())
	if err != nil {
		t.Fatal(err)
	}
	h = setWorkspaceCondition(t, api, h, WorkspaceRunning, "Ready", 2, 1)
	api.Resource(workspacePods).Namespace(h.Namespace).Create(ctx, workspacePod(h), metav1.CreateOptions{})
	o, err := c.ObserveWorkspace(ctx, h)
	if err != nil || o.Ready {
		t.Fatalf("stale Ready accepted %+v %v", o, err)
	}
	h = setWorkspaceCondition(t, api, h, WorkspaceRunning, "Ready", 2, 2)
	o, err = c.ObserveWorkspace(ctx, h)
	if err != nil || !o.Ready || o.PodUID == "" {
		t.Fatalf("current readiness missing %+v %v", o, err)
	}
	h = setWorkspaceCondition(t, api, h, WorkspaceSuspended, "Suspended", 3, 2)
	o, err = c.ObserveWorkspace(ctx, h)
	if err != nil || o.Suspended {
		t.Fatal("stale suspension accepted")
	}
	h = setWorkspaceCondition(t, api, h, WorkspaceSuspended, "Suspended", 3, 3)
	o, err = c.ObserveWorkspace(ctx, h)
	if err != nil || o.Suspended {
		t.Fatal("suspension accepted with live pod")
	}
	api.Resource(workspacePods).Namespace(h.Namespace).Delete(ctx, "worker", metav1.DeleteOptions{})
	o, err = c.WaitWorkspaceMode(ctx, h, WorkspaceSuspended)
	if err != nil || !o.Suspended {
		t.Fatalf("suspension not observed %+v %v", o, err)
	}
}
func TestWorkspaceMutationsRejectStaleHandles(t *testing.T) {
	for _, kind := range []string{"version", "generation", "uid"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			c, api := workspaceTestControl()
			h, err := c.EnsureWorkspace(ctx, workspaceTestSpec())
			if err != nil {
				t.Fatal(err)
			}
			o, _ := api.Resource(Sandboxes).Namespace(h.Namespace).Get(ctx, h.Sandbox, metav1.GetOptions{})
			switch kind {
			case "version":
				o.SetResourceVersion("newer")
			case "generation":
				o.SetGeneration(3)
				o.SetResourceVersion("newer")
			case "uid":
				o.SetUID("replacement")
			}
			api.Resource(Sandboxes).Namespace(h.Namespace).Update(ctx, o, metav1.UpdateOptions{})
			if _, err = c.SetWorkspaceMode(ctx, h, WorkspaceSuspended); err == nil {
				t.Fatal("stale scale accepted")
			}
			if err = c.DeleteWorkspaceSandbox(ctx, h); err == nil {
				t.Fatal("stale deletion accepted")
			}
		})
	}
}
func TestWorkspaceModePatchTestsIdentityAndVersion(t *testing.T) {
	ctx := context.Background()
	c, api := workspaceTestControl()
	h, err := c.EnsureWorkspace(ctx, workspaceTestSpec())
	if err != nil {
		t.Fatal(err)
	}
	h, err = c.SetWorkspaceMode(ctx, h, WorkspaceSuspended)
	if err != nil {
		t.Fatal(err)
	}
	o, _ := api.Resource(Sandboxes).Namespace(h.Namespace).Get(ctx, h.Sandbox, metav1.GetOptions{})
	mode, _, _ := unstructured.NestedString(o.Object, "spec", "operatingMode")
	if mode != "Suspended" {
		t.Fatal("mode not changed")
	}
	if _, err = c.WaitWorkspaceMode(ctx, h, WorkspaceRunning); err == nil {
		t.Fatal("wait accepted conflicting desired mode")
	}
}
func TestWorkspaceInputValidation(t *testing.T) {
	for _, kind := range []string{"namespace", "size", "class", "mode", "mount"} {
		t.Run(kind, func(t *testing.T) {
			s := workspaceTestSpec()
			switch kind {
			case "namespace":
				s.Namespace = ""
			case "size":
				s.Storage.Size = "0"
			case "class":
				s.Storage.StorageClass = ""
			case "mode":
				s.Storage.AccessModes = []string{"ReadOnlyMany"}
			case "mount":
				s.PodTemplate.Spec.Containers[0].VolumeMounts = nil
			}
			c, api := workspaceTestControl()
			if _, err := c.EnsureWorkspace(context.Background(), s); err == nil {
				t.Fatal("invalid input accepted")
			}
			if len(api.Actions()) != 0 {
				t.Fatal("invalid input performed side effects")
			}
		})
	}
}
func TestReadyRequiresObservedGeneration(t *testing.T) {
	for _, test := range []struct {
		generation, observed int64
		want                 bool
	}{{2, 1, false}, {2, 2, true}, {0, 0, true}} {
		o := &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"generation": test.generation}, "status": map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True", "observedGeneration": test.observed}}}}}
		got, err := ready(o)
		if err != nil || got != test.want {
			t.Fatalf("generation=%d observed=%d got=%v err=%v", test.generation, test.observed, got, err)
		}
	}
}
func TestWaitReadyRejectsReplacementAfterWatchRelist(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	_, api := workspaceTestControl()
	old := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "agents.x-k8s.io/v1beta1", "kind": "Sandbox", "metadata": map[string]any{"name": "old", "namespace": "test", "uid": "old-uid"}}}
	replacement := old.DeepCopy()
	replacement.SetUID("new-uid")
	replacement.Object["status"] = map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "True"}}}
	api.PrependWatchReactor("sandboxes", func(ktesting.Action) (bool, watch.Interface, error) {
		w := watch.NewFake()
		w.Stop()
		return true, w, nil
	})
	api.PrependReactor("get", "sandboxes", func(ktesting.Action) (bool, runtime.Object, error) { return true, replacement, nil })
	_, err := waitReady(ctx, api.Resource(Sandboxes).Namespace("test"), old)
	if err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("replacement was not rejected: %v", err)
	}
}

func TestWorkspaceModeRejectsConcurrentMutation(t *testing.T) {
	ctx := context.Background()
	c, api := workspaceTestControl()
	h, err := c.EnsureWorkspace(ctx, workspaceTestSpec())
	if err != nil {
		t.Fatal(err)
	}
	api.PrependReactor("patch", "sandboxes", func(a ktesting.Action) (bool, runtime.Object, error) {
		raw, err := api.Tracker().Get(Sandboxes, h.Namespace, h.Sandbox)
		if err != nil {
			return true, nil, err
		}
		o := raw.(*unstructured.Unstructured)
		o.SetResourceVersion("concurrent-change")
		if err = api.Tracker().Update(Sandboxes, o, h.Namespace); err != nil {
			return true, nil, err
		}
		return false, nil, nil
	})
	if _, err = c.SetWorkspaceMode(ctx, h, WorkspaceSuspended); err == nil {
		t.Fatal("concurrent mutation bypassed JSON patch resourceVersion test")
	}
	o, _ := api.Resource(Sandboxes).Namespace(h.Namespace).Get(ctx, h.Sandbox, metav1.GetOptions{})
	mode, _, _ := unstructured.NestedString(o.Object, "spec", "operatingMode")
	if mode != "Running" {
		t.Fatal("stale request suspended newer workspace")
	}
}

func TestWorkspaceWaitingHonorsCancellation(t *testing.T) {
	ctx := context.Background()
	c, _ := workspaceTestControl()
	h, err := c.EnsureWorkspace(ctx, workspaceTestSpec())
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = c.WaitWorkspaceMode(cancelled, h, WorkspaceRunning); !errors.Is(err, context.Canceled) {
		t.Fatalf("want canceled, got %v", err)
	}
}

func TestWorkspaceEnsureAcceptsAPIDefaultsButRejectsChangedPinnedFields(t *testing.T) {
	ctx := context.Background()
	c, api := workspaceTestControl()
	s := workspaceTestSpec()
	s.PodTemplate.Spec.Containers[0].Ports = []corev1.ContainerPort{{ContainerPort: 8080}}
	h, err := c.EnsureWorkspace(ctx, s)
	if err != nil {
		t.Fatal(err)
	}
	obj, _ := api.Resource(Sandboxes).Namespace(h.Namespace).Get(ctx, h.Sandbox, metav1.GetOptions{})
	containers, _, _ := unstructured.NestedSlice(obj.Object, "spec", "podTemplate", "spec", "containers")
	container := containers[0].(map[string]any)
	ports := container["ports"].([]any)
	port := ports[0].(map[string]any)
	port["protocol"] = "TCP"
	unstructured.SetNestedSlice(obj.Object, containers, "spec", "podTemplate", "spec", "containers")
	api.Resource(Sandboxes).Namespace(h.Namespace).Update(ctx, obj, metav1.UpdateOptions{})
	if _, err = c.EnsureWorkspace(ctx, s); err != nil {
		t.Fatalf("API defaults rejected: %v", err)
	}
	port["containerPort"] = int64(9999)
	unstructured.SetNestedSlice(obj.Object, containers, "spec", "podTemplate", "spec", "containers")
	api.Resource(Sandboxes).Namespace(h.Namespace).Update(ctx, obj, metav1.UpdateOptions{})
	if _, err = c.EnsureWorkspace(ctx, s); err == nil {
		t.Fatal("changed pinned port accepted")
	}
}
