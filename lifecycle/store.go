package lifecycle

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jake-molnia/agent-runtime/sandbox"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"strconv"
)

const stateAnnotation = "agent-runtime/t3-lifecycle"

var secrets = schema.GroupVersionResource{Version: "v1", Resource: "secrets"}

func digest(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
func (c *Controller) token(workspace string, epoch uint64) string {
	mac := hmac.New(sha256.New, c.SecretKey)
	b, _ := json.Marshal([]any{"t3-worker-v1", workspace, epoch})
	mac.Write(b)
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}
func (c *Controller) spec(profile, workspace string, epoch uint64) (sandbox.WorkspaceSpec, error) {
	p, ok := c.Profiles[profile]
	if !ok {
		return sandbox.WorkspaceSpec{}, errors.New("unknown execution profile")
	}
	pod := p.PodTemplate.DeepCopy()
	allocation := strconv.FormatUint(epoch, 10)
	secretName := sandbox.WorkspaceSandboxName(workspace, allocation) + "-auth"
	for i := range pod.Spec.Containers {
		container := &pod.Spec.Containers[i]
		if container.Name != p.WorkerContainer {
			continue
		}
		for _, e := range container.Env {
			switch e.Name {
			case "T3_WORKER_TOKEN", "T3_WORKSPACE_ID", "T3_ALLOCATION_ID", "T3_ALLOCATION_GENERATION", "T3_POD_UID":
				return sandbox.WorkspaceSpec{}, errors.New("profile overrides managed worker environment")
			}
		}
		container.Env = append(container.Env, corev1.EnvVar{Name: "T3_WORKER_TOKEN", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: secretName}, Key: "token"}}}, corev1.EnvVar{Name: "T3_WORKSPACE_ID", Value: workspace}, corev1.EnvVar{Name: "T3_ALLOCATION_ID", Value: allocation}, corev1.EnvVar{Name: "T3_ALLOCATION_GENERATION", Value: allocation}, corev1.EnvVar{Name: "T3_POD_UID", ValueFrom: &corev1.EnvVarSource{FieldRef: &corev1.ObjectFieldSelector{FieldPath: "metadata.uid"}}})
	}
	return sandbox.WorkspaceSpec{Namespace: p.Namespace, WorkspaceID: workspace, AllocationID: allocation, Storage: p.Storage, PodTemplate: *pod}, nil
}
func (c *Controller) load(ctx context.Context, profile, workspace string) (State, *unstructured.Unstructured, error) {
	p, ok := c.Profiles[profile]
	if !ok {
		return State{}, nil, errors.New("unknown profile")
	}
	obj, err := c.Control.API.Resource(sandbox.WorkspacePVCs).Namespace(p.Namespace).Get(ctx, sandbox.WorkspacePVCName(workspace), metav1.GetOptions{})
	if err != nil {
		return State{}, nil, err
	}
	// Control uses JSON-hashed workspace ownership keys.
	if obj.GetLabels()["app.kubernetes.io/managed-by"] != "agent-runtime" || obj.GetAnnotations()["agent-runtime/workspace-key"] != digest(workspace) || len(obj.GetOwnerReferences()) != 0 || obj.GetDeletionTimestamp() != nil {
		return State{}, nil, errors.New("workspace storage ownership mismatch")
	}
	var s State
	if encoded := obj.GetAnnotations()[stateAnnotation]; encoded != "" {
		if err = json.Unmarshal([]byte(encoded), &s); err != nil {
			return State{}, nil, errors.New("invalid persisted lifecycle state")
		}
		if s.Request.Profile != profile || s.ProfileHash != digest(p) {
			return State{}, nil, errors.New("workspace profile changed")
		}
	}
	return s, obj, nil
}
func (c *Controller) save(ctx context.Context, obj *unstructured.Unstructured, s State) error {
	encoded, _ := json.Marshal(s)
	patch, _ := json.Marshal([]map[string]any{{"op": "test", "path": "/metadata/uid", "value": obj.GetUID()}, {"op": "test", "path": "/metadata/resourceVersion", "value": obj.GetResourceVersion()}, {"op": "add", "path": "/metadata/annotations/agent-runtime~1t3-lifecycle", "value": string(encoded)}})
	_, err := c.Control.API.Resource(sandbox.WorkspacePVCs).Namespace(obj.GetNamespace()).Patch(ctx, obj.GetName(), types.JSONPatchType, patch, metav1.PatchOptions{})
	return err
}
func (c *Controller) Admit(ctx context.Context, r Request) (State, error) {
	if err := r.Validate(); err != nil {
		return State{}, err
	}
	spec, err := c.spec(r.Profile, r.WorkspaceID, 1)
	if err != nil {
		return State{}, err
	}
	if r.Action == EnsureRunning {
		if _, err = c.Control.EnsureWorkspaceStorage(ctx, spec); err != nil {
			return State{}, err
		}
	}
	s, obj, err := c.load(ctx, r.Profile, r.WorkspaceID)
	if err != nil {
		return State{}, err
	}
	if s.Request.Revision > r.Revision {
		return s, errors.New("stale lifecycle revision")
	}
	if s.Request.Revision == r.Revision {
		if digest(s.Request) != digest(r) {
			return s, errors.New("lifecycle revision reused with different input")
		}
		return s, nil
	}
	if s.Request.OperationID == r.OperationID {
		return s, errors.New("operation ID reused with different revision")
	}
	s.Request = r
	s.TaskID = ""
	s.ProfileHash = digest(c.Profiles[r.Profile])
	if s.Phase == "" {
		s.Phase = "released"
	}
	if err = c.save(ctx, obj, s); err != nil {
		return State{}, err
	}
	return s, nil
}
func (c *Controller) SetTask(ctx context.Context, r Request, taskID string) error {
	s, obj, err := c.load(ctx, r.Profile, r.WorkspaceID)
	if err != nil {
		return err
	}
	if digest(s.Request) != digest(r) {
		return errors.New("operation superseded")
	}
	s.TaskID = taskID
	return c.save(ctx, obj, s)
}
func (c *Controller) current(ctx context.Context, r Request) (State, error) {
	s, _, err := c.load(ctx, r.Profile, r.WorkspaceID)
	if err != nil {
		return s, err
	}
	if digest(s.Request) != digest(r) {
		return s, errors.New("operation superseded")
	}
	return s, nil
}
func (c *Controller) physical(ctx context.Context, r Request, next State) (State, error) {
	s, obj, err := c.load(ctx, r.Profile, r.WorkspaceID)
	if err != nil {
		return s, err
	}
	next.Request = s.Request
	next.TaskID = s.TaskID
	if err = c.save(ctx, obj, next); err != nil {
		return s, err
	}
	if digest(next.Request) != digest(r) {
		return next, errors.New("operation superseded")
	}
	return next, nil
}
func (c *Controller) ensureSecret(ctx context.Context, s State) error {
	p := c.Profiles[s.Request.Profile]
	name := sandbox.WorkspaceSandboxName(s.Request.WorkspaceID, strconv.FormatUint(s.Epoch, 10)) + "-auth"
	value := base64.StdEncoding.EncodeToString([]byte(c.token(s.Request.WorkspaceID, s.Epoch)))
	obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": name, "namespace": p.Namespace, "labels": map[string]any{"app.kubernetes.io/managed-by": "agent-runtime"}, "annotations": map[string]any{"agent-runtime/workspace-key": digest(s.Request.WorkspaceID)}}, "immutable": true, "type": "Opaque", "data": map[string]any{"token": value}}}
	api := c.Control.API.Resource(secrets).Namespace(p.Namespace)
	actual, err := api.Create(ctx, obj, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		actual, err = api.Get(ctx, name, metav1.GetOptions{})
	}
	if err != nil {
		return errors.New("worker credential provisioning failed")
	}
	token, _, _ := unstructured.NestedString(actual.Object, "data", "token")
	if actual.GetAnnotations()["agent-runtime/workspace-key"] != digest(s.Request.WorkspaceID) || token != value {
		return errors.New("worker credential identity mismatch")
	}
	return nil
}
func (c *Controller) Access(ctx context.Context, profile, workspace string) (Access, error) {
	s, _, err := c.load(ctx, profile, workspace)
	if err != nil {
		return Access{}, err
	}
	a := Access{State: s}
	if s.Phase == "running" && s.Handle != nil && s.Request.Action == EnsureRunning && s.CompletedOperation == s.Request.OperationID {
		observation, err := c.Control.ObserveWorkspace(ctx, *s.Handle)
		if err != nil {
			return Access{}, errors.New("current allocation could not be observed")
		}
		if !observation.Ready || observation.Mode != sandbox.WorkspaceRunning {
			return Access{}, errors.New("current allocation is not ready")
		}
		address := endpoint(observation.Host, c.Profiles[profile].Port)
		token := c.token(workspace, s.Epoch)
		identity, err := c.Worker.Identity(ctx, address, token)
		if err != nil {
			return Access{}, errors.New("current worker could not be authenticated")
		}
		if identity.WorkspaceID != workspace || identity.AllocationID != s.Handle.AllocationID || identity.Generation != s.Epoch || identity.ProtocolVersion != 1 || identity.Incarnation == "" || identity.PodUID != string(observation.PodUID) {
			return Access{}, errors.New("current worker identity mismatch")
		}
		// A worker can restart inside the same allocation. Publish the observed
		// incarnation so the caller expires old callbacks instead of using cached readiness.
		if s.Identity == nil || *s.Identity != identity || s.Endpoint != address {
			s.Identity = &identity
			s.Handle = &observation.Handle
			s.Endpoint = address
			s, err = c.physical(ctx, s.Request, s)
			if err != nil {
				return Access{}, err
			}
		} else if _, err := c.current(ctx, s.Request); err != nil {
			return Access{}, err
		}
		a.State = s
		a.Token = token
	}
	return a, nil
}
func endpoint(host string, port int) string { return fmt.Sprintf("http://%s:%d", host, port) }

func (c *Controller) deleteSecret(ctx context.Context, s State) error {
	p := c.Profiles[s.Request.Profile]
	name := sandbox.WorkspaceSandboxName(s.Request.WorkspaceID, strconv.FormatUint(s.Epoch, 10)) + "-auth"
	api := c.Control.API.Resource(secrets).Namespace(p.Namespace)
	obj, err := api.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return errors.New("worker credential cleanup failed")
	}
	if obj.GetLabels()["app.kubernetes.io/managed-by"] != "agent-runtime" || obj.GetAnnotations()["agent-runtime/workspace-key"] != digest(s.Request.WorkspaceID) {
		return errors.New("worker credential ownership mismatch")
	}
	uid, rv := obj.GetUID(), obj.GetResourceVersion()
	err = api.Delete(ctx, name, metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &rv}})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return errors.New("worker credential cleanup failed")
	}
	return nil
}
