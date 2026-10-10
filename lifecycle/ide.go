package lifecycle

import (
	"context"
	"errors"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path"
	"regexp"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var ideIngresses = schema.GroupVersionResource{Group: "networking.k8s.io", Version: "v1", Resource: "ingresses"}

const idePrefix = "/api/sandbox-ide/"

var dnsLabel = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
var ideLabel = regexp.MustCompile(`^t3-ide-[a-f0-9]{32}$`)

type IDEConfig struct {
	Domain, ProxyGroup, BackendService, CallbackURL string
	BackendPort                                     int
	Tags                                            []string
}

func (c IDEConfig) Validate() error {
	if len(c.Tags) == 0 {
		return errors.New("IDE ingress tags required")
	}
	for _, tag := range c.Tags {
		if !strings.HasPrefix(tag, "tag:") || !dnsLabel.MatchString(strings.TrimPrefix(tag, "tag:")) {
			return errors.New("invalid IDE ingress tag")
		}
	}
	if c.Domain == "" || c.Domain != strings.ToLower(c.Domain) || !strings.HasSuffix(c.Domain, ".ts.net") {
		return errors.New("IDE requires a tailnet DNS domain")
	}
	for _, label := range strings.Split(c.Domain, ".") {
		if !dnsLabel.MatchString(label) {
			return errors.New("invalid IDE DNS domain")
		}
	}
	if !dnsLabel.MatchString(c.ProxyGroup) || !dnsLabel.MatchString(c.BackendService) || c.BackendPort < 1 || c.BackendPort > 65535 {
		return errors.New("invalid IDE ingress backend")
	}
	u, err := url.Parse(c.CallbackURL)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return errors.New("invalid IDE callback origin")
	}
	return nil
}

type IDERequest struct {
	Profile  string   `json:"profile"`
	Identity Identity `json:"identity"`
}
type IDEOrigin struct {
	Origin string `json:"origin"`
	Ready  bool   `json:"ready"`
}

func (c *Controller) ideName(workspace string) string {
	return "t3-ide-" + digest([]string{c.PoolID, workspace})[:32]
}
func (c *Controller) ownedIDE(obj *unstructured.Unstructured, workspace string) bool {
	return obj.GetLabels()["app.kubernetes.io/managed-by"] == "agent-runtime" && obj.GetAnnotations()["agent-runtime/workspace-key"] == digest(workspace) && obj.GetAnnotations()["agent-runtime/ide-pool"] == c.PoolID
}

// The retained workspace identity determines its origin. It is never recycled for another workspace.
func (c *Controller) EnsureIDE(ctx context.Context, r IDERequest) (IDEOrigin, error) {
	if c.IDE == nil {
		return IDEOrigin{}, errors.New("IDE ingress is not configured")
	}
	c.ideMu.Lock()
	defer c.ideMu.Unlock()
	s, pvc, err := c.load(ctx, r.Profile, r.Identity.WorkspaceID)
	if err != nil {
		return IDEOrigin{}, err
	}
	if s.Request.Action != EnsureRunning || s.Phase != "running" || s.Identity == nil || *s.Identity != r.Identity {
		return IDEOrigin{}, errors.New("IDE workspace identity changed")
	}
	name := c.ideName(r.Identity.WorkspaceID)
	fqdn := name + "." + c.IDE.Domain
	resource := c.Control.API.Resource(ideIngresses).Namespace(pvc.GetNamespace())
	obj, err := resource.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		obj = &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "networking.k8s.io/v1", "kind": "Ingress",
			"metadata": map[string]any{
				"name": name, "namespace": pvc.GetNamespace(),
				"labels": map[string]any{"app.kubernetes.io/managed-by": "agent-runtime"},
				"annotations": map[string]any{
					"tailscale.com/proxy-group":   c.IDE.ProxyGroup,
					"tailscale.com/tags":          strings.Join(c.IDE.Tags, ","),
					"agent-runtime/workspace-key": digest(r.Identity.WorkspaceID),
					"agent-runtime/ide-pool":      c.PoolID,
				},
				"ownerReferences": []any{map[string]any{
					"apiVersion": "v1", "kind": "PersistentVolumeClaim",
					"name": pvc.GetName(), "uid": string(pvc.GetUID()),
				}},
			},
			"spec": map[string]any{
				"ingressClassName": "tailscale",
				"tls":              []any{map[string]any{"hosts": []any{name}}},
				"rules": []any{map[string]any{"http": map[string]any{"paths": []any{map[string]any{
					"path": idePrefix, "pathType": "Prefix",
					"backend": map[string]any{"service": map[string]any{
						"name": c.IDE.BackendService,
						"port": map[string]any{"number": int64(c.IDE.BackendPort)},
					}},
				}}}}},
			},
		}}
		obj, err = resource.Create(ctx, obj, metav1.CreateOptions{})
		if apierrors.IsAlreadyExists(err) {
			obj, err = resource.Get(ctx, name, metav1.GetOptions{})
		}
	}
	if err != nil {
		return IDEOrigin{}, err
	}
	if !c.ownedIDE(obj, r.Identity.WorkspaceID) || obj.GetDeletionTimestamp() != nil {
		return IDEOrigin{}, errors.New("IDE ingress ownership mismatch")
	}
	if !sameIDEStorage(obj, pvc) {
		return IDEOrigin{}, errors.New("IDE storage identity mismatch")
	}
	current, _, err := c.load(ctx, r.Profile, r.Identity.WorkspaceID)
	if err != nil {
		return IDEOrigin{}, err
	}
	if current.Request.Action != EnsureRunning || current.Phase != "running" || current.Identity == nil || *current.Identity != r.Identity {
		uid, rv := obj.GetUID(), obj.GetResourceVersion()
		deletion := resource.Delete(ctx, obj.GetName(), metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &rv}})
		if deletion != nil && !apierrors.IsNotFound(deletion) {
			return IDEOrigin{}, deletion
		}
		return IDEOrigin{}, errors.New("IDE workspace stopped during provisioning")
	}
	result := IDEOrigin{Origin: "https://" + fqdn}
	entries, _, _ := unstructured.NestedSlice(obj.Object, "status", "loadBalancer", "ingress")
	for _, entry := range entries {
		m, ok := entry.(map[string]any)
		if !ok || m["hostname"] != fqdn {
			continue
		}
		ports, _, _ := unstructured.NestedSlice(m, "ports")
		for _, port := range ports {
			p, ok := port.(map[string]any)
			if ok && p["port"] == int64(443) && p["protocol"] == "TCP" {
				result.Ready = true
			}
		}
	}
	return result, nil
}
func sameIDEStorage(obj, pvc *unstructured.Unstructured) bool {
	owners := obj.GetOwnerReferences()
	return len(owners) == 1 && owners[0].APIVersion == "v1" && owners[0].Kind == "PersistentVolumeClaim" && owners[0].Name == pvc.GetName() && owners[0].UID == pvc.GetUID()
}
func (c *Controller) deleteIDE(ctx context.Context, s State) error {
	if c.IDE == nil {
		return nil
	}
	c.ideMu.Lock()
	defer c.ideMu.Unlock()
	p, ok := c.Profiles[s.Request.Profile]
	if !ok {
		return errors.New("unknown IDE profile")
	}
	resource := c.Control.API.Resource(ideIngresses).Namespace(p.Namespace)
	obj, err := resource.Get(ctx, c.ideName(s.Request.WorkspaceID), metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !c.ownedIDE(obj, s.Request.WorkspaceID) {
		return errors.New("IDE ingress ownership mismatch")
	}
	_, pvc, err := c.load(ctx, s.Request.Profile, s.Request.WorkspaceID)
	if err != nil {
		return err
	}
	if !sameIDEStorage(obj, pvc) {
		return errors.New("IDE storage identity mismatch")
	}
	uid, rv := obj.GetUID(), obj.GetResourceVersion()
	err = resource.Delete(ctx, obj.GetName(), metav1.DeleteOptions{Preconditions: &metav1.Preconditions{UID: &uid, ResourceVersion: &rv}})
	if apierrors.IsNotFound(err) {
		return nil
	}
	return err
}
func (c *Controller) ideProxy() http.Handler {
	if c.IDE == nil {
		return http.NotFoundHandler()
	}
	target, _ := url.Parse(c.IDE.CallbackURL)
	proxy := &httputil.ReverseProxy{Rewrite: func(r *httputil.ProxyRequest) {
		r.SetURL(target)
		r.Out.Host = r.In.Host
		r.Out.Header.Del("Authorization")
		r.Out.Header.Del("Forwarded")
		r.Out.Header.Del("X-Forwarded-Host")
		r.Out.Header.Del("X-Forwarded-Proto")
	}, ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) { http.Error(w, "IDE gateway unavailable", 502) }}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		suffix := "." + c.IDE.Domain
		if path.Clean(r.URL.Path) != strings.TrimSuffix(r.URL.Path, "/") || strings.Contains(r.URL.Path, "\\") || !strings.HasPrefix(r.URL.Path, idePrefix) || !strings.HasSuffix(r.Host, suffix) || !ideLabel.MatchString(strings.TrimSuffix(r.Host, suffix)) {
			http.Error(w, "invalid IDE route", 403)
			return
		}
		proxy.ServeHTTP(w, r)
	})
}
