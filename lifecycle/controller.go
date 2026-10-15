package lifecycle

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/jake-molnia/agent-runtime/sandbox"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Execute runs under the runner's Kubernetes workspace lease. Every
// external mutation also carries resource identity/version checks for retry safety.
func (c *Controller) Execute(ctx context.Context, r Request) (result State, executionErr error) {
	defer func() {
		if executionErr == nil {
			return
		}
		failureCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		s, obj, err := c.load(failureCtx, r.Profile, r.WorkspaceID)
		if err == nil && digest(s.Request) == digest(r) {
			s.Error = &WorkspaceError{Code: "lifecycle_failed", Message: "Workspace transition failed; retry the desired state", Retryable: true}
			if errors.Is(executionErr, ErrWorkerBusy) {
				s.Error = &WorkspaceError{Code: "worker_busy", Message: "Worker has active work and cannot stop yet", Retryable: true}
			}
			_ = c.save(failureCtx, obj, s)
		}
	}()
	if err := r.Validate(); err != nil {
		return State{}, err
	}
	s, err := c.current(ctx, r)
	if err != nil {
		return s, err
	}
	if s.CompletedOperation == r.OperationID {
		return s, nil
	}
	// Finish a previously submitted irreversible stop before admitting execution.
	if s.Phase == "releasing" {
		if s.Handle == nil {
			return s, errors.New("release missing resource identity")
		}
		observation, observeErr := c.Control.ObserveWorkspaceForStop(ctx, *s.Handle, c.Profiles[r.Profile].Storage)
		if observeErr != nil {
			if !apierrors.IsNotFound(observeErr) {
				return s, observeErr
			}
			absent, err := c.allocationAbsent(ctx, s)
			if err != nil {
				return s, err
			}
			if !absent {
				return s, observeErr
			}
		} else {
			if !observation.Suspended {
				return s, errors.New("releasing allocation is no longer suspended")
			}
			s.Handle = &observation.Handle
		}
		if err = c.Control.DeleteWorkspaceSandbox(ctx, *s.Handle); err != nil {
			return s, err
		}
		if err = c.Control.WaitWorkspaceAbsent(ctx, *s.Handle); err != nil {
			return s, err
		}
		if err = c.deleteSecret(ctx, s); err != nil {
			return s, err
		}
		s.Handle = nil
		s.Identity = nil
		s.Endpoint = ""
		s.Phase = "released"
		s, err = c.physical(ctx, r, s)
		if err != nil {
			return s, err
		}
	}
	if r.Action == EnsureRunning {
		return c.ensureRunning(ctx, r, s)
	}
	return c.stop(ctx, r, s)
}
func (c *Controller) ensureRunning(ctx context.Context, r Request, s State) (State, error) {
	if s.Handle != nil {
		_, err := c.Control.API.Resource(sandbox.Sandboxes).Namespace(s.Handle.Namespace).Get(ctx, s.Handle.Sandbox, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			if _, err = c.current(ctx, r); err != nil {
				return s, err
			}
			if err = c.deleteSecret(ctx, s); err != nil {
				return s, err
			}
			s.Handle = nil
			s.Identity = nil
			s.Endpoint = ""
			s.Phase = "released"
			s, err = c.physical(ctx, r, s)
			if err != nil {
				return s, err
			}
		} else if err != nil {
			return s, err
		}
	}
	if s.Handle == nil {
		if s.Phase != "starting" {
			s.Epoch++
			s.Phase = "starting"
			var err error
			s, err = c.physical(ctx, r, s)
			if err != nil {
				return s, err
			}
		}
		if err := c.ensureSecret(ctx, s); err != nil {
			return s, err
		}
		spec, err := c.spec(r.Profile, r.WorkspaceID, s.Epoch)
		if err != nil {
			return s, err
		}
		if _, err = c.current(ctx, r); err != nil {
			return s, err
		}
		h, err := c.Control.EnsureWorkspace(ctx, spec)
		if err != nil {
			return s, err
		}
		s.Handle = &h
		s, err = c.physical(ctx, r, s)
		if err != nil {
			return s, err
		}
	}
	observation, err := c.Control.ObserveWorkspace(ctx, *s.Handle)
	if err != nil {
		return s, err
	}
	h := observation.Handle
	if observation.Mode == sandbox.WorkspaceSuspended {
		if _, err = c.Control.WaitWorkspaceMode(ctx, h, sandbox.WorkspaceSuspended); err != nil {
			return s, err
		}
		if _, err = c.current(ctx, r); err != nil {
			return s, err
		}
		h, err = c.Control.SetWorkspaceMode(ctx, h, sandbox.WorkspaceRunning)
		if err != nil {
			return s, err
		}
		s.Handle = &h
		s.Phase = "starting"
		s, err = c.physical(ctx, r, s)
		if err != nil {
			return s, err
		}
	}
	observation, err = c.Control.WaitWorkspaceMode(ctx, h, sandbox.WorkspaceRunning)
	if err != nil {
		return s, err
	}
	s.Handle = &observation.Handle
	s.Endpoint = endpoint(observation.Host, c.Profiles[r.Profile].Port)
	identity, err := c.Worker.Identity(ctx, s.Endpoint, c.token(r.WorkspaceID, s.Epoch))
	if err != nil {
		return s, err
	}
	if identity.WorkspaceID != r.WorkspaceID || identity.AllocationID != strconv.FormatUint(s.Epoch, 10) || identity.Generation != s.Epoch || identity.ProtocolVersion != 1 || identity.Incarnation == "" || identity.PodUID != string(observation.PodUID) {
		return s, errors.New("worker identity does not match current allocation and pod")
	}
	if _, err = c.current(ctx, r); err != nil {
		return s, err
	}
	if err = c.Worker.Resume(ctx, s.Endpoint, c.token(r.WorkspaceID, s.Epoch), WorkerRequest{Identity: identity, Revision: r.Revision, OperationID: r.OperationID}); err != nil {
		return s, err
	}
	s.Identity = &identity
	s.Phase = "running"
	s.CompletedOperation = r.OperationID
	return c.physical(ctx, r, s)
}
func (c *Controller) stop(ctx context.Context, r Request, s State) (State, error) {
	if r.ResolvePendingAllocation {
		var err error
		s, err = c.resolvePendingStop(ctx, r, s)
		if err != nil || s.CompletedOperation == r.OperationID {
			return s, err
		}
	}
	if s.Handle == nil {
		return c.completeMissingStop(ctx, r, s, errors.New("allocation still exists without a saved handle"))
	}
	expectedAllocation, expectedGeneration, expectedIncarnation := r.ExpectedAllocationID, r.ExpectedGeneration, r.ExpectedIncarnation
	if r.ResolvePendingAllocation {
		if s.PendingStopIdentity == nil {
			return s, errors.New("pending stop has no authenticated target")
		}
		expectedAllocation, expectedGeneration, expectedIncarnation = s.PendingStopIdentity.AllocationID, s.PendingStopIdentity.Generation, s.PendingStopIdentity.Incarnation
	}
	if s.Handle.AllocationID != expectedAllocation || s.Epoch != expectedGeneration || s.Identity == nil || s.Identity.Incarnation != expectedIncarnation {
		return s, errors.New("destructive request targets stale execution identity")
	}
	observation, err := c.Control.ObserveWorkspaceForStop(ctx, *s.Handle, c.Profiles[r.Profile].Storage)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return c.completeMissingStop(ctx, r, s, err)
		}
		return s, err
	}
	h := observation.Handle
	if observation.Mode == sandbox.WorkspaceRunning {
		if !observation.Ready {
			return s, errors.New("cannot stop allocation with unknown worker readiness")
		}
		identity, err := c.Worker.Identity(ctx, s.Endpoint, c.token(r.WorkspaceID, s.Epoch))
		if err != nil {
			return s, err
		}
		if identity != *s.Identity || identity.PodUID != string(observation.PodUID) {
			return s, errors.New("worker incarnation changed before quiescence")
		}
		proof, err := c.Worker.Quiesce(ctx, s.Endpoint, c.token(r.WorkspaceID, s.Epoch), WorkerRequest{Identity: identity, Revision: r.Revision, OperationID: r.OperationID})
		if err != nil {
			return s, err
		}
		if proof.Identity != identity || proof.Revision != r.Revision || !proof.Quiescent || proof.ActiveOperations != 0 || proof.PendingCallbacks != 0 {
			return s, ErrWorkerBusy
		}
		if _, err = c.current(ctx, r); err != nil {
			return s, err
		}
		s.Phase = "suspending"
		s.Handle = &h
		s, err = c.physical(ctx, r, s)
		if err != nil {
			return s, err
		}
	}
	if observation.Mode == sandbox.WorkspaceRunning {
		if _, err = c.current(ctx, r); err != nil {
			return s, err
		}
		h, err = c.Control.SetWorkspaceMode(ctx, h, sandbox.WorkspaceSuspended)
		if err != nil {
			return s, err
		}
		s.Handle = &h
		s, err = c.physical(ctx, r, s)
		if err != nil {
			return s, err
		}
	}
	observation, err = c.Control.WaitWorkspaceModeForStop(ctx, h, sandbox.WorkspaceSuspended, c.Profiles[r.Profile].Storage)
	if err != nil {
		return s, err
	}
	s.Handle = &observation.Handle
	s.Phase = "suspended"
	s, err = c.physical(ctx, r, s)
	if err != nil {
		return s, err
	}
	if r.Action == ReleaseCompute {
		s.Phase = "releasing"
		s, err = c.physical(ctx, r, s)
		if err != nil {
			return s, err
		}
		if _, err = c.current(ctx, r); err != nil {
			return s, err
		}
		if err = c.Control.DeleteWorkspaceSandbox(ctx, *s.Handle); err != nil {
			return s, err
		}
		if err = c.Control.WaitWorkspaceAbsent(ctx, *s.Handle); err != nil {
			return s, err
		}
		s.Phase = "released"
		if err = c.deleteSecret(ctx, s); err != nil {
			return s, err
		}
		s.Handle = nil
		s.Identity = nil
		s.Endpoint = ""
	}
	s.CompletedOperation = r.OperationID
	return c.physical(ctx, r, s)
}

// The runner holds the workspace lease, so an earlier start has finished or lost
// its execution deadline before this discovers an uncommitted allocation handle.
func (c *Controller) resolvePendingStop(ctx context.Context, r Request, s State) (State, error) {
	if s.Handle == nil && s.Epoch > 0 {
		spec, err := c.spec(r.Profile, r.WorkspaceID, s.Epoch)
		if err != nil {
			return s, err
		}
		handle, err := c.Control.FindWorkspaceForStop(ctx, spec)
		if err == nil {
			s.Handle = &handle
			s, err = c.physical(ctx, r, s)
			if err != nil {
				return s, err
			}
		} else if !apierrors.IsNotFound(err) {
			return s, err
		}
	}
	if s.Handle == nil {
		return c.completeMissingStop(ctx, r, s, errors.New("pending allocation still exists"))
	}
	if s.PendingStopIdentity != nil {
		return s, nil
	}
	if s.Identity == nil {
		observation, err := c.Control.WaitWorkspaceModeForStop(ctx, *s.Handle, sandbox.WorkspaceRunning, c.Profiles[r.Profile].Storage)
		if err != nil {
			if apierrors.IsNotFound(err) {
				return c.completeMissingStop(ctx, r, s, err)
			}
			return s, err
		}
		address := endpoint(observation.Host, c.Profiles[r.Profile].Port)
		identity, err := c.Worker.Identity(ctx, address, c.token(r.WorkspaceID, s.Epoch))
		if err != nil {
			return s, err
		}
		if identity.WorkspaceID != r.WorkspaceID || identity.AllocationID != s.Handle.AllocationID || identity.Generation != s.Epoch || identity.Incarnation == "" || identity.ProtocolVersion != 1 || identity.PodUID != string(observation.PodUID) {
			return s, errors.New("pending allocation worker identity mismatch")
		}
		s.Identity = &identity
		s.Endpoint = address
		s.Handle = &observation.Handle
		s.Phase = "running"
	}
	target := *s.Identity
	s.PendingStopIdentity = &target
	return c.physical(ctx, r, s)
}

// Dependency lookup failures do not prove compute is gone. Only a direct read of
// the saved allocation name may authorize cleanup, under the transition lease.
func (c *Controller) completeMissingStop(ctx context.Context, r Request, s State, observationErr error) (State, error) {
	absent, err := c.allocationAbsent(ctx, s)
	if err != nil {
		return s, err
	}
	if !absent {
		return s, observationErr
	}
	current, err := c.current(ctx, r)
	if err != nil {
		return s, err
	}
	if current.Epoch != s.Epoch || (current.Handle == nil) != (s.Handle == nil) || (current.Handle != nil && current.Handle.UID != s.Handle.UID) {
		return current, errors.New("pending stop allocation changed during observation")
	}
	if s.Epoch > 0 {
		if err = c.deleteSecret(ctx, s); err != nil {
			return s, err
		}
	}
	s.Handle = nil
	s.Identity = nil
	s.Endpoint = ""
	s.Phase = desiredState(r.Action)
	s.CompletedOperation = r.OperationID
	return c.physical(ctx, r, s)
}

// A dependency lookup or an unreachable worker is not evidence of allocation
// absence. Probe the exact saved Sandbox; a replacement UID remains protected.
func (c *Controller) allocationAbsent(ctx context.Context, s State) (bool, error) {
	if s.Epoch == 0 {
		if s.Handle != nil {
			return false, errors.New("allocation handle has no epoch")
		}
		return true, nil
	}
	profile, ok := c.Profiles[s.Request.Profile]
	if !ok {
		return false, errors.New("allocation profile unavailable")
	}
	allocation := strconv.FormatUint(s.Epoch, 10)
	name := sandbox.WorkspaceSandboxName(s.Request.WorkspaceID, allocation)
	if s.Handle != nil && (s.Handle.Namespace != profile.Namespace || s.Handle.Sandbox != name || s.Handle.WorkspaceID != s.Request.WorkspaceID || s.Handle.AllocationID != allocation) {
		return false, errors.New("pending stop allocation identity changed")
	}
	obj, err := c.Control.API.Resource(sandbox.Sandboxes).Namespace(profile.Namespace).Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	if s.Handle != nil && obj.GetUID() != s.Handle.UID {
		return false, errors.New("pending stop allocation was replaced")
	}
	return false, nil
}
