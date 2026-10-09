package lifecycle

import (
	"context"
	"github.com/jake-molnia/agent-runtime/sandbox"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestGatewayKubernetesReadCost(t *testing.T) {
	c, api, _ := fixture(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
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
	if _, err := c.Access(context.Background(), "default", state.Request.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	handler := Handler(c, &fakeTasks{}, "control")
	start := len(api.Actions())
	for i := 0; i < 5; i++ {
		r := httptest.NewRequest("GET", executionURL("", "default", state.Request.WorkspaceID)+"/v1/events", nil)
		r.Header.Set("Authorization", "Bearer "+c.token(state.Request.WorkspaceID, state.Epoch))
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		if w.Code != 204 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	t.Logf("five requests made %d Kubernetes API actions", len(api.Actions())-start)
	if len(api.Actions())-start != 6 {
		t.Fatal("gateway repeated Kubernetes reads")
	}
}

func TestGatewayCacheRejectsWrongBearerAndExpires(t *testing.T) {
	c, api, _ := fixture(t)
	state := run(t, c, request(1, EnsureRunning))
	ctx := context.Background()
	token := "Bearer " + c.token(state.Request.WorkspaceID, state.Epoch)
	if _, status := c.gatewayAccess(ctx, "default", state.Request.WorkspaceID, "Bearer wrong"); status != 401 {
		t.Fatal(status)
	}
	if len(c.gatewayCache.entries) != 0 {
		t.Fatal("unauthorized access cached")
	}
	if _, status := c.gatewayAccess(ctx, "default", state.Request.WorkspaceID, token); status != 200 {
		t.Fatal(status)
	}
	actions := len(api.Actions())
	if _, status := c.gatewayAccess(ctx, "default", state.Request.WorkspaceID, "Bearer wrong"); status != 401 {
		t.Fatal(status)
	}
	if len(api.Actions()) != actions {
		t.Fatal("wrong bearer caused unnecessary lookup despite current cached allocation")
	}
	key := gatewayKey{"default", state.Request.WorkspaceID}
	c.gatewayCache.mu.Lock()
	entry := c.gatewayCache.entries[key]
	entry.expires = time.Now().Add(-time.Second)
	c.gatewayCache.entries[key] = entry
	c.gatewayCache.mu.Unlock()
	if _, status := c.gatewayAccess(ctx, "default", state.Request.WorkspaceID, token); status != 200 {
		t.Fatal(status)
	}
	if len(api.Actions())-actions != 6 {
		t.Fatalf("expiry lookup cost %d", len(api.Actions())-actions)
	}
	actions = len(api.Actions())
	if _, err := c.Access(ctx, "default", state.Request.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if len(api.Actions()) == actions {
		t.Fatal("control Access incorrectly used gateway cache")
	}
	// Even a repeated admission synchronously discards the gateway observation.
	if _, err := c.Admit(ctx, state.Request); err != nil {
		t.Fatal(err)
	}
	if len(c.gatewayCache.entries) != 0 {
		t.Fatal("admission retained cache")
	}
	if _, status := c.gatewayAccess(ctx, "default", state.Request.WorkspaceID, token); status != 200 {
		t.Fatal(status)
	}
	stop := request(2, ReleaseCompute)
	stop.ExpectedAllocationID = state.Identity.AllocationID
	stop.ExpectedGeneration = state.Identity.Generation
	stop.ExpectedIncarnation = state.Identity.Incarnation
	run(t, c, stop)
	if _, status := c.gatewayAccess(ctx, "default", state.Request.WorkspaceID, token); status != 401 {
		t.Fatal("released token accepted", status)
	}
	replacement := run(t, c, request(3, EnsureRunning))
	if replacement.Epoch == state.Epoch {
		t.Fatal("test did not replace allocation")
	}
	if _, status := c.gatewayAccess(ctx, "default", state.Request.WorkspaceID, token); status != 401 {
		t.Fatal("stale token accepted", status)
	}
	if _, status := c.gatewayAccess(ctx, "default", state.Request.WorkspaceID, "Bearer "+c.token(state.Request.WorkspaceID, replacement.Epoch)); status != 200 {
		t.Fatal("replacement token rejected", status)
	}
}

type countingGatewayWorker struct {
	Worker
	probes  atomic.Int32
	entered chan struct{}
	proceed chan struct{}
}

func (w *countingGatewayWorker) Identity(ctx context.Context, endpoint, token string) (Identity, error) {
	if w.probes.Add(1) == 1 {
		close(w.entered)
	}
	select {
	case <-ctx.Done():
		return Identity{}, ctx.Err()
	case <-w.proceed:
	}
	return w.Worker.Identity(ctx, endpoint, token)
}
func TestGatewayCacheCoalescesConcurrentReadsAndBoundsEntries(t *testing.T) {
	c, api, worker := fixture(t)
	state := run(t, c, request(1, EnsureRunning))
	blocking := &countingGatewayWorker{Worker: worker, entered: make(chan struct{}), proceed: make(chan struct{})}
	c.Worker = blocking
	before := len(api.Actions())
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, status := c.gatewayAccess(context.Background(), "default", state.Request.WorkspaceID, "Bearer "+c.token(state.Request.WorkspaceID, state.Epoch)); status != 200 {
				t.Errorf("status %d", status)
			}
		}()
	}
	<-blocking.entered
	close(blocking.proceed)
	wg.Wait()
	if blocking.probes.Load() != 1 || len(api.Actions())-before != 6 {
		t.Fatalf("duplicate reads: probes=%d kube=%d", blocking.probes.Load(), len(api.Actions())-before)
	}
	c.gatewayCache.mu.Lock()
	for i := 0; i < gatewayCacheLimit; i++ {
		c.gatewayCache.entries[gatewayKey{"default", strconv.Itoa(i)}] = gatewayEntry{expires: time.Now().Add(time.Second)}
	}
	delete(c.gatewayCache.entries, gatewayKey{"default", state.Request.WorkspaceID})
	c.gatewayCache.mu.Unlock()
	if _, status := c.gatewayAccess(context.Background(), "default", state.Request.WorkspaceID, "Bearer "+c.token(state.Request.WorkspaceID, state.Epoch)); status != 200 {
		t.Fatal(status)
	}
	if len(c.gatewayCache.entries) > gatewayCacheLimit {
		t.Fatal("gateway cache grew beyond limit")
	}
}

func TestGatewayDiscardsInFlightObservationOnAdmission(t *testing.T) {
	c, _, worker := fixture(t)
	state := run(t, c, request(1, EnsureRunning))
	blocking := &countingGatewayWorker{Worker: worker, entered: make(chan struct{}), proceed: make(chan struct{})}
	c.Worker = blocking
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan int, 1)
	go func() {
		_, status := c.gatewayAccess(ctx, "default", state.Request.WorkspaceID, "Bearer "+c.token(state.Request.WorkspaceID, state.Epoch))
		done <- status
	}()
	<-blocking.entered
	if _, err := c.Admit(ctx, state.Request); err != nil {
		t.Fatal(err)
	}
	close(blocking.proceed)
	if status := <-done; status != 200 {
		t.Fatal(status)
	}
	if blocking.probes.Load() != 2 {
		t.Fatal("in-flight observation survived admission", blocking.probes.Load())
	}
	// Access refreshes the incarnation through save; this must not deadlock or cache its stale predecessor.
	c.invalidateGateway("default", state.Request.WorkspaceID)
	worker.identity.Incarnation = "restarted"
	access, status := c.gatewayAccess(ctx, "default", state.Request.WorkspaceID, "Bearer "+c.token(state.Request.WorkspaceID, state.Epoch))
	if status != 200 || access.State.Identity.Incarnation != "restarted" {
		t.Fatal("identity refresh failed", status)
	}
	if blocking.probes.Load() != 4 {
		t.Fatal("identity save did not invalidate in-flight observation", blocking.probes.Load())
	}
}
