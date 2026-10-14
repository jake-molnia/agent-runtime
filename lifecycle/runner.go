package lifecycle

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jake-molnia/agent-runtime/sandbox"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/util/retry"
)

const taskPrefix = "t3-"

var errOperationSuperseded = errors.New("operation superseded")

// Operation is stored with the workspace request, before execution starts.
// RUNNING survives shutdown so another runner can recover the same intent.
type Operation struct {
	Status   string    `json:"status"`
	Attempts int       `json:"attempts"`
	RetryAt  time.Time `json:"retryAt,omitempty"`
}

type taskAddress struct {
	Profile   string `json:"profile"`
	Workspace string `json:"workspace"`
	Request   string `json:"request"`
}

// Runner implements the existing lifecycle task API using workspace PVC state.
// The in-memory set only limits local work; Kubernetes leases serialize replicas.
type Runner struct {
	controller *Controller
	wake       chan struct{}
}

func NewRunner(controller *Controller) *Runner {
	return &Runner{controller: controller, wake: make(chan struct{}, 1)}
}

func taskID(r Request) string {
	data, _ := json.Marshal(taskAddress{Profile: r.Profile, Workspace: r.WorkspaceID, Request: digest(r)})
	return taskPrefix + base64.RawURLEncoding.EncodeToString(data)
}

func (runner *Runner) notify() {
	select {
	case runner.wake <- struct{}{}:
	default:
	}
}

func (runner *Runner) Submit(ctx context.Context, request Request) (string, error) {
	id, err := runner.prepare(ctx, request, true)
	if err == nil {
		runner.notify()
	}
	return id, err
}

func (runner *Runner) prepare(ctx context.Context, request Request, retry bool) (string, error) {
	id := taskID(request)
	_, err := runner.changeOperation(ctx, request, func(s *State) bool {
		if s.TaskID == id && s.Operation != nil && !(retry && s.Operation.Status == "FAILED") {
			return false
		}
		s.TaskID = id
		s.Operation = &Operation{Status: "QUEUED"}
		if s.CompletedOperation == request.OperationID {
			s.Operation.Status = "COMPLETED"
		}
		return true
	})
	return id, err
}

func (runner *Runner) changeOperation(ctx context.Context, request Request, update func(*State) bool) (State, error) {
	var state State
	err := retry.OnError(retry.DefaultRetry, func(err error) bool {
		// JSON Patch test failures are reported as Invalid by the API server.
		return apierrors.IsConflict(err) || apierrors.IsInvalid(err)
	}, func() error {
		s, obj, err := runner.controller.load(ctx, request.Profile, request.WorkspaceID)
		if err != nil {
			return err
		}
		if digest(s.Request) != digest(request) {
			return errOperationSuperseded
		}
		changed := update(&s)
		state = s
		if !changed {
			return nil
		}
		return runner.controller.save(ctx, obj, s)
	})
	return state, err
}

func (runner *Runner) Status(ctx context.Context, id string) (TaskStatus, error) {
	result := TaskStatus{ID: id}
	if !strings.HasPrefix(id, taskPrefix) {
		// Old Hatchet handles cannot be resumed by this runner. The client retries
		// its persisted intent through Submit after the old worker is drained.
		if _, err := uuid.Parse(id); err != nil {
			return result, errors.New("invalid lifecycle task ID")
		}
		result.Status = "CANCELLED"
		return result, nil
	}
	if len(id) > 4096 {
		return result, errors.New("invalid lifecycle task ID")
	}
	raw, err := base64.RawURLEncoding.DecodeString(strings.TrimPrefix(id, taskPrefix))
	var address taskAddress
	if err != nil || json.Unmarshal(raw, &address) != nil || address.Profile == "" || address.Workspace == "" || address.Request == "" {
		return result, errors.New("invalid lifecycle task ID")
	}
	s, _, err := runner.controller.load(ctx, address.Profile, address.Workspace)
	if err != nil {
		return result, err
	}
	switch {
	case digest(s.Request) != address.Request:
		result.Status = "CANCELLED"
	case s.CompletedOperation == s.Request.OperationID:
		result.Status = "COMPLETED"
	case s.Operation != nil:
		result.Status = s.Operation.Status
	default:
		result.Status = "QUEUED"
	}
	return result, nil
}

// Run rediscovers admitted requests even if the process died before Submit.
// Superseded operations are cancelled; only the current workspace intent runs.
func (runner *Runner) Run(ctx context.Context) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	type finished struct {
		key string
		err error
	}
	done := make(chan finished, 8)
	type running struct {
		request string
		cancel  context.CancelFunc
	}
	active := make(map[string]running)
	var queued []Request
	defer func() {
		for _, work := range active {
			work.cancel()
		}
		for len(active) > 0 {
			delete(active, (<-done).key)
		}
	}()
	runner.notify()
	for {
		select {
		case <-ctx.Done():
			return nil
		case work := <-done:
			active[work.key].cancel()
			delete(active, work.key)
			if work.err != nil && ctx.Err() == nil {
				return work.err
			}
		case <-ticker.C:
			runner.notify()
		case <-runner.wake:
			requests, err := runner.pending(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return nil
				}
				return err
			}
			queued = requests
			for _, request := range queued {
				key := runner.controller.Profiles[request.Profile].Namespace + "/" + sandbox.WorkspacePVCName(request.WorkspaceID)
				if work, ok := active[key]; ok && work.request != digest(request) {
					work.cancel()
				}
			}
		}
		for len(queued) > 0 && len(active) < cap(done) && ctx.Err() == nil {
			request := queued[0]
			queued = queued[1:]
			key := runner.controller.Profiles[request.Profile].Namespace + "/" + sandbox.WorkspacePVCName(request.WorkspaceID)
			if _, ok := active[key]; ok {
				continue
			}
			workCtx, cancel := context.WithCancel(ctx)
			active[key] = running{request: digest(request), cancel: cancel}
			go func() { done <- finished{key, runner.execute(workCtx, request)} }()
		}
	}
}

func (runner *Runner) pending(ctx context.Context) ([]Request, error) {
	c := runner.controller
	namespaces := make(map[string]bool)
	var requests []Request
	for _, profile := range c.Profiles {
		if namespaces[profile.Namespace] {
			continue
		}
		namespaces[profile.Namespace] = true
		objects, err := c.Control.API.Resource(sandbox.WorkspacePVCs).Namespace(profile.Namespace).List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/managed-by=agent-runtime"})
		if err != nil {
			return nil, fmt.Errorf("list lifecycle workspaces: %w", err)
		}
		for _, obj := range objects.Items {
			var s State
			if json.Unmarshal([]byte(obj.GetAnnotations()[stateAnnotation]), &s) != nil {
				continue
			}
			p, ok := c.Profiles[s.Request.Profile]
			if !ok || p.Namespace != obj.GetNamespace() || s.ProfileHash != digest(p) || s.PoolID == nil || *s.PoolID != c.PoolID || s.Request.Validate() != nil || obj.GetDeletionTimestamp() != nil {
				continue
			}
			if s.CompletedOperation == s.Request.OperationID || (s.Operation != nil && (s.Operation.Status == "FAILED" || s.Operation.RetryAt.After(time.Now()))) {
				continue
			}
			requests = append(requests, s.Request)
		}
	}
	return requests, nil
}

func (runner *Runner) execute(ctx context.Context, request Request) error {
	c := runner.controller
	// The lease lasts longer than the execution deadline and is never renewed.
	// An interrupted process cannot retain a lock indefinitely or keep mutating
	// through a live context after another process is allowed to take over.
	now := time.Now()
	bounded, cancel := context.WithDeadline(ctx, now.Add(4*time.Minute))
	defer cancel()
	release, err := c.acquireTransition(bounded, request, now)
	if err != nil || release == nil {
		return err
	}
	defer release()
	if _, err := runner.prepare(bounded, request, false); err != nil {
		if errors.Is(err, errOperationSuperseded) {
			return nil
		}
		return err
	}
	s, err := runner.changeOperation(bounded, request, func(s *State) bool {
		if s.CompletedOperation == request.OperationID || s.Operation.Status == "FAILED" || s.Operation.RetryAt.After(time.Now()) {
			return false
		}
		if s.Operation.Attempts >= 4 {
			s.Operation.Status = "FAILED"
		} else {
			s.Operation.Attempts++
			s.Operation.Status = "RUNNING"
			s.Operation.RetryAt = time.Time{}
		}
		return true
	})
	if errors.Is(err, errOperationSuperseded) {
		return nil
	}
	if err != nil {
		return err
	}
	if s.CompletedOperation == request.OperationID || s.Operation.Status != "RUNNING" || s.Operation.RetryAt.After(time.Now()) {
		return nil
	}
	_, executionErr := c.Execute(bounded, request)
	if ctx.Err() != nil {
		return nil
	}
	// Save a timeout outcome with a fresh context; the execution deadline has
	// expired but the lease still reserves a minute for this bookkeeping.
	finishCtx, finish := context.WithDeadline(ctx, now.Add(4*time.Minute+15*time.Second))
	defer finish()
	_, err = runner.changeOperation(finishCtx, request, func(s *State) bool {
		if executionErr == nil || s.CompletedOperation == request.OperationID {
			s.Operation.Status = "COMPLETED"
		} else if s.Operation.Attempts >= 4 {
			s.Operation.Status = "FAILED"
		} else {
			s.Operation.Status = "QUEUED"
			s.Operation.RetryAt = time.Now().Add(time.Second << (s.Operation.Attempts - 1))
		}
		return true
	})
	if errors.Is(err, errOperationSuperseded) {
		return nil
	}
	return err
}
