package command

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
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

func TestT3ExecutionWorkerStartsBeforeIDEHealth(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:8085")
	if err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	probed := make(chan struct{})
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(probed) })
		w.WriteHeader(http.StatusServiceUnavailable)
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
	if err := os.WriteFile(worker, []byte("#!/bin/sh\nprintf '%s' \"$DISPLAY $HOME $T3_IDE_PID\" > \"$T3_TEST_CAPTURE\"\nexec sleep 60\n"), 0700); err != nil {
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
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(marker); err == nil {
			fields := strings.Fields(string(data))
			if len(fields) != 3 || fields[0] != ":99" || fields[1] != filepath.Join(root, "home") {
				t.Fatalf("unexpected worker environment: %q", data)
			}
			pid, err := strconv.Atoi(fields[2])
			if err != nil || pid <= 0 || syscall.Kill(pid, 0) != nil {
				t.Fatalf("worker did not receive a live owned IDE PID: %q", fields[2])
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("execution worker did not start with the desktop environment")
}

func TestT3CoreStartsBeforeDesktopAndStopsOnAuxiliaryExit(t *testing.T) {
	for _, failedService := range []string{"desktop", "ide"} {
		t.Run(failedService, func(t *testing.T) {
			root := t.TempDir()
			config := filepath.Join(root, "config")
			if err := os.Mkdir(config, 0700); err != nil {
				t.Fatal(err)
			}
			for binary, script := range map[string]string{
				"xauth":         "exit 0",
				"xdpyinfo":      "exit 0",
				"dbus-send":     "exit 0",
				"dbus-daemon":   "exec sleep 60",
				"Xtigervnc":     "exec sleep 60",
				"xfce4-session": "echo $$ > \"$SANDBOX_ROOT/desktop-pid\"; exec sleep 60",
				"wmctrl":        "exit 1",
				"ide":           "exec sleep 60",
				"worker":        "echo \"$$ $T3_IDE_PID\" > \"$T3_WORKSPACE_ROOT/worker-pids\"; exec sleep 60",
			} {
				if err := os.WriteFile(filepath.Join(root, binary), []byte("#!/bin/sh\n"+script+"\n"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("PATH", root+":"+os.Getenv("PATH"))
			t.Setenv("SANDBOX_DESKTOP_CONFIG", config)
			t.Setenv("T3_WORKSPACE_ROOT", root)
			t.Setenv("T3_WORKER_STATE_DIR", filepath.Join(root, "state"))
			t.Setenv("T3_IDE_BINARY", filepath.Join(root, "ide"))
			t.Setenv("T3_WORKER_NODE", filepath.Join(root, "worker"))
			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan struct{})
			var sessionErr error
			go func() {
				defer close(done)
				sessionErr = t3Session(ctx)
			}()
			defer func() {
				cancel()
				select {
				case <-done:
				case <-time.After(7 * time.Second):
					t.Error("session did not stop its children")
				}
			}()
			readPIDs := func(name string, count int) []int {
				t.Helper()
				deadline := time.Now().Add(3 * time.Second)
				for time.Now().Before(deadline) {
					data, _ := os.ReadFile(filepath.Join(root, name))
					fields := strings.Fields(string(data))
					if len(fields) == count {
						pids := make([]int, count)
						for i, value := range fields {
							pid, err := strconv.Atoi(value)
							if err != nil || pid <= 0 {
								t.Fatalf("invalid child PID: %q", value)
							}
							pids[i] = pid
						}
						return pids
					}
					select {
					case <-done:
						t.Fatalf("session exited before %s: %v", name, sessionErr)
					case <-time.After(10 * time.Millisecond):
					}
				}
				t.Fatalf("%s never started while desktop and IDE were unready", name)
				return nil
			}
			pids := readPIDs("worker-pids", 2)
			desktopPID := readPIDs("desktop-pid", 1)[0]
			failedPID, binary := desktopPID, "xfce4-session"
			if failedService == "ide" {
				failedPID, binary = pids[1], "ide"
			}
			if err := syscall.Kill(failedPID, syscall.SIGTERM); err != nil {
				t.Fatal(err)
			}
			select {
			case <-done:
			case <-time.After(7 * time.Second):
				t.Fatal("auxiliary exit did not stop the session")
			}
			if sessionErr == nil || !strings.Contains(sessionErr.Error(), binary) {
				t.Fatalf("auxiliary failure was lost: %v", sessionErr)
			}
			for _, pid := range append(pids, desktopPID) {
				if syscall.Kill(pid, 0) == nil {
					t.Errorf("session left child %d running", pid)
				}
			}
		})
	}
}
