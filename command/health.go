package command

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/hatchet-dev/hatchet/pkg/client/rest"
)

// The SDK starts its listener asynchronously and exposes no connection callback.
// Readiness therefore uses the scheduler's record of this process's unique name.
type workerHealth struct {
	mu       sync.RWMutex
	started  bool
	ready    bool
	observed time.Time
}

func (h *workerHealth) observe(name string, workers *rest.WorkerList, err error, now time.Time) {
	ready := false
	if err == nil && workers != nil && workers.Rows != nil {
		for _, worker := range *workers.Rows {
			if worker.Name == name && worker.Status != nil && *worker.Status == rest.ACTIVE && worker.LastHeartbeatAt != nil && now.Sub(*worker.LastHeartbeatAt) < 45*time.Second {
				ready = true
				break
			}
		}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.ready = ready
	h.started = h.started || ready
	h.observed = now
}
func (h *workerHealth) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	h.mu.RLock()
	ready, started, observed := h.ready, h.started, h.observed
	h.mu.RUnlock()
	ok := false
	switch r.URL.Path {
	case "/healthz":
		ok = true
	case "/startupz":
		ok = started
	case "/readyz":
		ok = ready && time.Since(observed) < 30*time.Second
	}
	if !ok {
		http.Error(w, "worker unavailable", http.StatusServiceUnavailable)
		return
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok\n"))
}
func (h *workerHealth) monitor(ctx context.Context, name string, list func(context.Context) (*rest.WorkerList, error)) error {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		check, cancel := context.WithTimeout(ctx, 5*time.Second)
		workers, err := list(check)
		cancel()
		h.observe(name, workers, err, time.Now())
		select {
		case <-ctx.Done():
			h.mu.Lock()
			h.ready = false
			h.mu.Unlock()
			return nil
		case <-ticker.C:
		}
	}
}
