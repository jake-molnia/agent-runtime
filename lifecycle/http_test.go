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
}

func (f *fakeTasks) Submit(context.Context, Request) (string, error) {
	f.submissions++
	return "task-one", nil
}
func (f *fakeTasks) Status(_ context.Context, id string) (TaskStatus, error) {
	status := f.status
	if status == "" {
		status = "RUNNING"
	}
	return TaskStatus{ID: id, Status: status}, nil
}
func TestControlHTTPAuthorizationAndDurableSubmission(t *testing.T) {
	c, _, _ := fixture(t)
	tasks := &fakeTasks{}
	handler := Handler(c, tasks, "internal-key")
	body, _ := json.Marshal(request(1, EnsureRunning))
	for _, auth := range []string{"", "wrong", "internal-key"} {
		req := httptest.NewRequest("POST", "/v1/operations", strings.NewReader(string(body)))
		req.Header.Set("Authorization", auth)
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		if out.Code != 401 {
			t.Fatalf("accepted authorization %q", auth)
		}
	}
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest("POST", "/v1/operations", strings.NewReader(string(body)))
		req.Header.Set("Authorization", "Bearer internal-key")
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		if out.Code != 202 {
			t.Fatalf("submit: %d %s", out.Code, out.Body.String())
		}
	}
	if tasks.submissions != 1 {
		t.Fatal("duplicate POST submitted new task")
	}
	req := httptest.NewRequest("POST", "/v1/operations", strings.NewReader(string(body)+" {}"))
	req.Header.Set("Authorization", "Bearer internal-key")
	out := httptest.NewRecorder()
	handler.ServeHTTP(out, req)
	if out.Code != 400 {
		t.Fatal("trailing JSON accepted")
	}
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

func TestFailedLifecycleSubmissionCanRetrySameIntent(t *testing.T) {
	c, _, _ := fixture(t)
	tasks := &fakeTasks{}
	handler := Handler(c, tasks, "internal-key")
	body, _ := json.Marshal(request(1, EnsureRunning))
	submit := func() {
		t.Helper()
		req := httptest.NewRequest("POST", "/v1/operations", strings.NewReader(string(body)))
		req.Header.Set("Authorization", "Bearer internal-key")
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		if out.Code != 202 {
			t.Fatalf("submit: %d %s", out.Code, out.Body.String())
		}
	}
	submit()
	tasks.status = "FAILED"
	submit()
	if tasks.submissions != 2 {
		t.Fatal("failed task permanently pinned the request")
	}
}

func TestProfileMigrationHTTPRequiresManagementAuthAndStrictJSON(t *testing.T) {
	c, _, _ := fixture(t)
	s := run(t, c, request(1, EnsureRunning))
	s = run(t, c, stopRequest(2, ReleaseCompute, s))
	c.Profiles["next"] = c.Profiles["default"]
	migration := ProfileMigration{WorkspaceID: s.Request.WorkspaceID, FromProfile: "default", ToProfile: "next", ExpectedProfileHash: s.ProfileHash, ExpectedRevision: s.Request.Revision, ExpectedEpoch: s.Epoch}
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
