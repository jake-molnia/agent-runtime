// Package sandbox manages durable claims and exposes the complete sandboxd process and file APIs.
package sandbox

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
	ext "sigs.k8s.io/agent-sandbox/extensions/api/v1beta1"
)

var Claims = schema.GroupVersionResource{Group: "extensions.agents.x-k8s.io", Version: "v1beta1", Resource: "sandboxclaims"}
var Sandboxes = schema.GroupVersionResource{Group: "agents.x-k8s.io", Version: "v1beta1", Resource: "sandboxes"}
var Templates = schema.GroupVersionResource{Group: "extensions.agents.x-k8s.io", Version: "v1beta1", Resource: "sandboxtemplates"}
var WarmPools = schema.GroupVersionResource{Group: "extensions.agents.x-k8s.io", Version: "v1beta1", Resource: "sandboxwarmpools"}

type Control struct{ API dynamic.Interface }
type Lease struct {
	Namespace string    `json:"namespace"`
	Claim     string    `json:"claim"`
	UID       types.UID `json:"uid"`
	Sandbox   string    `json:"sandbox"`
	Host      string    `json:"host"`
	Launch    string    `json:"launch"`
	Expires   time.Time `json:"expires"`
}

func Config() (*rest.Config, error) {
	c, err := rest.InClusterConfig()
	if err == nil {
		return c, nil
	}
	return clientcmd.NewNonInteractiveDeferredLoadingClientConfig(clientcmd.NewDefaultClientConfigLoadingRules(), &clientcmd.ConfigOverrides{}).ClientConfig()
}
func NewControl(config *rest.Config) (*Control, error) {
	api, err := dynamic.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	return &Control{API: api}, nil
}
func ClaimName(key string) string {
	s := sha256.Sum256([]byte(key))
	return "ar-" + hex.EncodeToString(s[:16])
}

// Claim uses a stable key and verifies ownership on retry. It never injects Env, preserving warm adoption.
func (c *Control) Claim(ctx context.Context, namespace, pool, key string, expires time.Time) (Lease, error) {
	shutdown := metav1.NewTime(expires)
	return c.ClaimWithSpec(ctx, namespace, key, ext.SandboxClaimSpec{WarmPoolRef: ext.SandboxWarmPoolRef{Name: pool}, Lifecycle: &ext.Lifecycle{ShutdownTime: &shutdown, ShutdownPolicy: ext.ShutdownPolicy("DeleteForeground")}})
}

// ClaimWithSpec accepts the complete upstream claim contract. Env and per-claim volumes force cold starts.
func (c *Control) ClaimWithSpec(ctx context.Context, namespace, key string, spec ext.SandboxClaimSpec) (Lease, error) {
	if spec.Lifecycle == nil || spec.Lifecycle.ShutdownTime == nil {
		return Lease{}, errors.New("claim expiry required")
	}
	pool := spec.WarmPoolRef.Name
	expires := spec.Lifecycle.ShutdownTime.Time

	if key == "" || pool == "" || namespace == "" || !expires.After(time.Now()) {
		return Lease{}, errors.New("claim requires namespace, pool, stable key and future expiry")
	}
	name := ClaimName(key)
	sum := sha256.Sum256([]byte(key))
	owner := hex.EncodeToString(sum[:])
	object := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "extensions.agents.x-k8s.io/v1beta1", "kind": "SandboxClaim", "metadata": map[string]any{"name": name, "namespace": namespace, "labels": map[string]any{"app.kubernetes.io/managed-by": "agent-runtime"}, "annotations": map[string]any{"agent-runtime/run-key": owner, "agents.x-k8s.io/client-first-requested-at": time.Now().UTC().Format(time.RFC3339Nano)}}, "spec": map[string]any{"warmPoolRef": map[string]any{"name": pool}, "lifecycle": map[string]any{"shutdownTime": expires.UTC().Format(time.RFC3339), "shutdownPolicy": "DeleteForeground"}}}}
	raw, err := json.Marshal(spec)
	if err != nil {
		return Lease{}, err
	}
	var wire map[string]any
	if err = json.Unmarshal(raw, &wire); err != nil {
		return Lease{}, err
	}
	object.Object["spec"] = wire
	fingerprintSpec := spec
	fingerprintSpec.Lifecycle = nil
	fingerprintBytes, _ := json.Marshal(fingerprintSpec)
	fingerprint := sha256.Sum256(fingerprintBytes)
	annotations := object.GetAnnotations()
	carrier := propagation.MapCarrier{}
	propagation.TraceContext{}.Inject(ctx, carrier)
	encoded, _ := json.Marshal(carrier)
	annotations["opentelemetry.io/trace-context"] = string(encoded)
	annotations["agent-runtime/spec-sha256"] = hex.EncodeToString(fingerprint[:])
	object.SetAnnotations(annotations)

	api := c.API.Resource(Claims).Namespace(namespace)
	claim, err := api.Create(ctx, object, metav1.CreateOptions{})
	if apierrors.IsAlreadyExists(err) {
		claim, err = api.Get(ctx, name, metav1.GetOptions{})
		if err == nil {
			existing, _, _ := unstructured.NestedString(claim.Object, "spec", "warmPoolRef", "name")
			if claim.GetAnnotations()["agent-runtime/run-key"] != owner || existing != pool || claim.GetAnnotations()["agent-runtime/spec-sha256"] != annotations["agent-runtime/spec-sha256"] {
				return Lease{}, errors.New("claim ownership mismatch")
			}
		}
	}
	if err != nil {
		return Lease{}, err
	}
	if expiry, found, _ := unstructured.NestedString(claim.Object, "spec", "lifecycle", "shutdownTime"); found {
		parsed, parseErr := time.Parse(time.RFC3339, expiry)
		if parseErr != nil {
			return Lease{}, parseErr
		}
		expires = parsed
	}
	trace.SpanFromContext(ctx).SetAttributes(attribute.String("sandbox.claim", name))
	lease := Lease{Namespace: namespace, Claim: name, UID: claim.GetUID(), Expires: expires}
	ready, err := waitReady(ctx, api, claim)
	if err != nil {
		return lease, err
	}
	sandboxName, _, _ := unstructured.NestedString(ready.Object, "status", "sandbox", "name")
	if sandboxName == "" {
		return lease, errors.New("ready claim has no sandbox")
	}
	return c.Resolve(ctx, lease, sandboxName)
}
func (c *Control) Resolve(ctx context.Context, lease Lease, name string) (Lease, error) {
	api := c.API.Resource(Sandboxes).Namespace(lease.Namespace)
	obj, err := api.Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return lease, err
	}
	obj, err = waitReady(ctx, api, obj)
	if err != nil {
		return lease, err
	}
	host, _, _ := unstructured.NestedString(obj.Object, "status", "serviceFQDN")
	if host == "" {
		return lease, errors.New("sandbox template must enable spec.service for stable routing")
	}
	trace.SpanFromContext(ctx).SetAttributes(attribute.String("sandbox.name", name))
	lease.Sandbox = name
	lease.Host = host
	lease.Launch = obj.GetLabels()["agents.x-k8s.io/launch-type"]
	return lease, nil
}
func (c *Control) Attach(ctx context.Context, lease Lease) (Lease, error) {
	obj, err := c.API.Resource(Claims).Namespace(lease.Namespace).Get(ctx, lease.Claim, metav1.GetOptions{})
	if err != nil {
		return lease, err
	}
	if obj.GetUID() != lease.UID {
		return lease, errors.New("claim identity changed")
	}
	name, _, _ := unstructured.NestedString(obj.Object, "status", "sandbox", "name")
	return c.Resolve(ctx, lease, name)
}
func (c *Control) Delete(ctx context.Context, l Lease) error {
	policy := metav1.DeletePropagationForeground
	err := c.API.Resource(Claims).Namespace(l.Namespace).Delete(ctx, l.Claim, metav1.DeleteOptions{PropagationPolicy: &policy, Preconditions: &metav1.Preconditions{UID: &l.UID}})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}
func (c *Control) Expire(ctx context.Context, l Lease, at time.Time) error {
	patch, _ := json.Marshal([]map[string]any{{"op": "test", "path": "/metadata/uid", "value": l.UID}, {"op": "add", "path": "/spec/lifecycle/shutdownTime", "value": at.UTC().Format(time.RFC3339)}})
	_, err := c.API.Resource(Claims).Namespace(l.Namespace).Patch(ctx, l.Claim, types.JSONPatchType, patch, metav1.PatchOptions{})
	return err
}

// Scale pauses/resumes the workload; memory state is not preserved. Storage follows its volume policy.
func (c *Control) Scale(ctx context.Context, l Lease, running bool) error {
	claim, err := c.API.Resource(Claims).Namespace(l.Namespace).Get(ctx, l.Claim, metav1.GetOptions{})
	if err != nil {
		return err
	}
	if claim.GetUID() != l.UID {
		return errors.New("claim identity changed")
	}
	name, _, _ := unstructured.NestedString(claim.Object, "status", "sandbox", "name")
	if name != l.Sandbox {
		return errors.New("sandbox identity changed")
	}
	mode := "Suspended"
	if running {
		mode = "Running"
	}
	object, err := c.API.Resource(Sandboxes).Namespace(l.Namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return err
	}
	patch, _ := json.Marshal([]map[string]any{{"op": "test", "path": "/metadata/uid", "value": object.GetUID()}, {"op": "add", "path": "/spec/operatingMode", "value": mode}})
	_, err = c.API.Resource(Sandboxes).Namespace(l.Namespace).Patch(ctx, name, types.JSONPatchType, patch, metav1.PatchOptions{})
	return err
}

func ready(obj *unstructured.Unstructured) (bool, error) {
	if obj.GetDeletionTimestamp() != nil {
		return false, errors.New("sandbox resource is deleting")
	}
	conditions, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	for _, raw := range conditions {
		condition, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if condition["type"] != "Ready" {
			continue
		}
		if condition["status"] == "True" {
			observed, found, _ := unstructured.NestedInt64(condition, "observedGeneration")
			if (found && observed != obj.GetGeneration()) || (!found && obj.GetGeneration() > 0) {
				continue
			}
			return true, nil
		}
		switch condition["reason"] {
		case "TemplateNotFound", "WarmPoolNotFound", "InvalidMetadata", "EnvVarsInjectionRejected", "VolumeClaimTemplatesError", "ClaimExpired", "SandboxExpired":
			return false, fmt.Errorf("sandbox unavailable: %v", condition["reason"])
		}
	}
	return false, nil
}
func waitReady(ctx context.Context, api dynamic.ResourceInterface, obj *unstructured.Unstructured) (*unstructured.Unstructured, error) {
	uid := obj.GetUID()
	for {
		if obj.GetUID() != uid {
			return nil, errors.New("sandbox resource replaced")
		}
		if ok, err := ready(obj); ok || err != nil {
			return obj, err
		}
		seconds := int64(30)
		w, err := api.Watch(ctx, metav1.ListOptions{FieldSelector: fields.OneTermEqualSelector("metadata.name", obj.GetName()).String(), ResourceVersion: obj.GetResourceVersion(), TimeoutSeconds: &seconds})
		if err != nil {
			if !apierrors.IsResourceExpired(err) {
				return nil, err
			}
		} else {
			result, watchErr := consume(ctx, w, uid)
			w.Stop()
			if result != nil || watchErr != nil {
				return result, watchErr
			}
		}
		obj, err = api.Get(ctx, obj.GetName(), metav1.GetOptions{})
		if err != nil {
			return nil, err
		}
	}
}
func consume(ctx context.Context, w watch.Interface, uid types.UID) (*unstructured.Unstructured, error) {
	for {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case ev, ok := <-w.ResultChan():
			if !ok {
				return nil, nil
			}
			if ev.Type == watch.Error {
				err := apierrors.FromObject(ev.Object)
				if apierrors.IsResourceExpired(err) {
					return nil, nil
				}
				return nil, err
			}
			if ev.Type == watch.Deleted {
				return nil, errors.New("sandbox resource deleted")
			}
			obj, ok := ev.Object.(*unstructured.Unstructured)
			if !ok {
				continue
			}
			if obj.GetUID() != uid {
				return nil, errors.New("sandbox resource replaced")
			}
			if done, err := ready(obj); done || err != nil {
				return obj, err
			}
		}
	}
}

func (c *Control) ActiveClaims(ctx context.Context, namespaces []string) (map[string]bool, error) {
	if len(namespaces) == 0 {
		return nil, errors.New("claim inventory requires namespaces")
	}
	active := map[string]bool{}
	for _, namespace := range namespaces {
		if namespace == "" {
			return nil, errors.New("claim inventory requires explicit namespaces")
		}
		list, err := c.API.Resource(Claims).Namespace(namespace).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/managed-by=agent-runtime"})
		if err != nil {
			return nil, err
		}
		if list.GetContinue() != "" {
			return nil, errors.New("incomplete claim inventory")
		}
		for _, claim := range list.Items {
			active[claim.GetName()] = true
		}
	}
	return active, nil
}

// Find resolves an owned lease without waiting for readiness, including failed or suspended workloads.
func (c *Control) Find(ctx context.Context, namespace, key string) (Lease, error) {
	claim, err := c.API.Resource(Claims).Namespace(namespace).Get(ctx, ClaimName(key), metav1.GetOptions{})
	if err != nil {
		return Lease{}, err
	}
	sum := sha256.Sum256([]byte(key))
	if claim.GetAnnotations()["agent-runtime/run-key"] != hex.EncodeToString(sum[:]) {
		return Lease{}, errors.New("claim ownership mismatch")
	}
	name, _, _ := unstructured.NestedString(claim.Object, "status", "sandbox", "name")
	host, _, _ := unstructured.NestedString(claim.Object, "status", "sandbox", "serviceFQDN")
	return Lease{Namespace: namespace, Claim: claim.GetName(), UID: claim.GetUID(), Sandbox: name, Host: host}, nil
}
