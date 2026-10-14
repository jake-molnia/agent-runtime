package lifecycle

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/jake-molnia/agent-runtime/sandbox"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type WorkspaceError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}
type WorkspaceProgress struct {
	Step      string `json:"step"`
	StartedAt string `json:"startedAt"`
}
type WorkspaceExecution struct {
	Identity Identity `json:"identity"`
	Endpoint string   `json:"endpoint"`
	Token    string   `json:"token"`
}
type Workspace struct {
	ID           string              `json:"id"`
	Pool         string              `json:"pool"`
	Profile      string              `json:"profile"`
	DesiredState string              `json:"desiredState"`
	State        string              `json:"state"`
	Version      string              `json:"version"`
	Identity     *Identity           `json:"identity"`
	Progress     *WorkspaceProgress  `json:"progress"`
	Error        *WorkspaceError     `json:"error"`
	Execution    *WorkspaceExecution `json:"execution"`
}

type workspaceFailure struct {
	status        int
	code, message string
	retryable     bool
}

func (e *workspaceFailure) Error() string { return e.message }
func failure(status int, code, message string, retryable bool) error {
	return &workspaceFailure{status, code, message, retryable}
}
func writeAPIError(w http.ResponseWriter, status int, code, message string, retryable bool) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, status, map[string]any{"error": WorkspaceError{code, message, retryable}})
}
func writeWorkspaceFailure(w http.ResponseWriter, err error) {
	var f *workspaceFailure
	if errors.As(err, &f) {
		writeAPIError(w, f.status, f.code, f.message, f.retryable)
		return
	}
	writeAPIError(w, 503, "runtime_unavailable", "Workspace runtime is unavailable", true)
}
func decodeWorkspaceRequest(w http.ResponseWriter, r *http.Request, value any) bool {
	d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	d.DisallowUnknownFields()
	if err := d.Decode(value); err != nil {
		writeAPIError(w, 400, "invalid_request", "Invalid JSON request", false)
		return false
	}
	if err := d.Decode(new(any)); !errors.Is(err, io.EOF) {
		writeAPIError(w, 400, "invalid_request", "Expected one JSON object", false)
		return false
	}
	return true
}
func desiredState(action Action) string {
	switch action {
	case EnsureRunning:
		return "running"
	case Suspend:
		return "suspended"
	default:
		return "released"
	}
}
func workspaceVersion(s State) string {
	incarnation := s.Request.ExpectedIncarnation
	if s.Identity != nil {
		incarnation = s.Identity.Incarnation
	}
	return digest([]any{s.Request, s.Epoch, incarnation})
}

func (c *Controller) workspacePool() string {
	if c.PoolID == "" {
		return "local"
	}
	return c.PoolID
}

// A workspace ID has one retained binding across the namespaces served by this pool.
func (c *Controller) workspaceBinding(ctx context.Context, id string) (State, *unstructured.Unstructured, error) {
	if id == "" || len(id) > 512 {
		return State{}, nil, failure(400, "invalid_workspace", "Invalid workspace ID", false)
	}
	namespaces := map[string]bool{}
	var found *unstructured.Unstructured
	for _, p := range c.Profiles {
		if namespaces[p.Namespace] {
			continue
		}
		namespaces[p.Namespace] = true
		obj, err := c.Control.API.Resource(sandbox.WorkspacePVCs).Namespace(p.Namespace).Get(ctx, sandbox.WorkspacePVCName(id), metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return State{}, nil, err
		}
		if found != nil {
			return State{}, nil, failure(409, "ambiguous_workspace", "Workspace ID is bound in multiple namespaces", false)
		}
		found = obj
	}
	if found == nil {
		return State{}, nil, failure(404, "workspace_not_found", "Workspace is not registered", false)
	}
	if found.GetLabels()["app.kubernetes.io/managed-by"] != "agent-runtime" || found.GetAnnotations()["agent-runtime/workspace-key"] != digest(id) || len(found.GetOwnerReferences()) != 0 || found.GetDeletionTimestamp() != nil {
		return State{}, nil, failure(409, "workspace_ownership", "Workspace storage ownership does not match", false)
	}
	var s State
	if err := json.Unmarshal([]byte(found.GetAnnotations()[stateAnnotation]), &s); err != nil || s.Request.WorkspaceID != id {
		return State{}, found, failure(409, "workspace_binding", "Workspace has no valid lifecycle binding", false)
	}
	if s.PoolID != "" && s.PoolID != c.PoolID {
		return State{}, nil, failure(409, "pool_mismatch", "Workspace is bound to a different pool", false)
	}
	p, ok := c.Profiles[s.Request.Profile]
	if !ok || p.Namespace != found.GetNamespace() || digest(p) != s.ProfileHash {
		return State{}, nil, failure(409, "profile_changed", "Workspace execution profile requires an explicit migration", false)
	}
	return s, found, nil
}
func (c *Controller) registerWorkspace(ctx context.Context, id, pool, profile string) (State, bool, error) {
	if id == "" || len(id) > 512 {
		return State{}, false, failure(400, "invalid_workspace", "Invalid workspace ID", false)
	}
	if pool != c.workspacePool() {
		return State{}, false, failure(409, "pool_mismatch", "Workspace pool does not match this runtime", false)
	}
	if _, ok := c.Profiles[profile]; !ok {
		return State{}, false, failure(400, "unknown_profile", "Unknown execution profile", false)
	}
	s, existing, err := c.workspaceBinding(ctx, id)
	if err == nil {
		if s.Request.Profile != profile {
			return State{}, false, failure(409, "binding_conflict", "Workspace profile is immutable; use an explicit migration", false)
		}
		return s, false, nil
	}
	var f *workspaceFailure
	if !errors.As(err, &f) || (f.code != "workspace_not_found" && !(f.code == "workspace_binding" && existing != nil && existing.GetAnnotations()[stateAnnotation] == "" && existing.GetNamespace() == c.Profiles[profile].Namespace)) {
		return State{}, false, err
	}
	spec, err := c.spec(profile, id, 1)
	if err != nil {
		return State{}, false, err
	}
	if _, err = c.Control.EnsureWorkspaceStorage(ctx, spec); err != nil {
		return State{}, false, err
	}
	s, obj, err := c.load(ctx, profile, id)
	if err != nil {
		return State{}, false, err
	}
	if s.Request.Profile != "" {
		return s, false, nil
	}
	s = State{PoolID: c.PoolID, Request: Request{WorkspaceID: id, Profile: profile, OperationID: uuid.NewString(), Revision: 1, Action: ReleaseCompute}, ProfileHash: digest(c.Profiles[profile]), Phase: "released", StartedAt: time.Now().UTC().Format(time.RFC3339Nano)}
	s.CompletedOperation = s.Request.OperationID
	if err = c.save(ctx, obj, s); err != nil {
		return State{}, false, failure(409, "workspace_changed", "Workspace changed during registration; read and retry", true)
	}
	return s, true, nil
}
func (c *Controller) workspaceResource(ctx context.Context, s State, tasks Tasks, strong bool) Workspace {
	resource := Workspace{ID: s.Request.WorkspaceID, Pool: c.workspacePool(), Profile: s.Request.Profile, DesiredState: desiredState(s.Request.Action), State: s.Phase, Version: workspaceVersion(s), Identity: s.Identity, Error: s.Error}
	if s.Phase == "running" {
		var access Access
		var err error
		if strong {
			access, err = c.Access(ctx, s.Request.Profile, s.Request.WorkspaceID)
		} else {
			var status int
			access, status = c.controlAccess(ctx, s.Request.Profile, s.Request.WorkspaceID)
			if status != http.StatusOK {
				err = errors.New("unavailable")
			}
		}
		if err != nil {
			resource.State = "unavailable"
			resource.Error = &WorkspaceError{"worker_unavailable", "Current worker is unavailable", true}
		} else {
			s = access.State
			resource.DesiredState = desiredState(s.Request.Action)
			resource.State = s.Phase
			resource.Version = workspaceVersion(s)
			resource.Identity = s.Identity
			resource.Error = s.Error
			if s.Identity != nil && access.Token != "" && resource.DesiredState == "running" {
				endpoint := s.Endpoint
				if c.PublicURL != "" {
					endpoint = executionURL(c.PublicURL, s.Request.Profile, s.Request.WorkspaceID)
				}
				resource.Execution = &WorkspaceExecution{*s.Identity, endpoint, access.Token}
			}
		}
	}
	if s.CompletedOperation != s.Request.OperationID {
		if resource.Error == nil && s.TaskID != "" {
			status, err := tasks.Status(ctx, s.TaskID)
			if err != nil {
				resource.Error = &WorkspaceError{"task_unavailable", "Workspace transition status is unavailable", true}
			} else if status.retryable() {
				resource.Error = &WorkspaceError{"lifecycle_failed", "Workspace transition failed; retry the desired state", true}
			}
		}
		step := s.Phase
		if s.TaskID == "" || s.Phase == "running" || s.Phase == "released" || s.Phase == "suspended" {
			step = "queued"
		}
		resource.Progress = &WorkspaceProgress{step, s.StartedAt}
	}
	if resource.Error != nil && (resource.Error.Code == "lifecycle_failed" || resource.Error.Code == "worker_busy") {
		resource.State = "failed"
		resource.Execution = nil
	}
	return resource
}
func writeWorkspace(w http.ResponseWriter, status int, resource Workspace) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("ETag", strconv.Quote(resource.Version))
	if status == http.StatusAccepted || status == http.StatusCreated {
		w.Header().Set("Location", "/v1/workspaces/"+url.PathEscape(resource.ID))
	}
	writeJSON(w, status, resource)
}
func (c *Controller) workspaceRoutes(mux *http.ServeMux, tasks Tasks) {
	mux.HandleFunc("PUT /v1/workspaces/{workspaceId}", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			Pool    string `json:"pool"`
			Profile string `json:"profile"`
		}
		if !decodeWorkspaceRequest(w, r, &input) {
			return
		}
		s, created, err := c.registerWorkspace(r.Context(), r.PathValue("workspaceId"), input.Pool, input.Profile)
		if err != nil {
			writeWorkspaceFailure(w, err)
			return
		}
		status := http.StatusOK
		if created {
			status = http.StatusCreated
		}
		writeWorkspace(w, status, c.workspaceResource(r.Context(), s, tasks, false))
	})
	mux.HandleFunc("GET /v1/workspaces/{workspaceId}", func(w http.ResponseWriter, r *http.Request) {
		s, _, err := c.workspaceBinding(r.Context(), r.PathValue("workspaceId"))
		if err != nil {
			writeWorkspaceFailure(w, err)
			return
		}
		writeWorkspace(w, 200, c.workspaceResource(r.Context(), s, tasks, false))
	})
	mux.HandleFunc("GET /v1/workspaces/{workspaceId}/migration", func(w http.ResponseWriter, r *http.Request) {
		s, _, err := c.workspaceBinding(r.Context(), r.PathValue("workspaceId"))
		if err != nil {
			writeWorkspaceFailure(w, err)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, 200, map[string]any{"profile": s.Request.Profile, "profileHash": s.ProfileHash, "revision": s.Request.Revision, "epoch": s.Epoch, "state": s.Phase})
	})
	mux.HandleFunc("PUT /v1/workspaces/{workspaceId}/state", func(w http.ResponseWriter, r *http.Request) {
		var input struct {
			DesiredState string `json:"desiredState"`
		}
		if !decodeWorkspaceRequest(w, r, &input) {
			return
		}
		action := map[string]Action{"running": EnsureRunning, "suspended": Suspend, "released": ReleaseCompute}[input.DesiredState]
		if action == "" {
			writeAPIError(w, 400, "invalid_state", "Desired state must be running, suspended, or released", false)
			return
		}
		if r.Header.Get("If-Match") == "" {
			writeAPIError(w, 428, "version_required", "If-Match must contain the workspace ETag", false)
			return
		}
		s, _, err := c.workspaceBinding(r.Context(), r.PathValue("workspaceId"))
		if err != nil {
			writeWorkspaceFailure(w, err)
			return
		}
		observed := c.workspaceResource(r.Context(), s, tasks, true)
		s, obj, err := c.load(r.Context(), s.Request.Profile, s.Request.WorkspaceID)
		if err != nil {
			writeWorkspaceFailure(w, err)
			return
		}
		if r.Header.Get("If-Match") != strconv.Quote(workspaceVersion(s)) {
			writeAPIError(w, 412, "version_conflict", "Workspace changed; read its current state before retrying", true)
			return
		}
		completed := s.CompletedOperation == s.Request.OperationID
		sameIntent := s.Request.Action == action
		if sameIntent && completed && observed.State == input.DesiredState {
			writeWorkspace(w, 200, observed)
			return
		}
		if action != EnsureRunning && s.Handle != nil && (s.Identity == nil || observed.State == "unavailable") {
			writeAPIError(w, 409, "worker_unavailable", "Cannot stop compute without an authenticated worker identity", true)
			return
		}
		if !sameIntent || completed {
			next := Request{WorkspaceID: s.Request.WorkspaceID, Profile: s.Request.Profile, OperationID: uuid.NewString(), Revision: s.Request.Revision + 1, Action: action}
			if s.Identity != nil {
				next.ExpectedAllocationID = s.Identity.AllocationID
				next.ExpectedGeneration = s.Identity.Generation
				next.ExpectedIncarnation = s.Identity.Incarnation
			}
			s.Request = next
			s.TaskID = ""
			s.Error = nil
			s.StartedAt = time.Now().UTC().Format(time.RFC3339Nano)
			if s.Handle == nil && action != EnsureRunning {
				s.Phase = input.DesiredState
				s.CompletedOperation = next.OperationID
			}
			if err = c.save(r.Context(), obj, s); err != nil {
				writeAPIError(w, 412, "version_conflict", "Workspace changed; read its current state before retrying", true)
				return
			}
		}
		if s.CompletedOperation == s.Request.OperationID {
			writeWorkspace(w, 200, c.workspaceResource(r.Context(), s, tasks, false))
			return
		}
		taskID := s.TaskID
		if taskID != "" {
			status, statusErr := tasks.Status(r.Context(), taskID)
			if statusErr != nil {
				writeAPIError(w, 503, "task_unavailable", "Workspace transition status is unavailable; retry the desired state", true)
				return
			}
			if status.retryable() {
				taskID = ""
			}
		}
		if taskID == "" {
			taskID, err = tasks.Submit(r.Context(), s.Request)
			if err != nil {
				writeAPIError(w, 503, "submission_failed", "Workspace transition could not be submitted; retry the desired state", true)
				return
			}
			if err = c.SetTask(r.Context(), s.Request, taskID); err != nil {
				writeAPIError(w, 409, "workspace_changed", "Workspace intent changed during submission; read its current state", true)
				return
			}
		}
		s, _, err = c.load(r.Context(), s.Request.Profile, s.Request.WorkspaceID)
		if err != nil {
			writeWorkspaceFailure(w, err)
			return
		}
		writeWorkspace(w, 202, c.workspaceResource(r.Context(), s, tasks, false))
	})
}
