package command

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestColdSandboxInitializationWaitsForDesktop(t *testing.T) {
	ready := make(chan struct{})
	var initialized atomic.Bool
	handler := desktopHandler(context.Background(), ready, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/initialize" {
			initialized.Store(true)
		}
		w.WriteHeader(http.StatusOK)
	}))
	health := httptest.NewRecorder()
	handler.ServeHTTP(health, httptest.NewRequest("GET", "/health", nil))
	if health.Code != http.StatusServiceUnavailable {
		t.Fatal("cold desktop reported healthy")
	}
	done := make(chan struct{})
	response := httptest.NewRecorder()
	go func() {
		defer close(done)
		handler.ServeHTTP(response, httptest.NewRequest("POST", "/initialize", nil))
	}()
	select {
	case <-done:
		t.Fatal("initialization did not wait for the desktop")
	case <-time.After(20 * time.Millisecond):
	}
	close(ready)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("initialization did not resume")
	}
	if !initialized.Load() || response.Code != http.StatusOK {
		t.Fatal("ready desktop did not initialize")
	}
	health = httptest.NewRecorder()
	handler.ServeHTTP(health, httptest.NewRequest("GET", "/health", nil))
	if health.Code != http.StatusOK {
		t.Fatal("ready desktop did not report healthy")
	}
}

func TestDesktopInitializationRespectsCancellation(t *testing.T) {
	for _, stopRuntime := range []bool{false, true} {
		runtimeCtx, stop := context.WithCancel(context.Background())
		requestCtx, cancel := context.WithCancel(context.Background())
		if stopRuntime {
			stop()
		} else {
			cancel()
		}
		handler := desktopHandler(runtimeCtx, make(chan struct{}), http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
			t.Error("initialization proceeded after cancellation")
		}))
		handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/initialize", nil).WithContext(requestCtx))
		cancel()
		stop()
	}
}

func TestDesktopEnvironmentDoesNotInheritCredentials(t *testing.T) {
	for _, key := range []string{"OPENAI_API_KEY", "ANTHROPIC_API_KEY", "OPENCODE_SERVER_PASSWORD", "TS_AUTHKEY", "AIO_API_KEY", "EXTRA_MCP_SERVERS", "DBUS_SESSION_BUS_ADDRESS"} {
		t.Setenv(key, "provisioner-secret")
	}
	env := desktopEnv("/workspace", "/tmp/desktop")
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "provisioner-secret") {
		t.Fatal("desktop inherited provisioner credentials or endpoints")
	}
	for _, expected := range []string{"HOME=/tmp/desktop/home", "DISPLAY=:99", "XAUTHORITY=/tmp/desktop/Xauthority", "DBUS_SESSION_BUS_ADDRESS=unix:path=/tmp/desktop/bus", "AIO_COMPUTER_USE_URL=http://127.0.0.1:18100"} {
		if !strings.Contains(joined, expected+"\n") {
			t.Fatalf("missing environment binding: %s", expected)
		}
	}
}

func TestDesktopMCPReadinessNegotiatesSessionAndChecksTools(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			initialized, closed := false, false
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method == http.MethodDelete {
					closed = true
					w.WriteHeader(http.StatusNoContent)
					return
				}
				var rpc struct {
					ID     int    `json:"id"`
					Method string `json:"method"`
				}
				if err := json.NewDecoder(r.Body).Decode(&rpc); err != nil {
					t.Error(err)
				}
				var result any
				if rpc.Method == "initialize" {
					w.Header().Set("Mcp-Session-Id", "test-session")
					result = map[string]any{"protocolVersion": "2025-03-26"}
				} else {
					if r.Header.Get("Mcp-Session-Id") != "test-session" || r.Header.Get("MCP-Protocol-Version") != "2025-03-26" {
						t.Error("MCP session/protocol not retained")
					}
					if rpc.Method == "notifications/initialized" {
						initialized = true
						w.WriteHeader(http.StatusAccepted)
						return
					}
					if !initialized || rpc.Method != "tools/list" {
						t.Errorf("unexpected MCP sequence: %s", rpc.Method)
					}
					result = map[string]any{"tools": []any{map[string]string{"name": "screenshot"}}}
				}
				data, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": rpc.ID, "result": result})
				if stream {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprintf(w, "event: message\ndata: %s\n\n", data)
				} else {
					w.Header().Set("Content-Type", "application/json")
					w.Write(data)
				}
			}))
			defer server.Close()
			if err := desktopMCPReady(context.Background(), server.URL, []string{"screenshot"}); err != nil {
				t.Fatal(err)
			}
			if !closed {
				t.Fatal("readiness session leaked")
			}
			if err := desktopMCPReady(context.Background(), server.URL, []string{"missing"}); err == nil {
				t.Fatal("accepted MCP server missing a required tool")
			}
		})
	}
}

func TestDesktopReadinessStopsWithServiceContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	attempts := 0
	err := waitDesktop(ctx, "failed service", func(context.Context) error {
		attempts++
		cancel()
		return errors.New("not ready")
	})
	if !errors.Is(err, context.Canceled) || attempts != 1 {
		t.Fatalf("readiness ignored cancellation: %v (%d attempts)", err, attempts)
	}
}

func TestDesktopChildEarlyExitAndCancellation(t *testing.T) {
	if err := runChild(context.Background(), exec.Command("sh", "-c", "exit 0")); err == nil {
		t.Fatal("early successful process exit must fail the runtime")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := runChild(ctx, exec.Command("sleep", "60")); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("child cancellation: %v", err)
	}
}
