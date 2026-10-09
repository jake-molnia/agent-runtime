package command

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestT3SessionEnvironmentKeepsIdentityAndSharesDesktop(t *testing.T) {
	inherited := []string{"HOME=/workspace/home", "T3_WORKER_TOKEN=worker-secret", "OPENAI_API_KEY=provider-secret", "DISPLAY=:0", "DISPLAY=:1", "XAUTHORITY=/wrong", "T3CODE_SANDBOX_IDE_URL=http://wrong", "T3_GIT_CREDENTIALS_FILE=/etc/t3-scm/credentials.json"}
	desktop := desktopEnv("/workspace", "/tmp/desktop")
	worker := strings.Join(t3WorkerEnvironment(inherited, desktop), "\n")
	for _, want := range []string{"HOME=/workspace/home", "T3_WORKER_TOKEN=worker-secret", "DISPLAY=:99", "XAUTHORITY=/tmp/desktop/Xauthority", "DBUS_SESSION_BUS_ADDRESS=unix:path=/tmp/desktop/bus", "T3CODE_SANDBOX_IDE_URL=http://127.0.0.1:8085", "T3CODE_SANDBOX_CDP_URL=http://127.0.0.1:9222", "T3CODE_SANDBOX_COMPUTER_URL=http://127.0.0.1:18091"} {
		if !strings.Contains(worker, want) {
			t.Errorf("worker missing %s", want)
		}
	}
	if strings.Count(worker, "DISPLAY=") != 1 || strings.Contains(worker, "/wrong") || strings.Contains(worker, "http://wrong") {
		t.Fatalf("worker inherited stale desktop binding: %s", worker)
	}
	ide := strings.Join(t3IDEEnvironment(inherited, desktop), "\n")
	if strings.Contains(ide, "worker-secret") || strings.Contains(ide, "provider-secret") {
		t.Fatal("IDE inherited control-plane or provider credentials")
	}
	for _, want := range []string{"HOME=/workspace/home", "DISPLAY=:99", "XAUTHORITY=/tmp/desktop/Xauthority", "T3_GIT_CREDENTIALS_FILE=/etc/t3-scm/credentials.json"} {
		if !strings.Contains(ide, want) {
			t.Errorf("IDE missing %s", want)
		}
	}
}

func TestT3ExecutionWorkerWaitsForIDE(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:8085")
	if err != nil {
		t.Fatal(err)
	}
	var ready atomic.Bool
	var once sync.Once
	probed := make(chan struct{})
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(probed) })
		if !ready.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"alive"}`))
	}))
	server.Listener.Close()
	server.Listener = listener
	server.Start()
	defer server.Close()
	root := t.TempDir()
	ide, worker, marker := filepath.Join(root, "ide"), filepath.Join(root, "worker"), filepath.Join(root, "worker-started")
	if err := os.WriteFile(ide, []byte("#!/bin/sh\nexec sleep 60\n"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(worker, []byte("#!/bin/sh\nprintf '%s' \"$DISPLAY $HOME\" > \"$T3_TEST_CAPTURE\"\nexec sleep 60\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("T3_IDE_BINARY", ide)
	t.Setenv("T3_WORKER_NODE", worker)
	t.Setenv("T3_TEST_CAPTURE", marker)
	t.Setenv("HOME", filepath.Join(root, "home"))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runT3SessionTools(ctx, root, root, desktopEnv(root, root)) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(7 * time.Second):
			t.Error("T3 session children did not stop")
		}
	}()
	select {
	case <-probed:
	case <-time.After(3 * time.Second):
		t.Fatal("IDE was not probed")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatal("execution worker started before IDE readiness")
	}
	ready.Store(true)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(marker); err == nil && string(data) == ":99 "+filepath.Join(root, "home") {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("execution worker did not start with the desktop environment")
}
