package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/jake-molnia/agent-runtime/sandbox"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const testWorkspace = "central/root"

func workspaceCall(t *testing.T, h http.Handler, method, path, body, etag string) (*httptest.ResponseRecorder, Workspace) {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer control")
	if etag != "" {
		r.Header.Set("If-Match", etag)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	var resource Workspace
	if w.Code >= 200 && w.Code < 300 {
		if err := json.Unmarshal(w.Body.Bytes(), &resource); err != nil {
			t.Fatal(err)
		}
	}
	return w, resource
}
func workspacePath() string { return "/v1/workspaces/" + url.PathEscape(testWorkspace) }
func registerTestWorkspace(t *testing.T, c *Controller, tasks *fakeTasks) (http.Handler, Workspace) {
	t.Helper()
	c.PoolID = "test-pool"
	h := Handler(c, tasks, "control")
	w, res := workspaceCall(t, h, "PUT", workspacePath(), `{"pool":"test-pool","profile":"default"}`, "")
	if w.Code != 201 || res.State != "released" || res.Execution != nil || res.Identity != nil || res.Version == "" {
		t.Fatalf("register: %d %s", w.Code, w.Body)
	}
	return h, res
}
func setTestState(t *testing.T, h http.Handler, desired string, res Workspace) (*httptest.ResponseRecorder, Workspace) {
	return workspaceCall(t, h, "PUT", workspacePath()+"/state", `{"desiredState":"`+desired+`"}`, strconv.Quote(res.Version))
}
func TestWorkspaceRegistrationIsStorageOnlyAndImmutable(t *testing.T) {
	c, api, _ := fixture(t)
	tasks := &fakeTasks{}
	h, original := registerTestWorkspace(t, c, tasks)
	for _, a := range api.Actions() {
		if a.GetVerb() == "create" && (a.GetResource() == sandbox.Sandboxes || a.GetResource() == secrets) {
			t.Fatal("registration allocated compute")
		}
	}
	for range 2 {
		w, res := workspaceCall(t, h, "PUT", workspacePath(), `{"pool":"test-pool","profile":"default"}`, "")
		if w.Code != 200 || res.Version != original.Version || tasks.submissions != 0 {
			t.Fatal("registration not idempotent", w.Code, w.Body)
		}
	}
	c.Profiles["next"] = c.Profiles["default"]
	for _, body := range []string{`{"pool":"other","profile":"default"}`, `{"pool":"test-pool","profile":"next"}`} {
		w, _ := workspaceCall(t, h, "PUT", workspacePath(), body, "")
		if w.Code != 409 {
			t.Fatal(w.Code, w.Body)
		}
	}
	w, _ := workspaceCall(t, h, "PUT", workspacePath(), `{"pool":"test-pool","profile":"default"} {}`, "")
	if w.Code != 400 {
		t.Fatal("trailing JSON accepted")
	}
	w, _ = workspaceCall(t, h, "POST", "/v1/operations", `{}`, "")
	if w.Code != 404 {
		t.Fatal("legacy endpoint still registered")
	}
}
func TestWorkspaceStateConditionalRequestsAndDurableRetry(t *testing.T) {
	c, _, _ := fixture(t)
	tasks := &fakeTasks{}
	h, registered := registerTestWorkspace(t, c, tasks)
	w, _ := workspaceCall(t, h, "PUT", workspacePath()+"/state", `{"desiredState":"running"}`, "")
	if w.Code != 428 {
		t.Fatal(w.Code, w.Body)
	}
	w, pending := setTestState(t, h, "running", registered)
	if w.Code != 202 || w.Header().Get("ETag") != strconv.Quote(pending.Version) || w.Header().Get("Location") != workspacePath() || pending.Progress == nil || pending.Progress.StartedAt == "" {
		t.Fatal(w.Code, w.Body, w.Header())
	}
	w, _ = setTestState(t, h, "running", registered)
	if w.Code != 412 {
		t.Fatal("lost response stale retry accepted", w.Code)
	}
	w, again := setTestState(t, h, "running", pending)
	if w.Code != 202 || again.Version != pending.Version || tasks.submissions != 1 {
		t.Fatal("same intent duplicated", w.Code, w.Body, tasks.submissions)
	}
	tasks.status = "FAILED"
	w, failed := workspaceCall(t, h, "GET", workspacePath(), "", "")
	if w.Code != 200 || failed.State != "failed" || failed.Error.Code != "lifecycle_failed" {
		t.Fatal(w.Code, w.Body)
	}
	w, retried := setTestState(t, h, "running", pending)
	if w.Code != 202 || retried.Version != pending.Version || tasks.submissions != 2 {
		t.Fatal("failed intent not retryable", w.Code, w.Body)
	}
	if _, err := c.Execute(context.Background(), tasks.request); err != nil {
		t.Fatal(err)
	}
	tasks.status = "COMPLETED"
	w, ready := workspaceCall(t, h, "GET", workspacePath(), "", "")
	if ready.State != "running" || ready.Execution == nil || ready.Progress != nil {
		t.Fatal(w.Code, w.Body)
	}
	w, same := setTestState(t, h, "running", ready)
	if w.Code != 200 || same.Version != ready.Version || tasks.submissions != 2 {
		t.Fatal("ready convergence resubmitted", w.Code, w.Body)
	}
}
func TestWorkspaceStopVersionStableAndStorageRetained(t *testing.T) {
	c, api, _ := fixture(t)
	tasks := &fakeTasks{}
	h, res := registerTestWorkspace(t, c, tasks)
	_, res = setTestState(t, h, "running", res)
	if _, err := c.Execute(context.Background(), tasks.request); err != nil {
		t.Fatal(err)
	}
	_, res = workspaceCall(t, h, "GET", workspacePath(), "", "")
	pvc, err := api.Resource(sandbox.WorkspacePVCs).Namespace("test").Get(context.Background(), sandbox.WorkspacePVCName(testWorkspace), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	for _, desired := range []string{"suspended", "released"} {
		w, accepted := setTestState(t, h, desired, res)
		if w.Code != 202 || accepted.Execution != nil {
			t.Fatal(w.Code, w.Body)
		}
		if _, err := c.Execute(context.Background(), tasks.request); err != nil {
			t.Fatal(err)
		}
		w, res = workspaceCall(t, h, "GET", workspacePath(), "", "")
		if res.State != desired || res.Version != accepted.Version || res.Execution != nil {
			t.Fatal("stop version changed", w.Code, w.Body, accepted.Version)
		}
		if desired == "suspended" && res.Identity == nil {
			t.Fatal("suspension lost identity")
		}
	}
	current, err := api.Resource(sandbox.WorkspacePVCs).Namespace("test").Get(context.Background(), sandbox.WorkspacePVCName(testWorkspace), metav1.GetOptions{})
	if err != nil || current.GetUID() != pvc.GetUID() {
		t.Fatal("release removed workspace storage", err)
	}
	_, noop := setTestState(t, h, "released", res)
	if noop.Version != res.Version {
		t.Fatal("release noop changed version")
	}
}
func TestWorkspaceRestartFencesStopAndUnavailableRead(t *testing.T) {
	c, api, worker := fixture(t)
	tasks := &fakeTasks{}
	h, res := registerTestWorkspace(t, c, tasks)
	_, res = setTestState(t, h, "running", res)
	s, err := c.Execute(context.Background(), tasks.request)
	if err != nil {
		t.Fatal(err)
	}
	_, res = workspaceCall(t, h, "GET", workspacePath(), "", "")
	worker.identity.Incarnation = "replacement-process"
	w, _ := setTestState(t, h, "suspended", res)
	if w.Code != 412 {
		t.Fatal("old process ETag admitted stop", w.Code, w.Body)
	}
	_, replacement := workspaceCall(t, h, "GET", workspacePath(), "", "")
	if replacement.Version == res.Version {
		t.Fatal("restart did not change version")
	}
	if err = api.Resource(pods).Namespace(s.Handle.Namespace).Delete(context.Background(), worker.identity.PodUID[:len(worker.identity.PodUID)-4], metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	c.invalidateGateway("default", testWorkspace)
	w, unavailable := workspaceCall(t, h, "GET", workspacePath(), "", "")
	if w.Code != 200 || unavailable.State != "unavailable" || unavailable.Execution != nil || unavailable.Error.Code != "worker_unavailable" || unavailable.Version == "" {
		t.Fatal(w.Code, w.Body)
	}
}
func TestWorkspaceBusyFailureIsSafeAndVisible(t *testing.T) {
	c, _, worker := fixture(t)
	tasks := &fakeTasks{}
	h, res := registerTestWorkspace(t, c, tasks)
	_, res = setTestState(t, h, "running", res)
	if _, err := c.Execute(context.Background(), tasks.request); err != nil {
		t.Fatal(err)
	}
	_, res = workspaceCall(t, h, "GET", workspacePath(), "", "")
	_, res = setTestState(t, h, "released", res)
	worker.busy = true
	if _, err := c.Execute(context.Background(), tasks.request); !errors.Is(err, ErrWorkerBusy) {
		t.Fatal(err)
	}
	w, failed := workspaceCall(t, h, "GET", workspacePath(), "", "")
	if w.Code != 200 || failed.State != "failed" || failed.Error.Code != "worker_busy" || failed.Execution != nil {
		t.Fatal(w.Code, w.Body)
	}
	s, _, err := c.load(context.Background(), "default", testWorkspace)
	if err != nil || s.Handle == nil {
		t.Fatal("busy worker destroyed", err)
	}
}
func TestWorkspaceOppositeIntentCAS(t *testing.T) {
	c, _, _ := fixture(t)
	tasks := &fakeTasks{}
	h, res := registerTestWorkspace(t, c, tasks)
	var wg sync.WaitGroup
	codes := make(chan int, 2)
	for _, desired := range []string{"running", "suspended"} {
		wg.Add(1)
		go func() { defer wg.Done(); w, _ := setTestState(t, h, desired, res); codes <- w.Code }()
	}
	wg.Wait()
	close(codes)
	accepted, rejected := 0, 0
	for code := range codes {
		if code == 200 || code == 202 {
			accepted++
		} else if code == 412 {
			rejected++
		} else {
			t.Fatal(code)
		}
	}
	if accepted != 1 || rejected != 1 {
		t.Fatal("opposite intents admitted", accepted, rejected)
	}
}

func TestWorkspaceRunningRepairsDeletedAllocation(t *testing.T) {
	c, api, _ := fixture(t)
	tasks := &fakeTasks{}
	h, res := registerTestWorkspace(t, c, tasks)
	_, res = setTestState(t, h, "running", res)
	old, err := c.Execute(context.Background(), tasks.request)
	if err != nil {
		t.Fatal(err)
	}
	_, res = workspaceCall(t, h, "GET", workspacePath(), "", "")
	if err = api.Resource(sandbox.Sandboxes).Namespace(old.Handle.Namespace).Delete(context.Background(), old.Handle.Sandbox, metav1.DeleteOptions{}); err != nil {
		t.Fatal(err)
	}
	w, repair := setTestState(t, h, "running", res)
	if w.Code != 202 || repair.Version == res.Version {
		t.Fatal(w.Code, w.Body)
	}
	next, err := c.Execute(context.Background(), tasks.request)
	if err != nil {
		t.Fatal(err)
	}
	if next.Epoch <= old.Epoch || next.Handle == nil || next.Handle.Sandbox == old.Handle.Sandbox {
		t.Fatal("deleted allocation not replaced")
	}
	w, ready := workspaceCall(t, h, "GET", workspacePath(), "", "")
	if ready.Execution == nil || ready.State != "running" {
		t.Fatal(w.Code, w.Body)
	}
}
func TestWorkspaceSubmissionFailureCanResumeAfterRead(t *testing.T) {
	c, _, _ := fixture(t)
	tasks := &fakeTasks{submitError: errors.New("secret internal detail")}
	h, res := registerTestWorkspace(t, c, tasks)
	w, _ := setTestState(t, h, "running", res)
	if w.Code != 503 || strings.Contains(w.Body.String(), "secret") {
		t.Fatal(w.Code, w.Body)
	}
	_, res = workspaceCall(t, h, "GET", workspacePath(), "", "")
	tasks.submitError = nil
	w, _ = setTestState(t, h, "running", res)
	if w.Code != 202 || tasks.submissions != 2 {
		t.Fatal(w.Code, w.Body)
	}
}
func TestWorkspaceBindingDiscoveryRejectsAmbiguity(t *testing.T) {
	c, api, _ := fixture(t)
	tasks := &fakeTasks{}
	h, _ := registerTestWorkspace(t, c, tasks)
	profile := c.Profiles["default"]
	c.Profiles["alias"] = profile
	before := len(api.Actions())
	w, _ := workspaceCall(t, h, "GET", workspacePath(), "", "")
	if w.Code != 200 || len(api.Actions())-before != 1 {
		t.Fatal("namespace not deduplicated", w.Code, len(api.Actions())-before)
	}
	original, err := api.Resource(sandbox.WorkspacePVCs).Namespace("test").Get(context.Background(), sandbox.WorkspacePVCName(testWorkspace), metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	profile.Namespace = "other"
	c.Profiles["other"] = profile
	original.SetNamespace("other")
	if _, err = api.Resource(sandbox.WorkspacePVCs).Namespace("other").Create(context.Background(), original, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	w, _ = workspaceCall(t, h, "GET", workspacePath(), "", "")
	if w.Code != 409 || !strings.Contains(w.Body.String(), "ambiguous_workspace") {
		t.Fatal(w.Code, w.Body)
	}
}
func TestWorkspaceRegistrationRecoversInterruptedStorageCreation(t *testing.T) {
	c, _, _ := fixture(t)
	spec, err := c.spec("default", testWorkspace, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Control.EnsureWorkspaceStorage(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	registerTestWorkspace(t, c, &fakeTasks{})
}
func TestWorkspaceMigrationViewDoesNotExposeExecutionCredentials(t *testing.T) {
	c, _, _ := fixture(t)
	h, _ := registerTestWorkspace(t, c, &fakeTasks{})
	w, _ := workspaceCall(t, h, "GET", workspacePath()+"/migration", "", "")
	var fields map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &fields); err != nil {
		t.Fatal(err)
	}
	if w.Code != 200 || len(fields) != 6 || fields["bindingGeneration"] != float64(1) || fields["profile"] != "default" || fields["state"] != "released" {
		t.Fatal(w.Code, w.Body)
	}
}

func TestUnscopedControllerUsesLocalPublicPoolWithoutChangingWorkerCredentials(t *testing.T) {
	c, _, _ := fixture(t)
	token := c.token(testWorkspace, 1)
	h := Handler(c, &fakeTasks{}, "control")
	w, resource := workspaceCall(t, h, "PUT", workspacePath(), `{"pool":"local","profile":"default"}`, "")
	if w.Code != 201 || resource.Pool != "local" {
		t.Fatal(w.Code, w.Body)
	}
	if c.PoolID != "" || c.token(testWorkspace, 1) != token {
		t.Fatal("public pool alias changed internal credential scope")
	}
	w, _ = workspaceCall(t, h, "PUT", workspacePath(), `{"pool":"","profile":"default"}`, "")
	if w.Code != 409 {
		t.Fatal("empty public pool accepted", w.Code, w.Body)
	}
}

func TestWorkspaceAPIRetriesWithPersistentRunner(t *testing.T) {
	c, _, _ := fixture(t)
	c.PoolID = "test-pool"
	runner := NewRunner(c)
	h := Handler(c, runner, "control")
	w, registered := workspaceCall(t, h, "PUT", workspacePath(), `{"pool":"test-pool","profile":"default"}`, "")
	if w.Code != 201 {
		t.Fatal(w.Code, w.Body)
	}
	w, pending := setTestState(t, h, "running", registered)
	if w.Code != 202 {
		t.Fatal(w.Code, w.Body)
	}
	state, _, err := c.load(t.Context(), "default", testWorkspace)
	if err != nil || state.Operation == nil || state.Operation.Status != "QUEUED" {
		t.Fatal(state, err)
	}
	_, err = runner.changeOperation(t.Context(), state.Request, func(s *State) bool {
		s.Operation.Status = "FAILED"
		s.Error = &WorkspaceError{Code: "lifecycle_failed", Message: "Workspace transition failed", Retryable: true}
		return true
	})
	if err != nil {
		t.Fatal(err)
	}
	w, retried := setTestState(t, h, "running", pending)
	if w.Code != 202 || retried.Error != nil || retried.Version != pending.Version {
		t.Fatal(w.Code, w.Body)
	}
	state, _, err = c.load(t.Context(), "default", testWorkspace)
	if err != nil || state.Operation.Status != "QUEUED" {
		t.Fatal(state, err)
	}
	requests, err := runner.pending(t.Context())
	if err != nil || len(requests) != 1 {
		t.Fatal(requests, err)
	}
	if _, err = c.Execute(t.Context(), requests[0]); err != nil {
		t.Fatal(err)
	}
	_, ready := workspaceCall(t, h, "GET", workspacePath(), "", "")
	if ready.State != "running" || ready.Execution == nil {
		t.Fatal(ready)
	}
}

func TestWorkspaceAPIAdoptsLegacyBindingForNativeRunner(t *testing.T) {
	for _, register := range []bool{false, true} {
		t.Run(strconv.FormatBool(register), func(t *testing.T) {
			c, _, _ := fixture(t)
			c.PoolID = "test-pool"
			state := run(t, c, request(1, EnsureRunning))
			s, obj, err := c.load(t.Context(), "default", testWorkspace)
			if err != nil {
				t.Fatal(err)
			}
			s.PoolID = nil
			if err = c.save(t.Context(), obj, s); err != nil {
				t.Fatal(err)
			}
			runner := NewRunner(c)
			h := Handler(c, runner, "control")
			if register {
				w, _ := workspaceCall(t, h, "PUT", workspacePath(), `{"pool":"test-pool","profile":"default"}`, "")
				if w.Code != 200 {
					t.Fatal(w.Code, w.Body)
				}
				stored, _, err := c.load(t.Context(), "default", testWorkspace)
				if err != nil || stored.PoolID == nil || *stored.PoolID != c.PoolID {
					t.Fatal(stored, err)
				}
			}
			_, resource := workspaceCall(t, h, "GET", workspacePath(), "", "")
			w, _ := setTestState(t, h, "suspended", resource)
			if w.Code != 202 {
				t.Fatal(w.Code, w.Body)
			}
			pending, err := runner.pending(t.Context())
			if err != nil || len(pending) != 1 || pending[0].WorkspaceID != state.Request.WorkspaceID {
				t.Fatal(pending, err)
			}
		})
	}
}
