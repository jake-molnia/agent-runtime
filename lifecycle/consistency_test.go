package lifecycle

import (
	"context"
	"net/http"
	"testing"
)

func controllerReplica(t *testing.T, c *Controller) *Controller {
	t.Helper()
	replica, err := New(c.Control, c.Profiles, c.SecretKey)
	if err != nil {
		t.Fatal(err)
	}
	replica.PoolID, replica.Worker = c.PoolID, c.Worker
	return replica
}

type delayedIdentityWorker struct {
	Worker
	entered chan struct{}
	proceed chan struct{}
}

func (w *delayedIdentityWorker) Identity(ctx context.Context, endpoint, token string) (Identity, error) {
	identity, err := w.Worker.Identity(ctx, endpoint, token)
	close(w.entered)
	select {
	case <-ctx.Done():
		return Identity{}, ctx.Err()
	case <-w.proceed:
		return identity, err
	}
}

func TestAccessCannotPublishObservationAfterReplicaRelease(t *testing.T) {
	for _, refresh := range []bool{false, true} {
		name := "unchanged-identity"
		if refresh {
			name = "restarted-identity"
		}
		t.Run(name, func(t *testing.T) {
			c, _, worker := runnerFixture(t, nil)
			state := run(t, c, request(1, EnsureRunning))
			replica := controllerReplica(t, c)
			ctx := context.Background()
			if refresh {
				worker.identity.Incarnation = "restarted"
			} else {
				if _, err := replica.Admit(ctx, stopRequest(2, ReleaseCompute, state)); err != nil {
					t.Fatal(err)
				}
			}
			blocked := &delayedIdentityWorker{Worker: worker, entered: make(chan struct{}), proceed: make(chan struct{})}
			c.Worker = blocked
			done := make(chan error, 1)
			go func() { _, err := c.Access(ctx, "default", state.Request.WorkspaceID); done <- err }()
			<-blocked.entered
			if refresh {
				current, err := replica.Access(ctx, "default", state.Request.WorkspaceID)
				if err != nil {
					t.Fatal(err)
				}
				state = current.State
				if _, err = replica.Admit(ctx, stopRequest(2, ReleaseCompute, state)); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := replica.Execute(ctx, stopRequest(2, ReleaseCompute, state)); err != nil {
				t.Fatal(err)
			}
			close(blocked.proceed)
			if err := <-done; err == nil {
				t.Error("stale Access returned running authorization after release")
			}
			persisted, _, err := replica.load(ctx, "default", state.Request.WorkspaceID)
			if err != nil {
				t.Fatal(err)
			}
			if persisted.Phase != "released" || persisted.Handle != nil || persisted.Identity != nil || persisted.Endpoint != "" {
				t.Fatalf("stale Access overwrote release: %+v", persisted)
			}
		})
	}
}

func TestGatewayCacheObservesReplicaStop(t *testing.T) {
	for _, action := range []Action{Suspend, ReleaseCompute} {
		for _, complete := range []bool{false, true} {
			phase := "admitted"
			if complete {
				phase = "completed"
			}
			t.Run(string(action)+"/"+phase, func(t *testing.T) {
				c, _, _ := runnerFixture(t, nil)
				state := run(t, c, request(1, EnsureRunning))
				replica := controllerReplica(t, c)
				ctx := context.Background()
				token := "Bearer " + c.token(state.Request.WorkspaceID, state.Epoch)
				if _, status := c.gatewayAccess(ctx, "default", state.Request.WorkspaceID, token); status != http.StatusOK {
					t.Fatal(status)
				}
				stop := stopRequest(2, action, state)
				if _, err := replica.Admit(ctx, stop); err != nil {
					t.Fatal(err)
				}
				if complete {
					if _, err := replica.Execute(ctx, stop); err != nil {
						t.Fatal(err)
					}
				}
				if _, status := c.gatewayAccess(ctx, "default", state.Request.WorkspaceID, token); status == http.StatusOK {
					t.Fatal("cached old allocation authorized after replica stop")
				}
				access, status := c.controlAccess(ctx, "default", state.Request.WorkspaceID)
				if status != http.StatusOK || access.State.Request != stop {
					t.Fatalf("control cache hid replica transition: %d %+v", status, access.State.Request)
				}
			})
		}
	}
}

func TestGatewayCacheAcceptsReplicaReplacementToken(t *testing.T) {
	c, _, _ := runnerFixture(t, nil)
	state := run(t, c, request(1, EnsureRunning))
	replica := controllerReplica(t, c)
	ctx := context.Background()
	oldToken := "Bearer " + c.token(state.Request.WorkspaceID, state.Epoch)
	if _, status := c.gatewayAccess(ctx, "default", state.Request.WorkspaceID, oldToken); status != http.StatusOK {
		t.Fatal(status)
	}
	run(t, replica, stopRequest(2, ReleaseCompute, state))
	replacement := run(t, replica, request(3, EnsureRunning))
	token := "Bearer " + c.token(state.Request.WorkspaceID, replacement.Epoch)
	access, status := c.gatewayAccess(ctx, "default", state.Request.WorkspaceID, token)
	if status != http.StatusOK || access.State.Epoch != replacement.Epoch {
		t.Fatalf("valid replacement token rejected by stale replica cache: %d", status)
	}
	if _, status := c.gatewayAccess(ctx, "default", state.Request.WorkspaceID, oldToken); status != http.StatusUnauthorized {
		t.Fatalf("old allocation token admitted: %d", status)
	}
}

func TestAdmissionRejectsRetainedCompletionIDReuse(t *testing.T) {
	c, _, _ := runnerFixture(t, nil)
	ctx := context.Background()
	first := request(1, EnsureRunning)
	running := run(t, c, first)
	if _, err := c.Admit(ctx, first); err != nil {
		t.Fatalf("exact completed retry rejected: %v", err)
	}
	pending := request(2, EnsureRunning)
	if _, err := c.Admit(ctx, pending); err != nil {
		t.Fatal(err)
	}
	release := stopRequest(3, ReleaseCompute, running)
	collision := release
	collision.OperationID = first.OperationID
	if _, err := c.Admit(ctx, collision); err == nil {
		runner := NewRunner(c)
		id, err := runner.Submit(ctx, collision)
		if err != nil {
			t.Fatal(err)
		}
		status, err := runner.Status(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		t.Fatalf("reused completed ID admitted for release; runner reports %s while compute is running", status.Status)
	}
	current, _, err := c.load(ctx, "default", first.WorkspaceID)
	if err != nil {
		t.Fatal(err)
	}
	if current.Request != pending || current.CompletedOperation != first.OperationID {
		t.Fatal("rejected collision changed retained lifecycle state")
	}
	if _, err := c.Admit(ctx, pending); err != nil {
		t.Fatalf("exact pending retry rejected: %v", err)
	}
	stopped := run(t, c, release)
	if stopped.Phase != "released" || stopped.CompletedOperation != release.OperationID {
		t.Fatal("distinct release operation did not execute")
	}
	if _, err := c.Admit(ctx, release); err != nil {
		t.Fatalf("exact completed release retry rejected: %v", err)
	}
}
