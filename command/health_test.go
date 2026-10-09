package command

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/hatchet-dev/hatchet/pkg/client/rest"
)

func TestWorkerHealthRequiresFreshRegisteredSchedulerIdentity(t *testing.T) {
	now := time.Now()
	active := rest.ACTIVE
	inactive := rest.INACTIVE
	rows := []rest.Worker{{Name: "this-process", Status: &active, LastHeartbeatAt: &now}}
	health := &workerHealth{}
	status := func(path string) int {
		rec := httptest.NewRecorder()
		health.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec.Code
	}
	if status("/healthz") != 200 || status("/readyz") != 503 || status("/startupz") != 503 {
		t.Fatal("unregistered worker reported ready")
	}
	health.observe("this-process", &rest.WorkerList{Rows: &rows}, nil, now)
	if status("/readyz") != 200 || status("/startupz") != 200 {
		t.Fatal("registered active worker was not ready")
	}
	rows[0].Status = &inactive
	health.observe("this-process", &rest.WorkerList{Rows: &rows}, nil, now)
	if status("/readyz") != 503 || status("/startupz") != 200 {
		t.Fatal("disconnected worker remained ready or forgot successful startup")
	}
	rows[0].Status = &active
	rows[0].Name = "different-process"
	health.observe("this-process", &rest.WorkerList{Rows: &rows}, nil, now)
	if status("/readyz") != 503 {
		t.Fatal("another process made this worker ready")
	}
	rows[0].Name = "this-process"
	old := now.Add(-2 * time.Minute)
	rows[0].LastHeartbeatAt = &old
	health.observe("this-process", &rest.WorkerList{Rows: &rows}, nil, now)
	if status("/readyz") != 503 {
		t.Fatal("stale scheduler heartbeat accepted")
	}
	rows[0].LastHeartbeatAt = &now
	health.observe("this-process", &rest.WorkerList{Rows: &rows}, nil, old)
	if status("/readyz") != 503 {
		t.Fatal("stale health sample accepted")
	}
}
