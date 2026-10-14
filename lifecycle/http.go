package lifecycle

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
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
		if name := r.URL.Query().Get("profile"); name != "" {
			profile, ok := c.Profiles[name]
			if !ok {
				http.Error(w, "unknown execution profile", http.StatusBadRequest)
				return
			}
			// Unavailable or unsupported reservation inventory remains unknown, not zero.
			snapshot.RequestCapacity, _ = ProfileRequestCapacity(r.Context(), c.Control.API, name, profile)
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
	c.workspaceRoutes(mux, tasks)
	gateway := http.NewServeMux()
	gateway.Handle("/v1/execution/{profile}/{workspaceId}/{path...}", c.gateway())
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			writeAPIError(w, 401, "unauthorized", "Management authorization required", false)
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
