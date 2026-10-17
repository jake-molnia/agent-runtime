package command

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestT3IDEWarmLoadsBackendWithoutChangingWorkspace(t *testing.T) {
	paths := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths <- r.URL.RequestURI()
		w.Header().Set("Location", "/?folder=/workspace/previous")
		w.WriteHeader(http.StatusFound)
	}))
	defer server.Close()
	if err := warmT3IDE(context.Background(), server.URL+"/"); err != nil {
		t.Fatal(err)
	}
	if len(paths) != 1 || <-paths != "/" {
		t.Fatal("warmup changed the workspace or followed its redirect")
	}
}

func TestT3IDERestartsChildWithoutStoppingWorker(t *testing.T) {
	testT3IDERestart(t, false)
}

func TestT3IDEKeepsWorkerAliveAcrossRepeatedStartupFailures(t *testing.T) {
	testT3IDERestart(t, true)
}

func testT3IDERestart(t *testing.T, failStartup bool) {
	t.Helper()
	root := t.TempDir()
	eventsPath := filepath.Join(root, "events")
	if err := syscall.Mkfifo(eventsPath, 0600); err != nil {
		t.Fatal(err)
	}
	pipe, err := os.OpenFile(eventsPath, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer pipe.Close()
	events := make(chan string, 10)
	go func() {
		scanner := bufio.NewScanner(pipe)
		for scanner.Scan() {
			events <- scanner.Text()
		}
		close(events)
	}()
	ide := filepath.Join(root, "fake-ide")
	worker := filepath.Join(root, "fake-worker")
	ideLifetime := "sleep 60 &\nwait\n"
	if failStartup {
		ideLifetime = "exit 42\n"
	}
	for path, script := range map[string]string{
		ide:    "#!/bin/sh\necho diagnostic-stdout\necho diagnostic-stderr >&2\nprintf '%s\\n' \"$@\" > \"$T3_WORKSPACE_ROOT/ide-args\"\necho \"ide $$ $PPID\" > \"$T3_WORKSPACE_ROOT/events\"\n" + ideLifetime,
		worker: "#!/bin/sh\necho \"worker $$ $T3_IDE_PID\" > \"$T3_WORKSPACE_ROOT/events\"\nexec sleep 60\n",
	} {
		if err := os.WriteFile(path, []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("T3_WORKSPACE_ROOT", root)
	t.Setenv("T3_IDE_BINARY", ide)
	t.Setenv("T3_WORKER_NODE", worker)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runT3SessionTools(ctx, root, filepath.Join(root, "state"), desktopEnv(root, root)) }()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(7 * time.Second):
			t.Error("session failed to stop")
		}
	}()
	next := func() (string, int, int) {
		t.Helper()
		select {
		case line := <-events:
			var kind string
			var pid, parent int
			if _, err := fmt.Sscanf(line, "%s %d %d", &kind, &pid, &parent); err != nil {
				t.Fatal(err)
			}
			return kind, pid, parent
		case err := <-done:
			t.Fatalf("session stopped: %v", err)
		case <-time.After(5 * time.Second):
			t.Fatal("child did not announce startup")
		}
		return "", 0, 0
	}
	var idePID, supervisorPID, workerPID, workerSupervisorPID int
	for range 2 {
		kind, pid, parent := next()
		if kind == "ide" {
			idePID = pid
			supervisorPID = parent
		} else {
			workerPID = pid
			workerSupervisorPID = parent
			if parent <= 0 {
				t.Fatal("missing supervisor PID")
			}
		}
	}
	if idePID <= 0 || workerPID <= 0 || supervisorPID <= 0 {
		t.Fatal("missing child identity")
	}
	if supervisorPID != workerSupervisorPID {
		t.Fatal("worker is not tracking the stable supervisor")
	}
	if !failStartup {
		if err := syscall.Kill(idePID, syscall.SIGTERM); err != nil {
			t.Fatal(err)
		}
	}
	kind, replacement, parent := next()
	if kind != "ide" || replacement == idePID || parent != supervisorPID {
		t.Fatal("IDE did not restart under the same supervisor")
	}
	if err := syscall.Kill(workerPID, 0); err != nil {
		t.Fatalf("worker stopped with IDE: %v", err)
	}
	args, err := os.ReadFile(filepath.Join(root, "ide-args"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(args), "--disable-workspace-trust\n") {
		t.Fatal("workspace trust still enabled")
	}
	diagnostics, err := os.ReadFile(filepath.Join(root, "state", "ide", "code-server.log"))
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"diagnostic-stdout", "diagnostic-stderr"} {
		if !strings.Contains(string(diagnostics), text) {
			t.Fatalf("missing %s", text)
		}
	}
}

func TestT3IDELogRetainsBoundedPrivateDiagnostics(t *testing.T) {
	path := filepath.Join(t.TempDir(), "code-server.log")
	log, err := openIDELog(path, 8)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := log.Write([]byte("1111111122222222333333334444")); err != nil {
		t.Fatal(err)
	}
	if err := log.Close(); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{path: "4444", path + ".1": "33333333"} {
		got, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != want {
			t.Fatalf("unexpected retained log: %q", got)
		}
		info, err := os.Stat(name)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Fatalf("diagnostics are not private: %v", info.Mode())
		}
	}
}

func TestT3IDEShutdownReapsEditorBeforeOuterDeadline(t *testing.T) {
	root := t.TempDir()
	eventsPath := filepath.Join(root, "ready")
	if err := syscall.Mkfifo(eventsPath, 0600); err != nil {
		t.Fatal(err)
	}
	pipe, err := os.OpenFile(eventsPath, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer pipe.Close()
	ide := filepath.Join(root, "fake-ide")
	if err := os.WriteFile(ide, []byte("#!/bin/sh\ntrap '' TERM\necho $$ > \"$T3_WORKSPACE_ROOT/ready\"\nsleep 60 &\nwait\n"), 0700); err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	supervisor := exec.Command(executable, "t3-ide")
	supervisor.Env = append(os.Environ(), "T3_WORKSPACE_ROOT="+root, "T3_WORKER_STATE_DIR="+filepath.Join(root, "state"), "T3_IDE_BINARY="+ide)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runChildStarted(ctx, supervisor, nil) }()
	ready := make(chan int, 1)
	go func() {
		var pid int
		if _, err := fmt.Fscanln(pipe, &pid); err == nil {
			ready <- pid
		}
	}()
	var pid int
	select {
	case pid = <-ready:
	case err := <-done:
		t.Fatalf("supervisor exited before editor started: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("editor did not start")
	}
	// This PID is announced by our own child at startup; reap its owned group on test failure too.
	defer syscall.Kill(-pid, syscall.SIGKILL)
	started := time.Now()
	cancel()
	select {
	case <-done:
	case <-time.After(7 * time.Second):
		t.Fatal("session shutdown timed out")
	}
	if elapsed := time.Since(started); elapsed >= 4500*time.Millisecond {
		t.Fatalf("editor cleanup consumed the outer five-second shutdown budget: %s", elapsed)
	}
	if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("editor %d survived outer supervisor shutdown: %v", pid, err)
	}
}
