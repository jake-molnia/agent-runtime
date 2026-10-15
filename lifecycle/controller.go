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
		if observation, observeErr := c.Control.ObserveWorkspace(ctx, *s.Handle); observeErr == nil {
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
		if s.Phase == "released" && r.Action == ReleaseCompute {
			s.CompletedOperation = r.OperationID
			return c.physical(ctx, r, s)
		}
		return s, errors.New("no active allocation")
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
	observation, err := c.Control.ObserveWorkspace(ctx, *s.Handle)
	if err != nil {
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
	observation, err = c.Control.WaitWorkspaceMode(ctx, h, sandbox.WorkspaceSuspended)
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
		handle, err := c.Control.FindWorkspace(ctx, spec)
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
		if _, err := c.current(ctx, r); err != nil {
			return s, err
		}
		if s.Epoch > 0 {
			if err := c.deleteSecret(ctx, s); err != nil {
				return s, err
			}
		}
		s.Identity = nil
		s.Endpoint = ""
		s.Phase = desiredState(r.Action)
		s.CompletedOperation = r.OperationID
		return c.physical(ctx, r, s)
	}
	if s.PendingStopIdentity != nil {
		return s, nil
	}
	if s.Identity == nil {
		observation, err := c.Control.WaitWorkspaceMode(ctx, *s.Handle, sandbox.WorkspaceRunning)
		if err != nil {
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
