package githubreview

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jake-molnia/agent-runtime/sandbox"
	"google.golang.org/grpc"
	process "sigs.k8s.io/agent-sandbox/packages/sandboxd/spec/process/v1"
)

type checkoutProcesses struct {
	process.ProcessServiceClient
	t              *testing.T
	path           string
	output         []byte
	credentialsLog string
}

func (p *checkoutProcesses) Execute(ctx context.Context, request *process.ExecuteRequest, _ ...grpc.CallOption) (*process.ExecuteResponse, error) {
	p.t.Helper()
	config := request.Config
	for _, arg := range config.Command {
		if strings.Contains(arg, "checkout-private-token") {
			p.t.Fatal("token entered argv")
		}
	}
	if len(config.EnvVars) != 1 || config.EnvVars["AGENT_RUNTIME_CHECKOUT_TOKEN"] != "checkout-private-token" {
		p.t.Fatalf("unexpected checkout environment: %v", len(config.EnvVars))
	}
	command := exec.CommandContext(ctx, config.Command[0], config.Command[1:]...)
	command.Dir = config.GetCwd()
	command.Env = []string{"PATH=" + p.path}
	for key, value := range config.EnvVars {
		command.Env = append(command.Env, key+"="+value)
	}
	output, err := command.CombinedOutput()
	p.output = output
	if err != nil {
		return &process.ExecuteResponse{ExitCode: 1}, nil
	}
	return &process.ExecuteResponse{}, nil
}
func checkoutGit(t *testing.T, directory string, args ...string) string {
	t.Helper()
	command := exec.Command("git", args...)
	command.Dir = directory
	command.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git fixture: %v: %s", err, output)
	}
	return strings.TrimSpace(string(output))
}
func checkoutFixture(t *testing.T) (Input, *sandbox.Runtime, *checkoutProcesses, string, *atomic.Int32) {
	t.Helper()
	input, _ := fixture()
	root := t.TempDir()
	repository := filepath.Join(root, "owner", "repo.git")
	if err := os.MkdirAll(repository, 0700); err != nil {
		t.Fatal(err)
	}
	checkoutGit(t, repository, "init", "--bare", "--initial-branch=main")
	source := t.TempDir()
	checkoutGit(t, source, "init", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(source, "base.txt"), []byte("base content"), 0600); err != nil {
		t.Fatal(err)
	}
	checkoutGit(t, source, "add", ".")
	checkoutGit(t, source, "commit", "-m", "base")
	input.BaseSHA = checkoutGit(t, source, "rev-parse", "HEAD")
	if err := os.WriteFile(filepath.Join(source, "head.txt"), []byte("head content"), 0600); err != nil {
		t.Fatal(err)
	}
	checkoutGit(t, source, "add", ".")
	checkoutGit(t, source, "commit", "-m", "head")
	input.HeadSHA = checkoutGit(t, source, "rev-parse", "HEAD")
	checkoutGit(t, source, "push", repository, "HEAD:refs/heads/main")
	gitBinary, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	backend := &cgi.Handler{Path: filepath.Join(checkoutGit(t, source, "--exec-path"), "git-http-backend"), Env: []string{"GIT_PROJECT_ROOT=" + root, "GIT_HTTP_EXPORT_ALL=1"}}
	calls := new(atomic.Int32)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, password, ok := r.BasicAuth()
		if !ok || user != "x-access-token" || password != "checkout-private-token" {
			w.Header().Set("WWW-Authenticate", `Basic realm="fixture"`)
			w.WriteHeader(401)
			return
		}
		calls.Add(1)
		backend.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	shim := t.TempDir()
	// Only the test transport rewrites the fixed GitHub URL. The production hook has no URL parameter.
	credentialsLog := filepath.Join(t.TempDir(), "credential-paths")
	script := fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$AGENT_RUNTIME_CHECKOUT_TOKEN_FILE\" >> %q\nexec %q -c %q \"$@\"\n", credentialsLog, gitBinary, "url."+server.URL+"/.insteadOf=https://github.com/")
	if err := os.WriteFile(filepath.Join(shim, "git"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	processes := &checkoutProcesses{t: t, path: shim + ":" + os.Getenv("PATH"), credentialsLog: credentialsLog}
	return input, &sandbox.Runtime{Processes: processes}, processes, filepath.Join(t.TempDir(), "checkout"), calls
}
func TestPrepareRepositoryFetchesExactCommitsAndRetries(t *testing.T) {
	input, runtime, processes, directory, calls := checkoutFixture(t)
	hook, err := PrepareRepository(input, "checkout-private-token", directory)
	if err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := hook(t.Context(), runtime, map[string]string{"provider": "must-not-enter-checkout"}); err != nil {
			t.Fatalf("prepare: %v: %s", err, processes.output)
		}
		if got := checkoutGit(t, directory, "rev-parse", "HEAD"); got != input.HeadSHA {
			t.Fatalf("head=%s", got)
		}
		if got := checkoutGit(t, directory, "rev-parse", input.BaseSHA+"^{commit}"); got != input.BaseSHA {
			t.Fatalf("base=%s", got)
		}
		if got := checkoutGit(t, directory, "status", "--porcelain"); got != "" {
			t.Fatalf("dirty checkout: %s", got)
		}
		if got := checkoutGit(t, directory, "rev-parse", "--abbrev-ref", "HEAD"); got != "HEAD" {
			t.Fatalf("not detached: %s", got)
		}
	}
	assertCheckoutCredentialsRemoved(t, processes)
	if calls.Load() == 0 {
		t.Fatal("git never authenticated to HTTP fixture")
	}
	config, err := os.ReadFile(filepath.Join(directory, ".git", "config"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(config), "token") || strings.Contains(string(config), "http") || strings.Contains(string(config), "askpass") {
		t.Fatalf("credential configuration persisted: %s", config)
	}
	if contents, err := os.ReadFile(filepath.Join(directory, "head.txt")); err != nil || string(contents) != "head content" {
		t.Fatalf("head file: %s %v", contents, err)
	}
}
func TestPrepareRepositoryRejectsWrongDirectoryAndRevision(t *testing.T) {
	input, runtime, processes, directory, _ := checkoutFixture(t)
	for _, bad := range []string{"/", "relative", "/workspace/../other", "/workspace/", "/workspace\x00"} {
		if _, err := PrepareRepository(input, "checkout-private-token", bad); err == nil {
			t.Fatalf("accepted directory %q", bad)
		}
	}
	hook, err := PrepareRepository(input, "checkout-private-token", directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := hook(t.Context(), runtime, nil); err != nil {
		t.Fatalf("prepare: %v %s", err, processes.output)
	}
	changed := input
	changed.HeadSHA = strings.Repeat("f", 40)
	hook, err = PrepareRepository(changed, "checkout-private-token", directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := hook(t.Context(), runtime, nil); err == nil || strings.Contains(err.Error(), "checkout-private-token") {
		t.Fatalf("expected sanitized identity failure: %v", err)
	}
	outside := t.TempDir()
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	hook, err = PrepareRepository(input, "checkout-private-token", link)
	if err != nil {
		t.Fatal(err)
	}
	if err := hook(t.Context(), runtime, nil); err == nil {
		t.Fatal("accepted symlink checkout")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatal("modified outside symlink target")
	}
}
func TestCheckoutTokenRejectsSupersededAndDisabledPullRequests(t *testing.T) {
	for _, scenario := range []string{"superseded", "closed", "draft"} {
		t.Run(scenario, func(t *testing.T) {
			input, _ := fixture()
			contentsTokens := 0
			client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/repos/owner/repo/installation":
					fmt.Fprint(w, `{"id":7}`)
				case "/repos/owner/repo":
					fmt.Fprint(w, `{"id":11,"full_name":"owner/repo"}`)
				case "/repos/owner/repo/pulls/9":
					head, state, draft := input.HeadSHA, "open", false
					if scenario == "superseded" {
						head = strings.Repeat("f", 40)
					}
					if scenario == "closed" {
						state = "closed"
					}
					if scenario == "draft" {
						draft = true
					}
					fmt.Fprintf(w, `{"number":9,"state":%q,"draft":%t,"base":{"sha":%q,"repo":{"id":11,"full_name":"owner/repo"}},"head":{"sha":%q}}`, state, draft, input.BaseSHA, head)
				case "/app/installations/7/access_tokens":
					var body struct {
						Permissions map[string]string `json:"permissions"`
					}
					json.NewDecoder(r.Body).Decode(&body)
					if body.Permissions["contents"] != "" {
						contentsTokens++
					}
					fmt.Fprint(w, `{"token":"read-token"}`)
				default:
					w.WriteHeader(404)
				}
			})
			if _, err := client.CheckoutToken(t.Context(), input); err == nil || contentsTokens != 0 {
				t.Fatalf("issued contents token: %d %v", contentsTokens, err)
			}
		})
	}
}

func assertCheckoutCredentialsRemoved(t *testing.T, processes *checkoutProcesses) {
	t.Helper()
	data, err := os.ReadFile(processes.credentialsLog)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range strings.Fields(string(data)) {
		if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
			t.Fatalf("checkout credential directory remains: %v", err)
		}
	}
}
func TestPrepareRepositoryFailedFetchCleansCredentials(t *testing.T) {
	input, runtime, processes, directory, _ := checkoutFixture(t)
	input.HeadSHA = strings.Repeat("f", 40)
	hook, err := PrepareRepository(input, "checkout-private-token", directory)
	if err != nil {
		t.Fatal(err)
	}
	if err := hook(t.Context(), runtime, nil); err == nil || err.Error() != "repository checkout failed" {
		t.Fatalf("fetch failure leaked details: %v", err)
	}
	assertCheckoutCredentialsRemoved(t, processes)
}
