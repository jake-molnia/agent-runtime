package opencode

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func TestReadyMCPNativeReconnectAfterInitial503(t *testing.T) {
	binary := os.Getenv("AGENT_RUNTIME_TEST_OPENCODE_BINARY")
	if binary == "" {
		t.Skip("set AGENT_RUNTIME_TEST_OPENCODE_BINARY to pinned OpenCode V2 2.0.26")
	}
	var healthy atomic.Bool
	var initialized atomic.Int32
	broker := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !healthy.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		var request struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if len(request.ID) == 0 {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		var result any
		switch request.Method {
		case "initialize":
			initialized.Add(1)
			result = map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "recovered", "version": "1"}}
		case "tools/list":
			result = map[string]any{"tools": []any{}}
		default:
			t.Errorf("unexpected MCP method %s", request.Method)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"jsonrpc": "2.0", "id": request.ID, "result": result})
	}))
	defer broker.Close()
	root := t.TempDir()
	config := filepath.Join(root, "opencode.json")
	body, err := json.Marshal(map[string]any{"mcp": map[string]any{"servers": map[string]any{"broker": map[string]any{"type": "remote", "url": broker.URL, "oauth": false}}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(config, body, 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	cmd := exec.Command(binary, "serve", "--hostname", "127.0.0.1", "--port", strconv.Itoa(port))
	cmd.Dir = root
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + root, "XDG_CONFIG_HOME=" + root + "/config", "XDG_DATA_HOME=" + root + "/data", "XDG_STATE_HOME=" + root + "/state", "XDG_CACHE_HOME=" + root + "/cache", "OPENCODE_CONFIG=" + config, "OPENCODE_SERVER_PASSWORD=local-mcp-readiness-test", "OPENCODE_DISABLE_MODELS_FETCH=1", "OPENCODE_DISABLE_PROJECT_CONFIG=1", "GIT_TERMINAL_PROMPT=0"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	defer func() {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-done
		}
	}()
	auth := &http.Request{Header: make(http.Header)}
	auth.SetBasicAuth("opencode", "local-mcp-readiness-test")
	client, err := New(fmt.Sprintf("http://127.0.0.1:%d", port), auth.Header, nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	if err := client.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	query := url.Values{"location[directory]": {root}}
	activation, err := client.Do(ctx, http.MethodGet, "/api/integration", Arguments{Query: query})
	if err != nil {
		t.Fatal(err)
	}
	activation.Body.Close()
	for {
		inventory, err := Decode[mcpInventory](client.Do(ctx, http.MethodGet, "/api/mcp", Arguments{Query: query}))
		if err != nil {
			t.Fatal(err)
		}
		failed := false
		for _, entry := range inventory.Data {
			if entry.Name == "broker" && entry.Status.Status == "failed" {
				if !strings.Contains(entry.Status.Error, "503") {
					t.Fatalf("unexpected native failure: %s", mcpState(entry.Status.Status))
				}
				failed = true
			}
		}
		if failed {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
	healthy.Store(true)
	if err := client.ReadyMCP(ctx, root, []string{"broker"}); err != nil {
		t.Fatal(err)
	}
	if initialized.Load() != 1 {
		t.Fatalf("expected one healthy native initialization, got %d", initialized.Load())
	}
}
