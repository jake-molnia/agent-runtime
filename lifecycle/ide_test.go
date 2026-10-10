package lifecycle

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jake-molnia/agent-runtime/sandbox"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coder/websocket"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	ktesting "k8s.io/client-go/testing"
)

type ideTransport func(*http.Request) (*http.Response, error)

func (f ideTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func ideConfig(callback string) *IDEConfig {
	return &IDEConfig{Domain: "test.ts.net", ProxyGroup: "ingress", BackendService: "t3-runtime", BackendPort: 8084, CallbackURL: callback, Tags: []string{"tag:k8s"}}
}
func TestIDEOriginReadinessIdentityAndRelease(t *testing.T) {
	c, api, _ := fixture(t)
	c.IDE = ideConfig("http://central:3773")
	c.PoolID = "one"
	ctx := context.Background()
	s := run(t, c, request(1, EnsureRunning))
	r := IDERequest{Profile: "default", Identity: *s.Identity}
	result, err := c.EnsureIDE(ctx, r)
	if err != nil || result.Ready {
		t.Fatalf("first allocation: %+v %v", result, err)
	}
	resource := api.Resource(ideIngresses).Namespace(s.Handle.Namespace)
	obj, err := resource.Get(ctx, c.ideName(r.Identity.WorkspaceID), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	hostname := strings.TrimPrefix(result.Origin, "https://")
	obj.Object["status"] = map[string]any{"loadBalancer": map[string]any{"ingress": []any{map[string]any{"hostname": hostname}}}}
	if _, err = resource.UpdateStatus(ctx, obj, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	result, err = c.EnsureIDE(ctx, r)
	if err != nil || result.Ready {
		t.Fatal("hostname without TLS must not be ready", err)
	}
	entries := []any{map[string]any{"hostname": hostname, "ports": []any{map[string]any{"port": int64(443), "protocol": "TCP"}}}}
	_ = unstructured.SetNestedSlice(obj.Object, entries, "status", "loadBalancer", "ingress")
	if _, err = resource.UpdateStatus(ctx, obj, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	result, err = c.EnsureIDE(ctx, r)
	if err != nil || !result.Ready {
		t.Fatal("TLS origin unavailable", err)
	}
	changed := r
	changed.Identity.Incarnation = "wrong"
	if _, err = c.EnsureIDE(ctx, changed); err == nil {
		t.Fatal("stale identity accepted")
	}
	other := *c.IDE
	if err = other.Validate(); err != nil {
		t.Fatal(err)
	}
	run(t, c, stopRequest(2, ReleaseCompute, s))
	if _, err = resource.Get(ctx, obj.GetName(), metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatal("release retained ingress", err)
	}
	if _, err = c.EnsureIDE(ctx, r); err == nil {
		t.Fatal("released origin recreated")
	}
}
func TestIDEProxyPreservesHTTPAndWebSocketWithoutExposingControl(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Host != "t3-ide-"+strings.Repeat("a", 32)+".test.ts.net" || r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "t3_ide_session=proof" {
			t.Error("proxy changed session boundary")
			w.WriteHeader(400)
			return
		}
		if r.Header.Get("Upgrade") == "websocket" {
			conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
			if err != nil {
				return
			}
			defer conn.CloseNow()
			kind, data, err := conn.Read(r.Context())
			if err == nil {
				_ = conn.Write(r.Context(), kind, data)
			}
			return
		}
		w.Header().Set("Set-Cookie", "t3_ide_session=next; HttpOnly; Secure")
		w.WriteHeader(201)
	}))
	defer upstream.Close()
	c, _, _ := fixture(t)
	c.IDE = ideConfig(upstream.URL)
	gateway := httptest.NewServer(Handler(c, nil, "secret"))
	defer gateway.Close()
	req, _ := http.NewRequest("GET", gateway.URL+idePrefix+"session/", nil)
	req.Host = "t3-ide-" + strings.Repeat("a", 32) + ".test.ts.net"
	req.Header.Set("Authorization", "Bearer must-not-forward")
	req.Header.Set("Cookie", "t3_ide_session=proof")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 201 || res.Header.Get("Set-Cookie") == "" {
		t.Fatal(res.Status)
	}
	for _, path := range []string{"/v1/resources", "/v1/ide-origins", "/api/auth/session"} {
		r, _ := http.NewRequest("GET", gateway.URL+path, nil)
		r.Host = req.Host
		out, err := http.DefaultClient.Do(r)
		if err != nil {
			t.Fatal(err)
		}
		out.Body.Close()
		if out.StatusCode != 401 {
			t.Fatalf("exposed %s: %d", path, out.StatusCode)
		}
	}
	req.Host = "central.test.ts.net"
	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 403 {
		t.Fatal("central host proxied")
	}
	headers := http.Header{"Host": []string{"t3-ide-" + strings.Repeat("a", 32) + ".test.ts.net"}, "Cookie": []string{"t3_ide_session=proof"}}
	conn, _, err := websocket.Dial(context.Background(), gateway.URL+idePrefix+"session/", &websocket.DialOptions{HTTPHeader: headers, HTTPClient: &http.Client{Transport: ideTransport(func(r *http.Request) (*http.Response, error) {
		r.Host = "t3-ide-" + strings.Repeat("a", 32) + ".test.ts.net"
		return http.DefaultTransport.RoundTrip(r)
	})}})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	if err = conn.Write(context.Background(), websocket.MessageText, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	_, body, err := conn.Read(context.Background())
	if err != nil || string(body) != "hello" {
		t.Fatal(string(body), err)
	}
}

func TestIDERejectsInvalidConfigAndForeignIngress(t *testing.T) {
	for _, callback := range []string{"file:///tmp/file", "http://user:pass@central/", "http://central/path", "http://central/?target=elsewhere", "http://central/#fragment"} {
		if err := ideConfig(callback).Validate(); err == nil {
			t.Fatalf("accepted %s", callback)
		}
	}
	c, api, _ := fixture(t)
	c.IDE = ideConfig("http://central")
	s := run(t, c, request(1, EnsureRunning))
	r := IDERequest{Profile: "default", Identity: *s.Identity}
	ctx := context.Background()
	if _, err := c.EnsureIDE(ctx, r); err != nil {
		t.Fatal(err)
	}
	resource := api.Resource(ideIngresses).Namespace(s.Handle.Namespace)
	obj, _ := resource.Get(ctx, c.ideName(s.Request.WorkspaceID), metav1.GetOptions{})
	obj.SetAnnotations(map[string]string{"agent-runtime/workspace-key": "foreign"})
	if _, err := resource.Update(ctx, obj, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.EnsureIDE(ctx, r); err == nil {
		t.Fatal("adopted foreign ingress")
	}
	if err := c.deleteIDE(ctx, s); err == nil {
		t.Fatal("deleted foreign ingress")
	}
}

func TestIDERejectsStopIntentAndCleansConcurrentAdmission(t *testing.T) {
	for _, duringCreate := range []bool{false, true} {
		t.Run(fmt.Sprint(duringCreate), func(t *testing.T) {
			c, api, _ := fixture(t)
			c.IDE = ideConfig("http://central")
			s := run(t, c, request(1, EnsureRunning))
			ctx := context.Background()
			stop := stopRequest(2, ReleaseCompute, s)
			admit := func() {
				if _, err := c.Admit(ctx, stop); err != nil {
					t.Fatal(err)
				}
			}
			if duringCreate {
				api.PrependReactor("create", "ingresses", func(a ktesting.Action) (bool, runtime.Object, error) {
					raw, err := api.Tracker().Get(sandbox.WorkspacePVCs, s.Handle.Namespace, sandbox.WorkspacePVCName(s.Request.WorkspaceID))
					if err != nil {
						t.Fatal(err)
					}
					pvc := raw.(*unstructured.Unstructured)
					state := s
					state.Request = stop
					encoded, _ := json.Marshal(state)
					annotations := pvc.GetAnnotations()
					annotations[stateAnnotation] = string(encoded)
					pvc.SetAnnotations(annotations)
					if err = api.Tracker().Update(sandbox.WorkspacePVCs, pvc, s.Handle.Namespace); err != nil {
						t.Fatal(err)
					}
					return false, nil, nil
				})
			} else {
				admit()
			}
			if _, err := c.EnsureIDE(ctx, IDERequest{Profile: "default", Identity: *s.Identity}); err == nil {
				t.Fatal("accepted stop intent")
			}
			_, err := api.Resource(ideIngresses).Namespace(s.Handle.Namespace).Get(ctx, c.ideName(s.Request.WorkspaceID), metav1.GetOptions{})
			if !apierrors.IsNotFound(err) {
				t.Fatal("orphan ingress after stop admission", err)
			}
		})
	}
}

func TestIDEReleasePreservesIngressOwnedByDifferentStorage(t *testing.T) {
	c, api, _ := fixture(t)
	c.IDE = ideConfig("http://central")
	s := run(t, c, request(1, EnsureRunning))
	ctx := context.Background()
	if _, err := c.EnsureIDE(ctx, IDERequest{Profile: "default", Identity: *s.Identity}); err != nil {
		t.Fatal(err)
	}
	resource := api.Resource(ideIngresses).Namespace(s.Handle.Namespace)
	obj, _ := resource.Get(ctx, c.ideName(s.Request.WorkspaceID), metav1.GetOptions{})
	owners := obj.GetOwnerReferences()
	owners[0].UID = "replaced-storage"
	obj.SetOwnerReferences(owners)
	if _, err := resource.Update(ctx, obj, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := c.deleteIDE(ctx, s); err == nil {
		t.Fatal("deleted ingress owned by other PVC")
	}
	if _, err := resource.Get(ctx, obj.GetName(), metav1.GetOptions{}); err != nil {
		t.Fatal("foreign ingress disappeared", err)
	}
}
