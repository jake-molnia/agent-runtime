package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	jsonpatch "github.com/evanphx/json-patch/v5"
	"github.com/google/uuid"
	"github.com/jake-molnia/agent-runtime/sandbox"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic/fake"
	ktesting "k8s.io/client-go/testing"
)

// The dynamic fake does not enforce resource versions or delete preconditions.
// These reactors make stale writers and stale lease releases fail like Kubernetes.
func runnerFixture(t *testing.T, saved func(State)) (*Controller, *fake.FakeDynamicClient, *fakeWorker) {
	t.Helper()
	c, api, worker := fixture(t)
	api.PrependReactor("patch", "persistentvolumeclaims", func(a ktesting.Action) (bool, runtime.Object, error) {
		patch := a.(ktesting.PatchAction)
		current, err := api.Tracker().Get(sandbox.WorkspacePVCs, a.GetNamespace(), patch.GetName())
		if err != nil {
			return true, nil, err
		}
		obj := current.(*unstructured.Unstructured)
		data, err := json.Marshal(obj)
		if err != nil {
			return true, nil, err
		}
		operations, err := jsonpatch.DecodePatch(patch.GetPatch())
		if err != nil {
			return true, nil, err
		}
		data, err = operations.Apply(data)
		if err != nil {
			return true, nil, apierrors.NewConflict(sandbox.WorkspacePVCs.GroupResource(), obj.GetName(), err)
		}
		if err = json.Unmarshal(data, obj); err != nil {
			return true, nil, err
		}
		rv, _ := strconv.Atoi(obj.GetResourceVersion())
		obj.SetResourceVersion(strconv.Itoa(rv + 1))
		if err = api.Tracker().Update(sandbox.WorkspacePVCs, obj, obj.GetNamespace()); err != nil {
			return true, nil, err
		}
		if saved != nil {
			var state State
			if json.Unmarshal([]byte(obj.GetAnnotations()[stateAnnotation]), &state) == nil {
				saved(state)
			}
		}
		return true, obj, nil
	})
	api.PrependReactor("create", "leases", func(a ktesting.Action) (bool, runtime.Object, error) {
		obj := a.(ktesting.CreateAction).GetObject().(*unstructured.Unstructured)
		obj.SetUID(types.UID(uuid.NewString()))
		obj.SetResourceVersion("1")
		err := api.Tracker().Create(transitionLeases, obj, a.GetNamespace())
		return true, obj, err
	})
	api.PrependReactor("update", "leases", func(a ktesting.Action) (bool, runtime.Object, error) {
		obj := a.(ktesting.UpdateAction).GetObject().(*unstructured.Unstructured)
		current, err := api.Tracker().Get(transitionLeases, a.GetNamespace(), obj.GetName())
		if err != nil {
			return true, nil, err
		}
		if obj.GetResourceVersion() != current.(*unstructured.Unstructured).GetResourceVersion() {
			return true, nil, apierrors.NewConflict(transitionLeases.GroupResource(), obj.GetName(), errors.New("stale lease"))
		}
		rv, _ := strconv.Atoi(obj.GetResourceVersion())
		obj.SetResourceVersion(strconv.Itoa(rv + 1))
		return true, obj, api.Tracker().Update(transitionLeases, obj, a.GetNamespace())
	})
	api.PrependReactor("delete", "leases", func(a ktesting.Action) (bool, runtime.Object, error) {
		deletion := a.(ktesting.DeleteAction)
		current, err := api.Tracker().Get(transitionLeases, a.GetNamespace(), deletion.GetName())
		if err != nil {
			return true, nil, err
		}
		obj := current.(*unstructured.Unstructured)
		pre := deletion.GetDeleteOptions().Preconditions
		if pre == nil || pre.UID == nil || pre.ResourceVersion == nil || *pre.UID != obj.GetUID() || *pre.ResourceVersion != obj.GetResourceVersion() {
			return true, nil, apierrors.NewConflict(transitionLeases.GroupResource(), obj.GetName(), errors.New("stale lease release"))
		}
		return true, nil, api.Tracker().Delete(transitionLeases, a.GetNamespace(), deletion.GetName())
	})
	return c, api, worker
}

func submitHTTP(t *testing.T, c *Controller, runner *Runner, r Request) string {
	t.Helper()
	body, _ := json.Marshal(r)
	request := httptest.NewRequest("POST", "/v1/operations", strings.NewReader(string(body)))
	request.Header.Set("Authorization", "Bearer control-token")
	out := httptest.NewRecorder()
	Handler(c, runner, "control-token").ServeHTTP(out, request)
	if out.Code != 202 {
		t.Fatalf("submission: %d %s", out.Code, out.Body.String())
	}
	var response struct {
		TaskID string `json:"taskId"`
	}
	if err := json.Unmarshal(out.Body.Bytes(), &response); err != nil || response.TaskID == "" {
		t.Fatalf("invalid task response: %s", out.Body.String())
	}
	return response.TaskID
}

func requireStatus(t *testing.T, runner *Runner, id, want string) {
	t.Helper()
	status, err := runner.Status(context.Background(), id)
	if err != nil || status.Status != want {
		t.Fatalf("status = %+v, %v; want %s", status, err, want)
	}
}

func TestNativeLifecycleHTTPAndDuplicateSubmission(t *testing.T) {
	c, _, worker := runnerFixture(t, nil)
	runner := NewRunner(c)
	r := request(1, EnsureRunning)
	id := submitHTTP(t, c, runner, r)
	if again := submitHTTP(t, c, runner, r); again != id {
		t.Fatal("duplicate submission changed task identity")
	}
	requireStatus(t, runner, id, "QUEUED")
	if err := runner.execute(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, runner, id, "COMPLETED")
	if err := runner.execute(context.Background(), r); err != nil || len(worker.calls) != 1 {
		t.Fatalf("completed operation ran again: %v, %v", err, worker.calls)
	}
	s, err := c.current(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	for i, action := range []Action{Suspend, EnsureRunning, ReleaseCompute} {
		r = request(uint64(i+2), action)
		if action != EnsureRunning {
			r = stopRequest(uint64(i+2), action, s)
		}
		id = submitHTTP(t, c, runner, r)
		if err := runner.execute(context.Background(), r); err != nil {
			t.Fatal(err)
		}
		requireStatus(t, runner, id, "COMPLETED")
		s, err = c.current(context.Background(), r)
		if err != nil {
			t.Fatal(err)
		}
	}
	if s.Phase != "released" || s.Handle != nil || s.Epoch != 1 {
		t.Fatalf("unexpected retained workspace state: %+v", s)
	}
}

func TestRunnerRecoversAdmissionWithoutSubmission(t *testing.T) {
	completed := make(chan struct{})
	var once sync.Once
	c, _, worker := runnerFixture(t, func(s State) {
		if s.CompletedOperation == "operation-1" {
			once.Do(func() { close(completed) })
		}
	})
	r := request(1, EnsureRunning)
	if _, err := c.Admit(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	// Recreate both objects, discarding every in-memory wakeup from admission.
	restarted, err := New(c.Control, c.Profiles, c.SecretKey)
	if err != nil {
		t.Fatal(err)
	}
	restarted.Worker = worker
	runner := NewRunner(restarted)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	select {
	case <-completed:
	case err := <-done:
		t.Fatalf("runner stopped before recovery: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("persisted admission was not recovered")
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	requireStatus(t, runner, taskID(r), "COMPLETED")
}

func TestRunnerRecoversInterruptedAttemptAndExpiredLease(t *testing.T) {
	c, _, _ := runnerFixture(t, nil)
	r := request(1, EnsureRunning)
	runner := NewRunner(c)
	id := submitHTTP(t, c, runner, r)
	s, obj, err := c.load(context.Background(), r.Profile, r.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	s.Operation = &Operation{Status: "RUNNING", Attempts: 1}
	if err = c.save(context.Background(), obj, s); err != nil {
		t.Fatal(err)
	}
	if release, err := c.acquireTransition(context.Background(), r, time.Now().Add(-6*time.Minute)); err != nil || release == nil {
		t.Fatalf("simulate crashed lease: %v", err)
	}
	restarted := NewRunner(c)
	if err = restarted.execute(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, restarted, id, "COMPLETED")
	s, _, err = c.load(context.Background(), r.Profile, r.WorkspaceID)
	if err != nil || s.Operation.Attempts != 2 {
		t.Fatalf("interrupted attempt not retained: %+v %v", s.Operation, err)
	}
}

func TestNativeTaskPreparationRetriesConcurrentStateWrite(t *testing.T) {
	c, api, _ := runnerFixture(t, nil)
	r := request(1, EnsureRunning)
	if _, err := c.Admit(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	conflict := true
	api.PrependReactor("patch", "persistentvolumeclaims", func(a ktesting.Action) (bool, runtime.Object, error) {
		if conflict {
			conflict = false
			return true, nil, apierrors.NewConflict(sandbox.WorkspacePVCs.GroupResource(), a.(ktesting.PatchAction).GetName(), errors.New("concurrent admission"))
		}
		return false, nil, nil
	})
	runner := NewRunner(c)
	id, err := runner.Submit(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, runner, id, "QUEUED")
	if err = runner.execute(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	s, _, err := c.load(context.Background(), r.Profile, r.WorkspaceID)
	if err != nil || s.Operation.Attempts != 1 {
		t.Fatalf("preparation conflict consumed an execution attempt: %+v %v", s.Operation, err)
	}
}

func TestBusyReplicaLeasesDoNotStarveOtherWorkspaces(t *testing.T) {
	completed := make(chan struct{})
	var once sync.Once
	c, api, _ := runnerFixture(t, func(s State) {
		if s.CompletedOperation == "available-operation" {
			once.Do(func() { close(completed) })
		}
	})
	runner := NewRunner(c)
	for i := 0; i < 9; i++ {
		r := request(1, EnsureRunning)
		r.WorkspaceID = "central/root-" + strconv.Itoa(i)
		if _, err := c.Admit(context.Background(), r); err != nil {
			t.Fatal(err)
		}
	}
	pending, err := runner.pending(context.Background())
	if err != nil || len(pending) != 9 {
		t.Fatalf("pending workspaces: %v %v", pending, err)
	}
	for _, r := range pending[:8] {
		release, err := c.acquireTransition(context.Background(), r, time.Now())
		if err != nil || release == nil {
			t.Fatalf("acquire other replica's lease: %v", err)
		}
		defer release()
	}
	available := pending[8]
	available.Revision++
	available.OperationID = "available-operation"
	if _, err = c.Admit(context.Background(), available); err != nil {
		t.Fatal(err)
	}
	// Fix discovery order so the first eight requests all belong to another replica.
	api.PrependReactor("list", "persistentvolumeclaims", func(a ktesting.Action) (bool, runtime.Object, error) {
		list := &unstructured.UnstructuredList{}
		for _, r := range pending {
			obj, err := api.Tracker().Get(sandbox.WorkspacePVCs, "test", sandbox.WorkspacePVCName(r.WorkspaceID))
			if err != nil {
				return true, nil, err
			}
			list.Items = append(list.Items, *obj.(*unstructured.Unstructured))
		}
		return true, list, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	select {
	case <-completed:
	case err := <-done:
		t.Fatalf("runner stopped: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("busy leases starved an available workspace")
	}
	cancel()
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	requireStatus(t, runner, taskID(available), "COMPLETED")
}

type resumeWorker struct {
	Worker
	resume func(context.Context) error
}

func (w resumeWorker) Resume(ctx context.Context, _, _ string, _ WorkerRequest) error {
	return w.resume(ctx)
}

func TestRunnerRetriesAreBoundedAndExplicitRetryWorks(t *testing.T) {
	c, _, worker := runnerFixture(t, nil)
	c.Worker = resumeWorker{Worker: worker, resume: func(context.Context) error { return errors.New("worker unavailable") }}
	runner := NewRunner(c)
	r := request(1, EnsureRunning)
	id := submitHTTP(t, c, runner, r)
	for attempt := 1; attempt <= 4; attempt++ {
		if err := runner.execute(context.Background(), r); err != nil {
			t.Fatal(err)
		}
		s, obj, err := c.load(context.Background(), r.Profile, r.WorkspaceID)
		if err != nil || s.Operation.Attempts != attempt {
			t.Fatalf("attempt = %+v, %v", s.Operation, err)
		}
		if attempt < 4 && !s.Operation.RetryAt.After(time.Now()) {
			t.Fatal("failed attempt had no retry delay")
		}
		s.Operation.RetryAt = time.Time{}
		if err = c.save(context.Background(), obj, s); err != nil {
			t.Fatal(err)
		}
	}
	requireStatus(t, runner, id, "FAILED")
	pending, err := runner.pending(context.Background())
	if err != nil || len(pending) != 0 {
		t.Fatalf("failed operation was automatically retried: %v %v", pending, err)
	}
	c.Worker = worker
	if retryID := submitHTTP(t, c, runner, r); retryID != id {
		t.Fatal("retry changed operation identity")
	}
	if err = runner.execute(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, runner, id, "COMPLETED")
}

func TestReplicaCannotExecuteWhilePreviousTransitionIsRunning(t *testing.T) {
	c, _, worker := runnerFixture(t, nil)
	run(t, c, request(1, EnsureRunning))
	started := make(chan struct{})
	var calls atomic.Int32
	c.Worker = resumeWorker{Worker: worker, resume: func(ctx context.Context) error {
		calls.Add(1)
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}}
	runner := NewRunner(c)
	r := request(2, EnsureRunning)
	id := submitHTTP(t, c, runner, r)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runner.execute(ctx, r) }()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("first transition did not start")
	}
	other := NewRunner(c)
	if err := other.execute(context.Background(), r); err != nil || calls.Load() != 1 {
		t.Fatalf("duplicate transition reached the worker: %v", err)
	}
	next := request(3, EnsureRunning)
	nextID := submitHTTP(t, c, other, next)
	if err := other.execute(context.Background(), next); err != nil || calls.Load() != 1 {
		t.Fatalf("new transition overlapped old transition: %v", err)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	c.Worker = worker
	if err := other.execute(context.Background(), next); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, other, id, "CANCELLED")
	requireStatus(t, other, nextID, "COMPLETED")
}

func TestExpiredLeaseOwnerCannotReleaseSuccessor(t *testing.T) {
	c, api, _ := runnerFixture(t, nil)
	r := request(1, EnsureRunning)
	oldRelease, err := c.acquireTransition(context.Background(), r, time.Now().Add(-6*time.Minute))
	if err != nil || oldRelease == nil {
		t.Fatal(err)
	}
	release, err := c.acquireTransition(context.Background(), r, time.Now())
	if err != nil || release == nil {
		t.Fatalf("expired lease could not be acquired: %v", err)
	}
	oldRelease()
	if acquired, err := c.acquireTransition(context.Background(), r, time.Now()); err != nil || acquired != nil {
		t.Fatalf("old owner removed active lease: %v", err)
	}
	release()
	_, err = api.Resource(transitionLeases).Namespace("test").Get(context.Background(), sandbox.WorkspacePVCName(r.WorkspaceID), metav1.GetOptions{})
	if !apierrors.IsNotFound(err) {
		t.Fatalf("completed transition leaked lease: %v", err)
	}
}

func TestNativeRunnerPoolOwnershipAndLegacyCutover(t *testing.T) {
	c, _, _ := runnerFixture(t, nil)
	r := request(1, EnsureRunning)
	runner := NewRunner(c)
	id := submitHTTP(t, c, runner, r)
	other, err := New(c.Control, c.Profiles, c.SecretKey)
	if err != nil {
		t.Fatal(err)
	}
	other.PoolID = "other-pool"
	if _, err = other.Admit(context.Background(), r); err == nil {
		t.Fatal("another pool adopted an owned workspace")
	}
	if pending, err := NewRunner(other).pending(context.Background()); err != nil || len(pending) != 0 {
		t.Fatalf("another pool discovered owned work: %v %v", pending, err)
	}
	if _, err = NewRunner(other).Status(context.Background(), id); err == nil {
		t.Fatal("another pool read task state")
	}
	// Old workers must be drained before cutover. Reposting the same intent
	// replaces its Hatchet task handle without changing revision or allocation.
	s, obj, err := c.load(context.Background(), r.Profile, r.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	s.TaskID, s.Operation, s.PoolID = uuid.NewString(), nil, nil
	if err = c.save(context.Background(), obj, s); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, runner, s.TaskID, "CANCELLED")
	other.Worker = c.Worker
	cutover := NewRunner(other)
	newID := submitHTTP(t, other, cutover, r)
	if err = cutover.execute(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	requireStatus(t, cutover, newID, "COMPLETED")
}

type workspaceResumeWorker struct {
	Worker
	resume func(context.Context, WorkerRequest) error
}

func (w workspaceResumeWorker) Resume(ctx context.Context, _, _ string, request WorkerRequest) error {
	return w.resume(ctx, request)
}

func TestRunnerKeepsOtherTransitionsAliveThroughInfrastructureErrors(t *testing.T) {
	for _, stage := range []string{"lease", "prepare", "outcome", "discovery"} {
		t.Run(stage, func(t *testing.T) {
			healthy := request(2, EnsureRunning)
			failed := request(1, EnsureRunning)
			failed.WorkspaceID, failed.OperationID = "central/transient", "transient-operation"
			healthyComplete, failedComplete := make(chan struct{}), make(chan struct{})
			var healthyOnce, failedOnce sync.Once
			c, api, worker := runnerFixture(t, func(s State) {
				if s.Request.WorkspaceID == healthy.WorkspaceID && s.CompletedOperation == healthy.OperationID {
					healthyOnce.Do(func() { close(healthyComplete) })
				}
				if s.Request.WorkspaceID == failed.WorkspaceID && s.CompletedOperation == failed.OperationID {
					failedOnce.Do(func() { close(failedComplete) })
				}
			})
			run(t, c, request(1, EnsureRunning))
			started, releaseHealthy := make(chan struct{}), make(chan struct{})
			var startedOnce sync.Once
			c.Worker = workspaceResumeWorker{Worker: worker, resume: func(ctx context.Context, request WorkerRequest) error {
				if request.Identity.WorkspaceID != healthy.WorkspaceID {
					return nil
				}
				startedOnce.Do(func() { close(started) })
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-releaseHealthy:
					return nil
				}
			}}
			runner := NewRunner(c)
			healthyID := submitHTTP(t, c, runner, healthy)
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- runner.Run(ctx) }()
			t.Cleanup(func() {
				cancel()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					t.Error("runner did not stop")
				}
			})
			wait := func(event <-chan struct{}, description string) {
				t.Helper()
				select {
				case <-event:
				case err := <-done:
					done <- err
					t.Fatalf("runner stopped while waiting for %s: %v", description, err)
				case <-time.After(10 * time.Second):
					t.Fatalf("timed out waiting for %s", description)
				}
			}
			wait(started, "healthy transition")
			if _, err := c.Admit(context.Background(), failed); err != nil {
				t.Fatal(err)
			}
			injected, rediscovered := make(chan struct{}), make(chan struct{})
			var injectedOnce, discoveryOnce sync.Once
			var faulted atomic.Bool
			api.PrependReactor("*", "*", func(a ktesting.Action) (bool, runtime.Object, error) {
				if a.GetVerb() == "list" && a.GetResource() == sandbox.WorkspacePVCs && faulted.Load() {
					discoveryOnce.Do(func() { close(rediscovered) })
				}
				if faulted.Load() {
					return false, nil, nil
				}
				fail := false
				switch stage {
				case "lease":
					if a.GetVerb() == "get" && a.GetResource() == transitionLeases {
						fail = a.(ktesting.GetAction).GetName() == sandbox.WorkspacePVCName(failed.WorkspaceID)
					}
				case "prepare":
					if a.GetVerb() == "get" && a.GetResource() == sandbox.WorkspacePVCs {
						fail = a.(ktesting.GetAction).GetName() == sandbox.WorkspacePVCName(failed.WorkspaceID)
					}
				case "outcome":
					if a.GetVerb() == "patch" && a.GetResource() == sandbox.WorkspacePVCs {
						patch := a.(ktesting.PatchAction)
						var operations []struct{ Op, Path, Value string }
						if patch.GetName() == sandbox.WorkspacePVCName(failed.WorkspaceID) && json.Unmarshal(patch.GetPatch(), &operations) == nil {
							for _, op := range operations {
								var state State
								if op.Op == "add" && json.Unmarshal([]byte(op.Value), &state) == nil && state.Operation != nil && state.Operation.Status == "COMPLETED" {
									fail = true
								}
							}
						}
					}
				case "discovery":
					fail = a.GetVerb() == "list" && a.GetResource() == sandbox.WorkspacePVCs
				}
				if !fail {
					return false, nil, nil
				}
				faulted.Store(true)
				injectedOnce.Do(func() { close(injected) })
				return true, nil, apierrors.NewServiceUnavailable("transient Kubernetes API outage")
			})
			runner.notify()
			wait(injected, "injected API failure")
			wait(rediscovered, "continued workspace discovery")
			requireStatus(t, runner, healthyID, "RUNNING")
			httpRequest := httptest.NewRequest("GET", "/v1/operations/"+healthyID, nil)
			httpRequest.Header.Set("Authorization", "Bearer control-token")
			response := httptest.NewRecorder()
			Handler(c, runner, "control-token").ServeHTTP(response, httpRequest)
			if response.Code != http.StatusOK {
				t.Fatalf("healthy operation status unavailable: %d %s", response.Code, response.Body.String())
			}
			if submitHTTP(t, c, runner, healthy) != healthyID {
				t.Fatal("healthy admission changed during unrelated failure")
			}
			close(releaseHealthy)
			wait(healthyComplete, "healthy transition completion")
			wait(failedComplete, "transient failure recovery")
			requireStatus(t, runner, taskID(failed), "COMPLETED")
		})
	}
}

func TestNativeProfileMigrationReplacesTaskHandleWithoutReexecution(t *testing.T) {
	c, _, worker := runnerFixture(t, nil)
	c.Profiles["next"] = c.Profiles["default"]
	runner := NewRunner(c)
	r := request(1, EnsureRunning)
	submitHTTP(t, c, runner, r)
	if err := runner.execute(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	running, err := c.current(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	r = stopRequest(2, ReleaseCompute, running)
	oldID := submitHTTP(t, c, runner, r)
	if err := runner.execute(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	released, err := c.current(context.Background(), r)
	if err != nil {
		t.Fatal(err)
	}
	migration := ProfileMigration{WorkspaceID: r.WorkspaceID, FromProfile: "default", ToProfile: "next", ExpectedProfileHash: released.ProfileHash, ExpectedRevision: r.Revision, ExpectedEpoch: released.Epoch}
	migrated, err := c.MigrateProfile(context.Background(), migration)
	if err != nil {
		t.Fatal(err)
	}
	newID := submitHTTP(t, c, runner, migrated.Request)
	if newID == oldID {
		t.Fatal("migration reused the old profile's task handle")
	}
	requireStatus(t, runner, newID, "COMPLETED")
	calls := len(worker.calls)
	if err := runner.execute(context.Background(), migrated.Request); err != nil || len(worker.calls) != calls {
		t.Fatalf("migrated release executed again: %v", err)
	}
	if _, err := c.MigrateProfile(context.Background(), migration); err != nil {
		t.Fatal(err)
	}
	if duplicate := submitHTTP(t, c, runner, migrated.Request); duplicate != newID {
		t.Fatal("migration retry changed the replacement handle")
	}
}
