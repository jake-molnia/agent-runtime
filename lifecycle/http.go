package lifecycle

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

type TaskStatus struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

func (s TaskStatus) retryable() bool { return s.Status == "FAILED" || s.Status == "CANCELLED" }

type Tasks interface {
	Submit(context.Context, Request) (string, error)
	Status(context.Context, string) (TaskStatus, error)
}

func Handler(c *Controller, tasks Tasks, token string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/ide-origins", func(w http.ResponseWriter, r *http.Request) {
		var request IDERequest
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		d.DisallowUnknownFields()
		if err := d.Decode(&request); err != nil {
			http.Error(w, "invalid request", 400)
			return
		}
		if err := d.Decode(new(any)); !errors.Is(err, io.EOF) {
			http.Error(w, "trailing request data", 400)
			return
		}
		result, err := c.EnsureIDE(r.Context(), request)
		if err != nil {
			http.Error(w, "IDE origin unavailable", 409)
			return
		}
		writeJSON(w, 200, result)
	})
	mux.HandleFunc("GET /v1/resources", func(w http.ResponseWriter, r *http.Request) {
		if c.Resources == nil {
			http.Error(w, "resources unavailable", 503)
			return
		}
		snapshot, err := c.Resources(r.Context())
		if err != nil {
			http.Error(w, "resources unavailable", 503)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		writeJSON(w, 200, snapshot)
	})
	mux.HandleFunc("POST /v1/profile-migrations", func(w http.ResponseWriter, r *http.Request) {
		var request ProfileMigration
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		d.DisallowUnknownFields()
		if err := d.Decode(&request); err != nil {
			http.Error(w, "invalid request", 400)
			return
		}
		if err := d.Decode(new(any)); !errors.Is(err, io.EOF) {
			http.Error(w, "trailing request data", 400)
			return
		}
		state, err := c.MigrateProfile(r.Context(), request)
		if err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		writeJSON(w, 200, state)
	})
	mux.HandleFunc("POST /v1/operations", func(w http.ResponseWriter, r *http.Request) {
		var request Request
		d := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		d.DisallowUnknownFields()
		if err := d.Decode(&request); err != nil {
			http.Error(w, "invalid request", 400)
			return
		}
		if err := d.Decode(new(any)); !errors.Is(err, io.EOF) {
			http.Error(w, "trailing request data", 400)
			return
		}
		state, err := c.Admit(r.Context(), request)
		if err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		taskID := state.TaskID
		if taskID != "" && state.CompletedOperation != request.OperationID {
			status, err := tasks.Status(r.Context(), taskID)
			if err != nil {
				http.Error(w, "task status unavailable; retry same operation", 503)
				return
			}
			if status.retryable() {
				taskID = ""
			}
		}
		if taskID == "" {
			taskID, err = tasks.Submit(r.Context(), request)
			if err != nil {
				http.Error(w, "task submission failed; retry same operation", 503)
				return
			}
			if err = c.SetTask(r.Context(), request, taskID); err != nil {
				http.Error(w, "operation changed; reconcile workspace", 409)
				return
			}
		}
		writeJSON(w, 202, map[string]string{"taskId": taskID})
	})
	mux.HandleFunc("GET /v1/operations/{taskId}", func(w http.ResponseWriter, r *http.Request) {
		result, err := tasks.Status(r.Context(), r.PathValue("taskId"))
		if err != nil {
			http.Error(w, "task unavailable", 503)
			return
		}
		writeJSON(w, 200, result)
	})
	mux.HandleFunc("GET /v1/workspaces/{workspaceId}", func(w http.ResponseWriter, r *http.Request) {
		access, err := c.Access(r.Context(), r.URL.Query().Get("profile"), r.PathValue("workspaceId"))
		if err != nil {
			status := http.StatusServiceUnavailable
			if apierrors.IsNotFound(err) {
				status = http.StatusNotFound
			}
			http.Error(w, "workspace unavailable", status)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		if c.PublicURL != "" && access.State.Endpoint != "" {
			access.State.Endpoint = executionURL(c.PublicURL, r.URL.Query().Get("profile"), r.PathValue("workspaceId"))
		}
		writeJSON(w, 200, access)
	})
	gateway := http.NewServeMux()
	gateway.Handle("/v1/execution/{profile}/{workspaceId}/{path...}", c.gateway())
	ideProxy := c.ideProxy()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, idePrefix) {
			ideProxy.ServeHTTP(w, r)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/v1/execution/") {
			gateway.ServeHTTP(w, r)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		given := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") || token == "" || subtle.ConstantTimeCompare([]byte(given), []byte(token)) != 1 {
			http.Error(w, "unauthorized", 401)
			return
		}
		mux.ServeHTTP(w, r)
	})
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
