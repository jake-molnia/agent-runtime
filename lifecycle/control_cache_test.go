package lifecycle

import (
	"context"
	"github.com/jake-molnia/agent-runtime/sandbox"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"
)

func controlRead(c *Controller, workspace, authorization string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "/v1/workspaces/"+url.PathEscape(workspace), nil)
	r.Header.Set("Authorization", authorization)
	w := httptest.NewRecorder()
	Handler(c, &fakeTasks{}, "control").ServeHTTP(w, r)
	return w
}

func TestControlKubernetesReadCost(t *testing.T) {
	c, api, _ := fixture(t)
	state := run(t, c, request(1, EnsureRunning))
	before := len(api.Actions())
	for range 5 {
		w := controlRead(c, state.Request.WorkspaceID, "Bearer control")
		if w.Code != http.StatusOK {
			t.Fatalf("status %d: %s", w.Code, w.Body.String())
		}
	}
	calls := len(api.Actions()) - before
	t.Logf("five authenticated control reads made %d Kubernetes API actions", calls)
	if calls != 15 {
		t.Fatalf("want namespace discovery and shared-state checks per read plus a fresh observation (15 actions), got %d", calls)
	}
}

func expireControlObservation(c *Controller, workspace string) {
	c.gatewayCache.mu.Lock()
	defer c.gatewayCache.mu.Unlock()
	key := gatewayKey{"default", workspace}
	entry := c.gatewayCache.entries[key]
	entry.expires = time.Now().Add(-time.Second)
	c.gatewayCache.entries[key] = entry
}

func TestControlCacheSeparatesAuthorizationAndRetiresAllocation(t *testing.T) {
	c, api, _ := fixture(t)
	state := run(t, c, request(1, EnsureRunning))
	workspace := state.Request.WorkspaceID
	ctx := context.Background()
	if w := controlRead(c, workspace, "Bearer control"); w.Code != 200 {
		t.Fatal(w.Code)
	}
	before := len(api.Actions())
	if w := controlRead(c, workspace, "Bearer "+c.token(workspace, state.Epoch)); w.Code != 401 {
		t.Fatal("worker token admitted as control", w.Code)
	}
	for _, token := range []string{"Bearer wrong", "Bearer control"} {
		if _, status := c.gatewayAccess(ctx, "default", workspace, token); status != 401 {
			t.Fatal("control-populated entry admitted wrong worker token", status)
		}
	}
	if _, status := c.gatewayAccess(ctx, "default", workspace, "Bearer "+c.token(workspace, state.Epoch)); status != 200 {
		t.Fatal(status)
	}
	if len(api.Actions())-before != 3 {
		t.Fatal("warm cache must perform one shared-state check for each gateway authorization")
	}
	if _, err := c.Access(ctx, "default", workspace); err != nil {
		t.Fatal(err)
	}
	if len(api.Actions()) == before {
		t.Fatal("strong Access was cached")
	}
	released := run(t, c, stopRequest(2, ReleaseCompute, state))
	before = len(api.Actions())
	for range 2 {
		if access, status := c.controlAccess(ctx, "default", workspace); status != 200 || access.State.Phase != "released" || access.Token != "" {
			t.Fatal("stale allocation returned", status, access.State.Phase)
		}
	}
	if len(api.Actions())-before != 4 {
		t.Fatal("stopped states were cached")
	}
	if _, status := c.gatewayAccess(ctx, "default", workspace, "Bearer "+c.token(workspace, state.Epoch)); status != 401 {
		t.Fatal("released token admitted", status)
	}
	replacement := run(t, c, request(3, EnsureRunning))
	if replacement.Epoch == released.Epoch {
		t.Fatal("no replacement allocation")
	}
	if access, status := c.controlAccess(ctx, "default", workspace); status != 200 || access.State.Epoch != replacement.Epoch {
		t.Fatal("replacement not observed")
	}
	if _, status := c.gatewayAccess(ctx, "default", workspace, "Bearer "+c.token(workspace, state.Epoch)); status != 401 {
		t.Fatal("old token admitted by replacement cache")
	}
}

func TestControlCacheExpiryChecksPodUIDAndDoesNotCacheErrors(t *testing.T) {
	c, api, worker := fixture(t)
	state := run(t, c, request(1, EnsureRunning))
	ctx := context.Background()
	if _, status := c.controlAccess(ctx, "default", state.Request.WorkspaceID); status != 200 {
		t.Fatal(status)
	}
	observation, err := c.Control.ObserveWorkspace(ctx, *state.Handle)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := api.Tracker().Get(pods, state.Handle.Namespace, observation.PodName)
	if err != nil {
		t.Fatal(err)
	}
	pod := raw.(*unstructured.Unstructured)
	pod.SetUID(types.UID("replacement-pod"))
	if err = api.Tracker().Update(pods, pod, state.Handle.Namespace); err != nil {
		t.Fatal(err)
	}
	expireControlObservation(c, state.Request.WorkspaceID)
	for range 2 {
		before := len(api.Actions())
		if _, status := c.controlAccess(ctx, "default", state.Request.WorkspaceID); status != 503 {
			t.Fatal("worker with stale PodUID admitted", status)
		}
		if len(api.Actions()) == before {
			t.Fatal("failed observation was cached")
		}
	}
	worker.identity.PodUID = "replacement-pod"
	worker.identity.Incarnation = "replacement-process"
	access, status := c.controlAccess(ctx, "default", state.Request.WorkspaceID)
	if status != 200 || access.State.Identity.PodUID != "replacement-pod" || access.State.Identity.Incarnation != "replacement-process" {
		t.Fatal("replacement identity not refreshed", status)
	}
	if err = api.Resource(pods).Namespace(state.Handle.Namespace).Delete(ctx, observation.PodName, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	expireControlObservation(c, state.Request.WorkspaceID)
	if access, status := c.controlAccess(ctx, "default", state.Request.WorkspaceID); status != 503 || access.Token != "" {
		t.Fatal("missing pod admitted", status)
	}
}

func TestControlCacheCoalescesReadsAndInvalidatesInFlightAdmission(t *testing.T) {
	c, api, worker := fixture(t)
	state := run(t, c, request(1, EnsureRunning))
	blocking := &countingGatewayWorker{Worker: worker, entered: make(chan struct{}), proceed: make(chan struct{})}
	c.Worker = blocking
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	before := len(api.Actions())
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, status := c.controlAccess(ctx, "default", state.Request.WorkspaceID); status != 200 {
				t.Errorf("status%d", status)
			}
		}()
	}
	<-blocking.entered
	close(blocking.proceed)
	wg.Wait()
	if blocking.probes.Load() != 1 || len(api.Actions())-before != 25 {
		t.Fatalf("duplicate control probes=%d kube=%d", blocking.probes.Load(), len(api.Actions())-before)
	}
	c.invalidateGateway("default", state.Request.WorkspaceID)
	blocking = &countingGatewayWorker{Worker: worker, entered: make(chan struct{}), proceed: make(chan struct{})}
	c.Worker = blocking
	done := make(chan Access, 1)
	go func() {
		access, status := c.controlAccess(ctx, "default", state.Request.WorkspaceID)
		if status != 200 {
			t.Errorf("status%d", status)
		}
		done <- access
	}()
	<-blocking.entered
	next := stopRequest(2, ReleaseCompute, state)
	if _, err := c.Admit(ctx, next); err != nil {
		t.Fatal(err)
	}
	close(blocking.proceed)
	access := <-done
	if access.State.Request.Revision != 2 || access.State.Request.Action != ReleaseCompute || blocking.probes.Load() != 2 {
		t.Fatal("inflight observation survived admission", access.State.Request, blocking.probes.Load())
	}
}

func TestControlAndGatewayConcurrentReadAuthentication(t *testing.T) {
	c, api, worker := fixture(t)
	state := run(t, c, request(1, EnsureRunning))
	blocking := &countingGatewayWorker{Worker: worker, entered: make(chan struct{}), proceed: make(chan struct{})}
	c.Worker = blocking
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	before := len(api.Actions())
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(control bool) {
			defer wg.Done()
			var status int
			if control {
				_, status = c.controlAccess(ctx, "default", state.Request.WorkspaceID)
			} else {
				_, status = c.gatewayAccess(ctx, "default", state.Request.WorkspaceID, "Bearer "+c.token(state.Request.WorkspaceID, state.Epoch))
			}
			if status != 200 {
				t.Errorf("status%d", status)
			}
		}(i%2 == 0)
	}
	<-blocking.entered
	// A wrong worker token cannot join the authenticated control observation.
	if _, status := c.gatewayAccess(ctx, "default", state.Request.WorkspaceID, "Bearer wrong"); status != 401 {
		t.Fatal("wrong bearer joined control flight", status)
	}
	close(blocking.proceed)
	wg.Wait()
	if blocking.probes.Load() > 2 || len(api.Actions())-before > 52 {
		t.Fatalf("duplicate mixed reads: probes%d kube%d", blocking.probes.Load(), len(api.Actions())-before)
	}
}

func TestGatewayConflictInvalidatesControlReadCache(t *testing.T) {
	c, api, _ := fixture(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusConflict) }))
	defer upstream.Close()
	u, _ := url.Parse(upstream.URL)
	port, _ := strconv.Atoi(u.Port())
	profile := c.Profiles["default"]
	profile.Port = port
	c.Profiles["default"] = profile
	state := run(t, c, request(1, EnsureRunning))
	obj, _ := api.Tracker().Get(sandbox.Sandboxes, "test", state.Handle.Sandbox)
	raw := obj.(*unstructured.Unstructured)
	unstructured.SetNestedField(raw.Object, u.Hostname(), "status", "serviceFQDN")
	if err := api.Tracker().Update(sandbox.Sandboxes, raw, "test"); err != nil {
		t.Fatal(err)
	}
	if w := controlRead(c, state.Request.WorkspaceID, "Bearer control"); w.Code != 200 {
		t.Fatal(w.Code)
	}
	before := len(api.Actions())
	r := httptest.NewRequest("POST", executionURL("", "default", state.Request.WorkspaceID)+"/v1/execute", nil)
	r.Header.Set("Authorization", "Bearer "+c.token(state.Request.WorkspaceID, state.Epoch))
	w := httptest.NewRecorder()
	Handler(c, &fakeTasks{}, "control").ServeHTTP(w, r)
	if w.Code != 409 {
		t.Fatal(w.Code, w.Body.String())
	}
	if len(api.Actions())-before != 1 {
		t.Fatal("gateway must share the worker probe and check shared state")
	}
	if w := controlRead(c, state.Request.WorkspaceID, "Bearer control"); w.Code != 200 {
		t.Fatal(w.Code)
	}
	if len(api.Actions())-before != 8 {
		t.Fatal("worker conflict failed to expire control observation")
	}
}

func TestControlCacheProfileMigrationInvalidatesSourceAndTarget(t *testing.T) {
	c, _, _ := fixture(t)
	ctx := context.Background()
	target := c.Profiles["default"]
	target.PodTemplate = *target.PodTemplate.DeepCopy()
	target.PodTemplate.Spec.Containers[0].Image = "worker:next"
	c.Profiles["next"] = target
	state := run(t, c, request(1, EnsureRunning))
	if _, status := c.controlAccess(ctx, "default", state.Request.WorkspaceID); status != 200 {
		t.Fatal(status)
	}
	released := run(t, c, stopRequest(2, ReleaseCompute, state))
	_, err := c.MigrateProfile(ctx, ProfileMigration{OperationID: "test-migration", ExpectedBindingGeneration: new(uint64), WorkspaceID: state.Request.WorkspaceID, FromProfile: "default", ToProfile: "next", ExpectedProfileHash: state.ProfileHash, ExpectedRevision: released.Request.Revision, ExpectedEpoch: released.Epoch})
	if err != nil {
		t.Fatal(err)
	}
	if _, status := c.controlAccess(ctx, "default", state.Request.WorkspaceID); status != 503 {
		t.Fatal("retired profile admitted", status)
	}
	if access, status := c.controlAccess(ctx, "next", state.Request.WorkspaceID); status != 200 || access.State.Request.Profile != "next" || access.Token != "" {
		t.Fatal("migrated state not observed", status)
	}
}
