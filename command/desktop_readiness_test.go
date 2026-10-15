package command

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

type desktopProbeTransport struct {
	address func() string
}

func (transport desktopProbeTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	request = request.Clone(request.Context())
	request.URL.Host = transport.address()
	return http.DefaultTransport.RoundTrip(request)
}

func routeDesktopProbe(t *testing.T, address func() string) {
	t.Helper()
	previous := desktopHTTP
	desktopHTTP = &http.Client{Transport: desktopProbeTransport{address}, Timeout: time.Second}
	t.Cleanup(func() { desktopHTTP = previous })
}

func TestDesktopHealthTracksBrowserRecovery(t *testing.T) {
	for _, exit := range []string{"close", "crash"} {
		t.Run(exit, func(t *testing.T) {
			dir := t.TempDir()
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- runDesktopBrowser(ctx, func() *exec.Cmd { return browserTestCommand(dir) }) }()
			t.Cleanup(func() {
				cancel()
				select {
				case <-done:
				case <-time.After(7 * time.Second):
					t.Error("browser supervisor did not stop")
				}
			})
			routeDesktopProbe(t, func() string {
				address, _ := os.ReadFile(filepath.Join(dir, "address"))
				return string(address)
			})
			ready := make(chan struct{})
			close(ready)
			handler := desktopHandler(ctx, ready, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
			checkHealth := func(want int) {
				t.Helper()
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/health", nil))
				if response.Code != want {
					t.Fatalf("desktop health = %d, want %d: %s", response.Code, want, response.Body.String())
				}
			}
			setReady := func(generation string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(dir, "cdp-ready"), []byte(generation), 0600); err != nil {
					t.Fatal(err)
				}
			}
			address, _ := awaitBrowserProcess(t, dir, 1, done)
			setReady("1")
			checkHealth(http.StatusOK)
			client := &http.Client{Timeout: time.Second}
			if response, err := client.Post(address+"/"+exit, "text/plain", nil); err == nil {
				response.Body.Close()
			}
			checkHealth(http.StatusServiceUnavailable)
			awaitBrowserProcess(t, dir, 2, done)
			checkHealth(http.StatusServiceUnavailable)
			setReady("2")
			checkHealth(http.StatusOK)
		})
	}
}
