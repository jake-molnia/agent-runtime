package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
)

var WorkspacePVCs = schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"}
var workspacePods = schema.GroupVersionResource{Version: "v1", Resource: "pods"}

type WorkspaceMode string

const (
	WorkspaceRunning   WorkspaceMode = "Running"
	WorkspaceSuspended WorkspaceMode = "Suspended"
)

type StorageSpec struct {
	Size         string   `json:"size"`
	StorageClass string   `json:"storageClass"`
	AccessModes  []string `json:"accessModes"`
}
type WorkspaceSpec struct {
	Namespace    string                 `json:"namespace"`
	WorkspaceID  string                 `json:"workspaceId"`
	AllocationID string                 `json:"allocationId"`
	Storage      StorageSpec            `json:"storage"`
	PodTemplate  corev1.PodTemplateSpec `json:"podTemplate"`
}

// WorkspaceHandle addresses direct resources, never an extension claim.
type WorkspaceHandle struct {
	Namespace       string    `json:"namespace"`
	WorkspaceID     string    `json:"workspaceId"`
	AllocationID    string    `json:"allocationId"`
	PVC             string    `json:"pvc"`
	PVCUID          types.UID `json:"pvcUid"`
	PVCSpecHash     string    `json:"pvcSpecHash"`
	Sandbox         string    `json:"sandbox"`
	UID             types.UID `json:"uid"`
	Generation      int64     `json:"generation"`
	ResourceVersion string    `json:"resourceVersion"`
	SpecHash        string    `json:"specHash"`
}
type WorkspaceObservation struct {
	Handle    WorkspaceHandle `json:"handle"`
	Mode      WorkspaceMode   `json:"mode"`
	Ready     bool            `json:"ready"`
	Suspended bool            `json:"suspended"`
	Host      string          `json:"host"`
	PodName   string          `json:"podName"`
	PodUID    types.UID       `json:"podUid"`
}

func workspaceHash(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func WorkspacePVCName(id string) string { return "ar-ws-" + workspaceHash(id)[:32] }
func WorkspaceSandboxName(workspace, allocation string) string {
	return "ar-ex-" + workspaceHash([]string{workspace, allocation})[:32]
}
func (s WorkspaceSpec) Validate() error {
	if len(validation.IsDNS1123Label(s.Namespace)) != 0 || s.WorkspaceID == "" || s.AllocationID == "" {
		return errors.New("workspace requires explicit namespace, workspace ID and allocation ID")
	}
	q, err := resource.ParseQuantity(s.Storage.Size)
	if err != nil || q.Sign() <= 0 {
		return errors.New("workspace storage size must be positive")
	}
	if s.Storage.StorageClass == "" || len(validation.IsDNS1123Subdomain(s.Storage.StorageClass)) != 0 {
		return errors.New("workspace requires explicit storage class")
	}
	if len(s.Storage.AccessModes) == 0 {
		return errors.New("workspace storage access modes required")
	}
	seen := map[string]bool{}
	for _, m := range s.Storage.AccessModes {
		if seen[m] || (m != "ReadWriteOnce" && m != "ReadWriteOncePod" && m != "ReadWriteMany") {
			return fmt.Errorf("invalid workspace access mode %q", m)
		}
		seen[m] = true
	}
	if len(s.PodTemplate.Spec.Containers) == 0 {
		return errors.New("workspace pod requires containers")
	}
	for _, v := range s.PodTemplate.Spec.Volumes {
		if v.Name == "workspace" {
			return errors.New("workspace volume is managed by agent-runtime")
		}
	}
	mounted := false
	for _, c := range s.PodTemplate.Spec.Containers {
		if c.Image == "" {
			return errors.New("workspace container requires image")
		}
		for _, m := range c.VolumeMounts {
			if m.Name == "workspace" && m.MountPath != "" {
				mounted = true
			}
		}
	}
	if !mounted {
		return errors.New("workspace pod requires workspace volume mount")
	}
	return nil
}
func ownedWorkspaceObject(kind, name, namespace, workspace, hash string, spec map[string]any) *unstructured.Unstructured {
	version := "v1"
	if kind == "Sandbox" {
		version = "agents.x-k8s.io/v1beta1"
	}
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": version, "kind": kind, "metadata": map[string]any{"name": name, "namespace": namespace, "labels": map[string]any{"app.kubernetes.io/managed-by": "agent-runtime"}, "annotations": map[string]any{"agent-runtime/workspace-key": workspaceHash(workspace), "agent-runtime/spec-sha256": hash}}, "spec": spec}}
}
func verifyWorkspaceObject(obj *unstructured.Unstructured, workspace, hash string) error {
	if obj.GetDeletionTimestamp() != nil {
		return errors.New("workspace resource is deleting")
	}
	if obj.GetLabels()["app.kubernetes.io/managed-by"] != "agent-runtime" || obj.GetAnnotations()["agent-runtime/workspace-key"] != workspaceHash(workspace) || obj.GetAnnotations()["agent-runtime/spec-sha256"] != hash || len(obj.GetOwnerReferences()) != 0 {
		return errors.New("workspace resource ownership or spec mismatch")
	}
	return nil
}

// EnsureWorkspaceStorage establishes retained storage without creating compute.
func (c *Control) EnsureWorkspaceStorage(ctx context.Context, s WorkspaceSpec) (*unstructured.Unstructured, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	q, _ := resource.ParseQuantity(s.Storage.Size)
	modes := make([]any, len(s.Storage.AccessModes))
	for i, m := range s.Storage.AccessModes {
		modes[i] = m
	}
	pvcSpec := map[string]any{"storageClassName": s.Storage.StorageClass, "accessModes": modes, "resources": map[string]any{"requests": map[string]any{"storage": q.String()}}}
	pvcHash := workspaceHash(pvcSpec)
	pvcName := WorkspacePVCName(s.WorkspaceID)
	pvcAPI := c.API.Resource(WorkspacePVCs).Namespace(s.Namespace)
	pvc, err := pvcAPI.Create(ctx, ownedWorkspaceObject("PersistentVolumeClaim", pvcName, s.Namespace, s.WorkspaceID, pvcHash, pvcSpec), metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		pvc, err = pvcAPI.Get(ctx, pvcName, metav1.GetOptions{})
	}
	if err != nil {
		return nil, err
	}
	if err = verifyWorkspaceObject(pvc, s.WorkspaceID, pvcHash); err != nil {
		return nil, err
	}
	// Check the requested subset, permitting Kubernetes binding/default fields.
	for _, path := range [][]string{{"spec", "storageClassName"}, {"spec", "accessModes"}, {"spec", "resources", "requests", "storage"}} {
		got, _, _ := unstructured.NestedFieldNoCopy(pvc.Object, path...)
		want, _, _ := unstructured.NestedFieldNoCopy(map[string]any{"spec": pvcSpec}, path...)
		if workspaceHash(got) != workspaceHash(want) {
			return nil, errors.New("workspace PVC spec drift")
		}
	}
	return pvc, nil
}

// EnsureWorkspace creates compute only after verifying its retained storage.
// Callers serialize allocations and await old compute deletion before replacement.
func (c *Control) EnsureWorkspace(ctx context.Context, s WorkspaceSpec) (WorkspaceHandle, error) {
	if err := s.Validate(); err != nil {
		return WorkspaceHandle{}, err
	}
	pvc, err := c.EnsureWorkspaceStorage(ctx, s)
	if err != nil {
		return WorkspaceHandle{}, err
	}
	pvcName := pvc.GetName()
	pvcHash := pvc.GetAnnotations()["agent-runtime/spec-sha256"]
	pod := s.PodTemplate.DeepCopy()
	disabled := false
	pod.Spec.AutomountServiceAccountToken = &disabled
	pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{Name: "workspace", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: pvcName}}})
	template, err := runtime.DefaultUnstructuredConverter.ToUnstructured(pod)
	if err != nil {
		return WorkspaceHandle{}, err
	}
	spec := map[string]any{"podTemplate": template, "service": true, "operatingMode": string(WorkspaceRunning)}
	hash := workspaceHash(map[string]any{"podTemplate": template, "service": true})
	name := WorkspaceSandboxName(s.WorkspaceID, s.AllocationID)
	obj := ownedWorkspaceObject("Sandbox", name, s.Namespace, s.WorkspaceID, hash, spec)
	annotations := obj.GetAnnotations()
	annotations["agent-runtime/pvc-uid"] = string(pvc.GetUID())
	requested, _ := json.Marshal(map[string]any{"podTemplate": template, "service": true})
	annotations["agent-runtime/requested-spec"] = string(requested)
	obj.SetAnnotations(annotations)
	api := c.API.Resource(Sandboxes).Namespace(s.Namespace)
	sb, err := api.Create(ctx, obj, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		sb, err = api.Get(ctx, name, metav1.GetOptions{})
	}
	if err != nil {
		return WorkspaceHandle{}, err
	}
	h := WorkspaceHandle{Namespace: s.Namespace, WorkspaceID: s.WorkspaceID, AllocationID: s.AllocationID, PVC: pvcName, PVCUID: pvc.GetUID(), PVCSpecHash: pvcHash, Sandbox: name, UID: sb.GetUID(), Generation: sb.GetGeneration(), ResourceVersion: sb.GetResourceVersion(), SpecHash: hash}
	if err = checkWorkspaceSandbox(sb, h); err != nil {
		return WorkspaceHandle{}, err
	}
	return h, nil
}
func checkWorkspaceSandbox(obj *unstructured.Unstructured, h WorkspaceHandle) error {
	for _, field := range []string{"volumeClaimTemplates", "shutdownTime"} {
		if _, found, _ := unstructured.NestedFieldNoCopy(obj.Object, "spec", field); found {
			return fmt.Errorf("workspace sandbox has unmanaged %s", field)
		}
	}
	if err := verifyWorkspaceObject(obj, h.WorkspaceID, h.SpecHash); err != nil {
		return err
	}
	if obj.GetUID() != h.UID || obj.GetAnnotations()["agent-runtime/pvc-uid"] != string(h.PVCUID) {
		return errors.New("workspace resource identity changed")
	}
	var requested map[string]any
	if err := json.Unmarshal([]byte(obj.GetAnnotations()["agent-runtime/requested-spec"]), &requested); err != nil || workspaceHash(requested) != h.SpecHash {
		return errors.New("workspace requested spec identity changed")
	}
	actual, _, _ := unstructured.NestedMap(obj.Object, "spec")
	if !containsRequestedSpec(actual, requested) {
		return errors.New("workspace sandbox spec drift")
	}
	return nil
}

// API admission defaults map fields such as container port protocol. Verify all
// pinned fields and exact list structure while allowing those added defaults.
func containsRequestedSpec(actual, requested any) bool {
	switch want := requested.(type) {
	case map[string]any:
		got, ok := actual.(map[string]any)
		if !ok {
			return len(want) == 0 && actual == nil
		}
		for key, value := range want {
			if !containsRequestedSpec(got[key], value) {
				return false
			}
		}
		return true
	case []any:
		got, ok := actual.([]any)
		if !ok || len(got) != len(want) {
			return false
		}
		for i, value := range want {
			if !containsRequestedSpec(got[i], value) {
				return false
			}
		}
		return true
	default:
		return workspaceHash(actual) == workspaceHash(requested)
	}
}

func currentCondition(obj *unstructured.Unstructured, kind string) bool {
	conditions, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	for _, v := range conditions {
		m, ok := v.(map[string]any)
		if !ok {
			continue
		}
		generation, _, _ := unstructured.NestedInt64(m, "observedGeneration")
		if m["type"] == kind && m["status"] == "True" && generation == obj.GetGeneration() {
			return true
		}
	}
	return false
}
func (c *Control) ObserveWorkspace(ctx context.Context, h WorkspaceHandle) (WorkspaceObservation, error) {
	obj, err := c.API.Resource(Sandboxes).Namespace(h.Namespace).Get(ctx, h.Sandbox, metav1.GetOptions{})
	if err != nil {
		return WorkspaceObservation{}, err
	}
	if err = checkWorkspaceSandbox(obj, h); err != nil {
		return WorkspaceObservation{}, err
	}
	if obj.GetGeneration() < h.Generation {
		return WorkspaceObservation{}, errors.New("workspace generation regressed")
	}
	pvc, err := c.API.Resource(WorkspacePVCs).Namespace(h.Namespace).Get(ctx, h.PVC, metav1.GetOptions{})
	if err != nil {
		return WorkspaceObservation{}, err
	}
	if err := verifyWorkspaceObject(pvc, h.WorkspaceID, h.PVCSpecHash); err != nil {
		return WorkspaceObservation{}, err
	}
	if pvc.GetUID() != h.PVCUID || pvc.GetDeletionTimestamp() != nil || len(pvc.GetOwnerReferences()) != 0 {
		return WorkspaceObservation{}, errors.New("workspace storage identity changed")
	}
	h.Generation = obj.GetGeneration()
	h.ResourceVersion = obj.GetResourceVersion()
	mode, _, _ := unstructured.NestedString(obj.Object, "spec", "operatingMode")
	host, _, _ := unstructured.NestedString(obj.Object, "status", "serviceFQDN")
	o := WorkspaceObservation{Handle: h, Mode: WorkspaceMode(mode), Host: host}
	pods, err := c.API.Resource(workspacePods).Namespace(h.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return o, err
	}
	count := 0
	podReady := false
	for _, p := range pods.Items {
		for _, owner := range p.GetOwnerReferences() {
			if owner.UID == h.UID && owner.Kind == "Sandbox" {
				count++
				o.PodName = p.GetName()
				o.PodUID = p.GetUID()
				conditions, _, _ := unstructured.NestedSlice(p.Object, "status", "conditions")
				for _, v := range conditions {
					m, ok := v.(map[string]any)
					if ok && m["type"] == "Ready" && m["status"] == "True" && p.GetDeletionTimestamp() == nil {
						podReady = true
					}
				}
				break
			}
		}
	}
	o.Ready = o.Mode == WorkspaceRunning && currentCondition(obj, "Ready") && count == 1 && podReady && host != ""
	o.Suspended = o.Mode == WorkspaceSuspended && currentCondition(obj, "Suspended") && count == 0
	return o, nil
}
func (c *Control) SetWorkspaceMode(ctx context.Context, h WorkspaceHandle, mode WorkspaceMode) (WorkspaceHandle, error) {
	if mode != WorkspaceRunning && mode != WorkspaceSuspended {
		return h, errors.New("invalid workspace mode")
	}
	obj, err := c.API.Resource(Sandboxes).Namespace(h.Namespace).Get(ctx, h.Sandbox, metav1.GetOptions{})
	if err != nil {
		return h, err
	}
	if err = checkWorkspaceSandbox(obj, h); err != nil {
		return h, err
	}
	if obj.GetResourceVersion() != h.ResourceVersion || obj.GetGeneration() != h.Generation {
		return h, errors.New("stale workspace handle")
	}
	patch, _ := json.Marshal([]map[string]any{{"op": "test", "path": "/metadata/uid", "value": h.UID}, {"op": "test", "path": "/metadata/resourceVersion", "value": h.ResourceVersion}, {"op": "add", "path": "/spec/operatingMode", "value": string(mode)}})
	obj, err = c.API.Resource(Sandboxes).Namespace(h.Namespace).Patch(ctx, h.Sandbox, types.JSONPatchType, patch, metav1.PatchOptions{})
	if err != nil {
		return h, err
	}
	h.Generation = obj.GetGeneration()
	h.ResourceVersion = obj.GetResourceVersion()
	return h, nil
}

// DeleteWorkspaceSandbox retains the workspace PVC. Absence is acknowledged separately.
func (c *Control) DeleteWorkspaceSandbox(ctx context.Context, h WorkspaceHandle) error {
	obj, err := c.API.Resource(Sandboxes).Namespace(h.Namespace).Get(ctx, h.Sandbox, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	// A retry may observe the already-deleting resource; immutable identity still fences it.
	if obj.GetDeletionTimestamp() == nil {
		if err = checkWorkspaceSandbox(obj, h); err != nil {
			return err
		}
	}
	if obj.GetUID() != h.UID {
		return errors.New("workspace resource identity changed")
	}
	if obj.GetDeletionTimestamp() != nil {
		return nil
	}
	if obj.GetResourceVersion() != h.ResourceVersion {
		return errors.New("stale workspace handle")
	}
	policy := metav1.DeletePropagationForeground
	err = c.API.Resource(Sandboxes).Namespace(h.Namespace).Delete(ctx, h.Sandbox, metav1.DeleteOptions{PropagationPolicy: &policy, Preconditions: &metav1.Preconditions{UID: &h.UID, ResourceVersion: &h.ResourceVersion}})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}
func (c *Control) WaitWorkspaceMode(ctx context.Context, h WorkspaceHandle, mode WorkspaceMode) (WorkspaceObservation, error) {
	if mode != WorkspaceRunning && mode != WorkspaceSuspended {
		return WorkspaceObservation{}, errors.New("invalid workspace mode")
	}
	for {
		o, err := c.ObserveWorkspace(ctx, h)
		if err != nil {
			return o, err
		}
		if o.Handle.Generation != h.Generation || o.Mode != mode {
			return o, errors.New("workspace desired mode changed while waiting")
		}
		if mode == WorkspaceRunning && o.Ready || mode == WorkspaceSuspended && o.Suspended {
			return o, nil
		}
		select {
		case <-ctx.Done():
			return o, ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}
func (c *Control) WaitWorkspaceAbsent(ctx context.Context, h WorkspaceHandle) error {
	for {
		obj, err := c.API.Resource(Sandboxes).Namespace(h.Namespace).Get(ctx, h.Sandbox, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if obj.GetUID() != h.UID {
			return errors.New("workspace resource replaced during deletion")
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(250 * time.Millisecond):
		}
	}
}
