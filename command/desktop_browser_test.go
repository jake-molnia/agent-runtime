package command

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sync/errgroup"
)

// The helper is a real process with a control socket. Closing the browser ends
// only that process, as Chromium does when its last window is closed.
func TestDesktopBrowserProcess(t *testing.T) {
	if os.Getenv("T3_BROWSER_TEST_PROCESS") != "1" {
		return
	}
	dir := os.Getenv("T3_BROWSER_TEST_STATE")
	count := 0
	if data, err := os.ReadFile(filepath.Join(dir, "generation")); err == nil {
		count, _ = strconv.Atoi(string(data))
	}
	count++
	if err := os.WriteFile(filepath.Join(dir, "generation"), []byte(strconv.Itoa(count)), 0600); err != nil {
		os.Exit(2)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		os.Exit(2)
	}
	if err := os.WriteFile(filepath.Join(dir, "address"), []byte(listener.Addr().String()), 0600); err != nil {
		os.Exit(2)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/close" {
			os.Exit(0)
		}
		if r.URL.Path == "/crash" {
			os.Exit(7)
		}
		_ = json.NewEncoder(w).Encode(map[string]int{"pid": os.Getpid(), "generation": count})
	})
	_ = http.Serve(listener, mux)
	os.Exit(2)
}

func browserTestCommand(dir string) *exec.Cmd {
	cmd := exec.Command(os.Args[0], "-test.run=^TestDesktopBrowserProcess$")
	cmd.Env = append(os.Environ(), "T3_BROWSER_TEST_PROCESS=1", "T3_BROWSER_TEST_STATE="+dir)
	return cmd
}

type browserProcessState struct {
	PID        int `json:"pid"`
	Generation int `json:"generation"`
}

func awaitBrowserProcess(t *testing.T, dir string, generation int, done <-chan error) (string, browserProcessState) {
	t.Helper()
	client := &http.Client{Timeout: 200 * time.Millisecond}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			t.Fatalf("session terminated after browser exit: %v", err)
		default:
		}
		if data, err := os.ReadFile(filepath.Join(dir, "address")); err == nil {
			address := "http://" + string(data)
			if res, err := client.Get(address); err == nil {
				var state browserProcessState
				err = json.NewDecoder(res.Body).Decode(&state)
				res.Body.Close()
				if err == nil && state.Generation == generation {
					return address, state
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("browser never became usable")
	return "", browserProcessState{}
}

func TestDesktopBrowserExitPreservesExecutionAndRestarts(t *testing.T) {
	for _, exit := range []string{"close", "crash"} {
		t.Run(exit, func(t *testing.T) {
			browserDir, workerDir := t.TempDir(), t.TempDir()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			g, lifetime := errgroup.WithContext(ctx)
			g.Go(func() error {
				return runDesktopBrowser(lifetime, func() *exec.Cmd { return browserTestCommand(browserDir) })
			})
			g.Go(func() error { return runChild(lifetime, browserTestCommand(workerDir)) })
			done := make(chan error, 1)
			go func() { done <- g.Wait(); close(done) }()
			var pids []int
			var addresses []string
			defer func() {
				cancel()
				select {
				case <-done:
				case <-time.After(7 * time.Second):
					t.Error("session children did not stop")
				}
				for _, address := range addresses {
					if conn, err := net.DialTimeout("tcp", address[len("http://"):], 100*time.Millisecond); err == nil {
						conn.Close()
						t.Errorf("listener leaked: %s", address)
					}
				}
				for _, pid := range pids {
					if err := syscall.Kill(pid, 0); !errors.Is(err, syscall.ESRCH) {
						t.Errorf("process %d leaked: %v", pid, err)
					}
				}
			}()
			browserAddress, first := awaitBrowserProcess(t, browserDir, 1, done)
			workerAddress, worker := awaitBrowserProcess(t, workerDir, 1, done)
			pids = append(pids, first.PID, worker.PID)
			addresses = append(addresses, browserAddress, workerAddress)
			closeTime := time.Now()
			client := &http.Client{Timeout: time.Second}
			if res, err := client.Post(browserAddress+"/"+exit, "text/plain", nil); err == nil {
				res.Body.Close()
			}
			secondAddress, second := awaitBrowserProcess(t, browserDir, 2, done)
			pids = append(pids, second.PID)
			addresses = append(addresses, secondAddress)
			if time.Since(closeTime) < time.Second {
				t.Fatal("browser restarted without backoff")
			}
			if second.PID == first.PID {
				t.Fatal("browser process was not replaced")
			}
			closeTime = time.Now()
			if res, err := client.Post(secondAddress+"/"+exit, "text/plain", nil); err == nil {
				res.Body.Close()
			}
			thirdAddress, third := awaitBrowserProcess(t, browserDir, 3, done)
			pids = append(pids, third.PID)
			addresses = append(addresses, thirdAddress)
			if time.Since(closeTime) < 2*time.Second {
				t.Fatal("repeated browser exits did not increase the backoff")
			}
			// The same provider/control process, not merely a newly started substitute,
			// must remain reachable while the desktop browser is replaced.
			res, err := client.Get(workerAddress)
			if err != nil {
				t.Fatal(err)
			}
			var still browserProcessState
			err = json.NewDecoder(res.Body).Decode(&still)
			res.Body.Close()
			if err != nil || still != worker {
				t.Fatalf("execution sentinel changed: %+v %v", still, err)
			}
		})
	}
}

func TestDesktopBrowserStartFailureRemainsFatal(t *testing.T) {
	err := runDesktopBrowser(context.Background(), func() *exec.Cmd { return exec.Command(filepath.Join(t.TempDir(), "missing-browser")) })
	if err == nil {
		t.Fatal("browser launch failure was ignored")
	}
}

func TestDesktopBrowserBackoffCancelsWithoutRelaunch(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runDesktopBrowser(ctx, func() *exec.Cmd { return browserTestCommand(dir) }) }()
	address, state := awaitBrowserProcess(t, dir, 1, done)
	client := &http.Client{Timeout: time.Second}
	if res, err := client.Post(address+"/close", "text/plain", nil); err == nil {
		res.Body.Close()
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("backoff ignored cancellation")
	}
	if err := syscall.Kill(state.PID, 0); !errors.Is(err, syscall.ESRCH) {
		t.Fatalf("browser leaked: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "generation"))
	if err != nil || string(data) != "1" {
		t.Fatalf("browser relaunched after cancellation: %s %v", data, err)
	}
}
