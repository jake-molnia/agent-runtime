package lifecycle

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type fakeTasks struct {
	submissions int
	status      string
	request     Request
	submitError error
	statusError error
}

func (f *fakeTasks) Submit(_ context.Context, r Request) (string, error) {
	f.submissions++
	f.request = r
	if f.submitError != nil {
		return "", f.submitError
	}
	return "task-one", nil
}
func (f *fakeTasks) Status(_ context.Context, id string) (TaskStatus, error) {
	status := f.status
	if status == "" {
		status = "RUNNING"
	}
	return TaskStatus{ID: id, Status: status}, f.statusError
}
func TestWorkerHTTPContractAndSanitizedErrors(t *testing.T) {
	identity := Identity{WorkspaceID: "w", AllocationID: "1", Generation: 1, Incarnation: "process", ProtocolVersion: 1, PodUID: "pod"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret-token" {
			t.Error("authorization missing")
		}
		switch r.URL.Path {
		case "/v1/identity":
			json.NewEncoder(w).Encode(identity)
		case "/v1/quiesce":
			var input WorkerRequest
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil || input.Identity != identity {
				t.Error("invalid identity envelope")
			}
			json.NewEncoder(w).Encode(Quiescence{Identity: identity, Revision: input.Revision, Quiescent: true})
		default:
			http.Error(w, "secret-token sensitive worker traceback", 500)
		}
	}))
	defer server.Close()
	worker := HTTPWorker{Client: server.Client()}
	got, err := worker.Identity(context.Background(), server.URL, "secret-token")
	if err != nil || got != identity {
		t.Fatal("identity did not roundtrip")
	}
	proof, err := worker.Quiesce(context.Background(), server.URL, "secret-token", WorkerRequest{Identity: identity, Revision: 2, OperationID: "op"})
	if err != nil || !proof.Quiescent || proof.Revision != 2 {
		t.Fatal("quiescence failed")
	}
	if err = worker.Resume(context.Background(), server.URL, "secret-token", WorkerRequest{}); err == nil || strings.Contains(err.Error(), "secret-token") {
		t.Fatal("worker failure leaked secret or was accepted")
	}
}

func TestProfileMigrationHTTPRequiresManagementAuthAndStrictJSON(t *testing.T) {
	c, _, _ := fixture(t)
	s := run(t, c, request(1, EnsureRunning))
	s = run(t, c, stopRequest(2, ReleaseCompute, s))
	c.Profiles["next"] = c.Profiles["default"]
	migration := ProfileMigration{OperationID: "test-migration", ExpectedBindingGeneration: new(uint64), WorkspaceID: s.Request.WorkspaceID, FromProfile: "default", ToProfile: "next", ExpectedProfileHash: s.ProfileHash, ExpectedRevision: s.Request.Revision, ExpectedEpoch: s.Epoch}
	body, _ := json.Marshal(migration)
	h := Handler(c, &fakeTasks{}, "internal-key")
	for _, test := range []struct {
		auth, body string
		status     int
	}{
		{"", string(body), 401},
		{"Bearer wrong", string(body), 401},
		{"Bearer internal-key", string(body) + " {}", 400},
		{"Bearer internal-key", `{"unknown":true}`, 400},
		{"Bearer internal-key", string(body), 200},
	} {
		req := httptest.NewRequest("POST", "/v1/profile-migrations", strings.NewReader(test.body))
		req.Header.Set("Authorization", test.auth)
		out := httptest.NewRecorder()
		h.ServeHTTP(out, req)
		if out.Code != test.status {
			t.Fatalf("got %d, want %d: %s", out.Code, test.status, out.Body.String())
		}
	}
}
