package command

import (
	"context"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
	"time"

	"github.com/jake-molnia/agent-runtime/opencode"
)

// Launch the CLI with the image's config asset, without the runtime compiler.
func TestOpenCodeNativeImagePolicy(t *testing.T) {
	binary := os.Getenv("AGENT_RUNTIME_TEST_OPENCODE_BINARY")
	if binary == "" {
		t.Skip("set AGENT_RUNTIME_TEST_OPENCODE_BINARY to pinned OpenCode 2.0.26")
	}
	config, err := filepath.Abs("../harnesses/opencode.json")
	if err != nil {
		t.Fatal(err)
	}
	workspace := t.TempDir()
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(workspace, "opencode.json"), []byte(`{"permissions":[{"action":"*","resource":"*","effect":"deny"}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.Command(binary, "serve", "--hostname", "127.0.0.1", "--port", strconv.Itoa(port))
	cmd.Dir = workspace
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "XDG_CONFIG_HOME=" + home + "/config", "XDG_DATA_HOME=" + home + "/data", "XDG_STATE_HOME=" + home + "/state", "XDG_CACHE_HOME=" + home + "/cache", "OPENCODE_CONFIG=" + config, "OPENCODE_DISABLE_PROJECT_CONFIG=1", "OPENCODE_DISABLE_MODELS_FETCH=1", "OPENCODE_SERVER_PASSWORD=image-policy-test"}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); _ = cmd.Wait() })
	req, _ := http.NewRequest("GET", "http://localhost", nil)
	req.SetBasicAuth("opencode", "image-policy-test")
	client, err := opencode.New("http://127.0.0.1:"+strconv.Itoa(port), req.Header, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = client.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	type agentInfo struct {
		ID          string                                      `json:"id"`
		Permissions []struct{ Action, Resource, Effect string } `json:"permissions"`
	}
	var agents []agentInfo
	for {
		result, err := opencode.Decode[struct {
			Data []agentInfo `json:"data"`
		}](client.Do(ctx, "GET", "/api/agent", opencode.Arguments{Query: url.Values{"directory": {workspace}}}))
		if err != nil {
			t.Fatal(err)
		}
		agents = result.Data
		loaded := false
		for _, agent := range agents {
			loaded = loaded || agent.ID == "authored"
		}
		if loaded {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal("native agent catalog did not initialize")
		case <-time.After(50 * time.Millisecond):
		}
	}

	found := map[string]bool{}
	for _, agent := range agents {
		found[agent.ID] = true
		for _, action := range []string{"shell", "read", "write", "execute", "subagent", "external_directory", "doom_loop"} {
			effect := "ask"
			for _, rule := range agent.Permissions {
				if (rule.Action == "*" || rule.Action == action) && rule.Resource == "*" {
					effect = rule.Effect
				}
			}
			if effect != "allow" {
				t.Errorf("direct CLI agent %s action %s is %s", agent.ID, action, effect)
			}
		}
	}
	if !found["build"] || !found["authored"] {
		t.Fatalf("missing native agents: %v", found)
	}
}
