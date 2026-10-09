package lifecycle

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jake-molnia/agent-runtime/sandbox"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestGatewayRoutesOnlyAuthenticatedCurrentAllocation(t *testing.T) {
	c, api, _ := fixture(t)
	calls := 0
	streamStarted := make(chan struct{})
	streamCancelled := make(chan struct{})
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Path == "/v1/stream" {
			w.Header().Set("Content-Type", "text/event-stream")
			io.WriteString(w, "data: ready\n\n")
			w.(http.Flusher).Flush()
			close(streamStarted)
			<-r.Context().Done()
			close(streamCancelled)
			return
		}
		if r.URL.RequestURI() != "/v1/events?after=3" || r.Header.Get("Cookie") != "" {
			t.Errorf("bad forwarded request %s", r.URL)
		}
		io.WriteString(w, "event-body")
	}))
	defer upstream.Close()
	u, _ := url.Parse(upstream.URL)
	port, _ := strconv.Atoi(u.Port())
	p := c.Profiles["default"]
	p.Port = port
	c.Profiles["default"] = p
	state := run(t, c, request(1, EnsureRunning))
	obj, _ := api.Tracker().Get(sandbox.Sandboxes, "test", state.Handle.Sandbox)
	raw := obj.(*unstructured.Unstructured)
	unstructured.SetNestedField(raw.Object, u.Hostname(), "status", "serviceFQDN")
	api.Tracker().Update(sandbox.Sandboxes, raw, "test")
	handler := Handler(c, &fakeTasks{}, "control-token")
	path := executionURL("", "default", state.Request.WorkspaceID) + "/v1/events?after=3"
	for _, token := range []string{"", "control-token", c.token(state.Request.WorkspaceID, state.Epoch)} {
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Cookie", "private=1")
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		if token == c.token(state.Request.WorkspaceID, state.Epoch) {
			if out.Code != 200 || out.Body.String() != "event-body" {
				t.Fatalf("proxy %d %s", out.Code, out.Body)
			}
		} else if out.Code != 401 {
			t.Fatal(out.Code)
		}
	}
	if calls != 1 {
		t.Fatal(calls)
	}
	c.PublicURL = "https://pool.example/base"
	req := httptest.NewRequest("GET", "/v1/workspaces/"+url.PathEscape(state.Request.WorkspaceID)+"?profile=default", nil)
	req.Header.Set("Authorization", "Bearer control-token")
	out := httptest.NewRecorder()
	handler.ServeHTTP(out, req)
	var access Access
	json.Unmarshal(out.Body.Bytes(), &access)
	if !strings.HasPrefix(access.State.Endpoint, "https://pool.example/base/v1/execution/default/") {
		t.Fatal(out.Body.String())
	}
	persisted, _, _ := c.load(context.Background(), "default", state.Request.WorkspaceID)
	if persisted.Endpoint != upstream.URL {
		t.Fatalf("persisted public URL: %s", persisted.Endpoint)
	}

	gateway := httptest.NewServer(handler)
	defer gateway.Close()
	ctx, cancel := context.WithCancel(context.Background())
	streamReq, _ := http.NewRequestWithContext(ctx, "GET", gateway.URL+executionURL("", "default", state.Request.WorkspaceID)+"/v1/stream", nil)
	streamReq.Header.Set("Authorization", "Bearer "+c.token(state.Request.WorkspaceID, state.Epoch))
	response, err := http.DefaultClient.Do(streamReq)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	select {
	case <-streamStarted:
	case <-time.After(time.Second):
		t.Fatal("stream buffered")
	}
	cancel()
	response.Body.Close()
	select {
	case <-streamCancelled:
	case <-time.After(time.Second):
		t.Fatal("upstream cancellation lost")
	}
	stop := request(2, Suspend)
	stop.ExpectedAllocationID = state.Identity.AllocationID
	stop.ExpectedGeneration = state.Identity.Generation
	stop.ExpectedIncarnation = state.Identity.Incarnation
	run(t, c, stop)
	req = httptest.NewRequest("GET", path, nil)
	req.Header.Set("Authorization", "Bearer "+c.token(state.Request.WorkspaceID, state.Epoch))
	out = httptest.NewRecorder()
	handler.ServeHTTP(out, req)
	if out.Code != 401 {
		t.Fatalf("suspended worker accessible: %d", out.Code)
	}
	suspended, _, _ := c.load(context.Background(), "default", state.Request.WorkspaceID)
	if suspended.Phase != "suspended" {
		t.Fatal("proxy woke workspace")
	}
}

func TestPoolCredentialsAreIsolated(t *testing.T) {
	c, _, _ := fixture(t)
	c.PoolID = "gompers"
	a := c.token("workspace", 1)
	c.PoolID = "portal"
	if a == c.token("workspace", 1) {
		t.Fatal("cross-pool token")
	}
}
