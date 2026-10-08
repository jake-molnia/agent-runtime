package runtime_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jake-molnia/agent-runtime/artifacts"
	"github.com/jake-molnia/agent-runtime/opencode"
	"github.com/jake-molnia/agent-runtime/orchestration"
	runtimeapi "github.com/jake-molnia/agent-runtime/runtime"
	"github.com/jake-molnia/agent-runtime/sandbox"
)

const dummyConfig = `{"provider":{"test":{"options":{"apiKey":"dummy-config-secret"}}}}`
const sessionExport = `{"session":{"id":"ses_test"},"messages":[{"text":"retained artifact"}]}`

// Run this test executable as the supervised server, with the real child environment.
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == "serve" {
		data := os.Getenv("XDG_DATA_HOME")
		if err := os.MkdirAll(data, 0700); err != nil {
			os.Exit(2)
		}
		if err := os.WriteFile(filepath.Join(data, "session.json"), []byte(sessionExport), 0600); err != nil {
			os.Exit(2)
		}
		mux := http.NewServeMux()
		mux.HandleFunc("/api/info", func(w http.ResponseWriter, r *http.Request) {
			cwd, _ := os.Getwd()
			env := map[string]string{"cwd": cwd}
			for _, key := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_CACHE_HOME", "OPENCODE_CONFIG"} {
				env[key] = os.Getenv(key)
			}
			_ = json.NewEncoder(w).Encode(env)
		})
		mux.HandleFunc("/api/experimental/session/ses_test/export", func(w http.ResponseWriter, r *http.Request) {
			http.ServeFile(w, r, filepath.Join(data, "session.json"))
		})
		err := http.ListenAndServe("127.0.0.1:4096", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			user, password, ok := r.BasicAuth()
			if !ok || user != "opencode" || password != os.Getenv("OPENCODE_SERVER_PASSWORD") {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			mux.ServeHTTP(w, r)
		}))
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	os.Exit(m.Run())
}

func startRuntime(t *testing.T, root string) (map[string]string, *orchestration.Engine) {
	t.Helper()
	t.Setenv("TMPDIR", t.TempDir())
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	engine := &orchestration.Engine{SecretKey: []byte("test-secret-key"), Artifacts: artifacts.Directory{Root: t.TempDir()}}
	supervisor := &runtimeapi.Supervisor{Root: root, OpenCodeBinary: binary}
	lifetime, stop := context.WithCancel(context.Background())
	handler := supervisor.Handler(lifetime)
	initialized := false
	t.Cleanup(func() {
		stop()
		if !initialized {
			return
		}
		deadline := time.Now().Add(7 * time.Second)
		for time.Now().Before(deadline) {
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, httptest.NewRequest("GET", "/health", nil))
			if w.Code == http.StatusServiceUnavailable {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Error("supervised process did not stop")
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	input := runtimeapi.Init{RunID: "test", Password: engine.Password("test"), Config: json.RawMessage(dummyConfig)}
	if _, err := supervisor.Initialize(ctx, input); err != nil {
		t.Fatal(err)
	}
	initialized = true
	if _, err := supervisor.Initialize(ctx, input); err != nil {
		t.Fatalf("repeat initialization: %v", err)
	}
	client, err := engine.Client("test", prepared())
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(ctx, "GET", "/api/info", opencode.Arguments{})
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	var env map[string]string
	if err := json.NewDecoder(response.Body).Decode(&env); err != nil {
		t.Fatal(err)
	}
	return env, engine
}

func prepared() orchestration.Prepared {
	return orchestration.Prepared{Lease: sandbox.Lease{Host: "127.0.0.1"}, SessionID: "ses_test"}
}

func git(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return string(output)
}

func TestRuntimeHomeDoesNotStageSecrets(t *testing.T) {
	root := t.TempDir()
	git(t, root, "init", "--quiet")
	env, engine := startRuntime(t, root)
	git(t, root, "add", "-A")
	if staged := git(t, root, "diff", "--cached", "--name-only"); staged != "" {
		t.Errorf("runtime files were staged: %s", staged)
	}
	if diff := git(t, root, "diff", "--cached"); strings.Contains(diff, "dummy-config-secret") {
		t.Error("resolved config secret entered the Git index")
	}
	if env["cwd"] != root {
		t.Fatalf("child cwd = %q, want %q", env["cwd"], root)
	}
	for key, path := range env {
		if key == "cwd" {
			continue
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
			t.Errorf("%s = %q is not outside checkout", key, path)
		}
	}
	for path, mode := range map[string]os.FileMode{env["HOME"]: 0700, env["OPENCODE_CONFIG"]: 0600} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != mode {
			t.Errorf("%s permissions = %o, want %o", path, info.Mode().Perm(), mode)
		}
	}
	config, err := os.ReadFile(env["OPENCODE_CONFIG"])
	if err != nil || string(config) != dummyConfig {
		t.Fatalf("resolved config was not preserved: %v", err)
	}
	// The initialize request has ended, but the server must retain data for collection.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	key, err := engine.Collect(ctx, orchestration.Request{Key: "test"}, prepared())
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := os.ReadFile(filepath.Join(engine.Artifacts.(artifacts.Directory).Root, key))
	if err != nil || string(artifact) != sessionExport {
		t.Fatalf("session export was not preserved: %v", err)
	}
}

func TestRuntimeInitializationAllowsCloneIntoWorkspace(t *testing.T) {
	source := t.TempDir()
	git(t, source, "init", "--quiet")
	if err := os.WriteFile(filepath.Join(source, "README.md"), []byte("fixture repository\n"), 0600); err != nil {
		t.Fatal(err)
	}
	git(t, source, "add", "README.md")
	git(t, source, "-c", "user.name=Test", "-c", "user.email=test@example.invalid", "commit", "--quiet", "-m", "fixture")
	root := t.TempDir()
	startRuntime(t, root)
	// Force the problematic ordering: initialization writes before preparation clones.
	git(t, root, "clone", "--quiet", source, ".")
	if status := git(t, root, "status", "--porcelain"); status != "" {
		t.Fatalf("checkout is not clean: %s", status)
	}
}

func TestRuntimeRejectsTemporaryHomeInsideWorkspace(t *testing.T) {
	for _, location := range []string{"root", "subdirectory", "symlink"} {
		t.Run(location, func(t *testing.T) {
			root := t.TempDir()
			temp := root
			if location == "subdirectory" {
				temp = filepath.Join(root, "tmp")
				if err := os.Mkdir(temp, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if location == "symlink" {
				temp = filepath.Join(t.TempDir(), "tmp")
				if err := os.Symlink(root, temp); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("TMPDIR", temp)
			supervisor := &runtimeapi.Supervisor{Root: root, OpenCodeBinary: "/missing-opencode-test-binary"}
			_, err := supervisor.Initialize(context.Background(), runtimeapi.Init{
				RunID: "test", Password: strings.Repeat("p", 32), Config: json.RawMessage(dummyConfig),
			})
			if err == nil || err.Error() != "runtime temporary directory must be outside workspace" {
				t.Fatalf("expected unsafe temporary directory rejection, got %v", err)
			}
			entries, err := os.ReadDir(temp)
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range entries {
				if entry.Name() != "tmp" {
					t.Errorf("initialization wrote %q inside workspace", entry.Name())
				}
			}
		})
	}
}

func TestRuntimeFailedInitializationRemovesSecrets(t *testing.T) {
	root, temp := t.TempDir(), t.TempDir()
	t.Setenv("TMPDIR", temp)
	supervisor := &runtimeapi.Supervisor{Root: root, OpenCodeBinary: filepath.Join(root, "missing-opencode")}
	for attempt := 0; attempt < 2; attempt++ {
		_, err := supervisor.Initialize(context.Background(), runtimeapi.Init{
			RunID: "test", Password: strings.Repeat("p", 32), Config: json.RawMessage(dummyConfig),
		})
		if err == nil || err.Error() != "OpenCode launch failed" {
			t.Fatalf("expected launch failure, got %v", err)
		}
		entries, err := os.ReadDir(temp)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) != 0 {
			t.Fatalf("failed initialization retained runtime home: %v", entries)
		}
	}
}
