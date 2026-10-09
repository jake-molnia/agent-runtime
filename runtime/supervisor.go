// Package runtime supervises sandboxd, an unprivileged tailnet, and one OpenCode server.
package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/jake-molnia/agent-runtime/opencode"
	"golang.org/x/net/proxy"
)

type Init struct {
	AllowProjectConfig bool            `json:"allow_project_config,omitempty"`
	RunID              string          `json:"run_id"`
	Password           string          `json:"password"`
	TailnetKey         string          `json:"tailnet_key,omitempty"`
	Hostname           string          `json:"hostname,omitempty"`
	Config             json.RawMessage `json:"config"`
}
type InitResult struct {
	TailnetSeconds float64 `json:"tailnet_seconds"`
	HarnessSeconds float64 `json:"harness_seconds"`
}
type Supervisor struct {
	exited          atomic.Bool
	Root            string
	OpenCodeBinary  string
	TailscaleSocket string
	mu              sync.Mutex
	digest          string
	result          InitResult
	lifetime        context.Context
}

func (s *Supervisor) Handler(ctx context.Context) http.Handler {
	s.lifetime = ctx
	mux := http.NewServeMux()
	mux.HandleFunc("POST /initialize", func(w http.ResponseWriter, r *http.Request) {
		defer r.Body.Close()
		var input Init
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&input); err != nil {
			http.Error(w, "invalid initialization", 400)
			return
		}
		result, err := s.Initialize(r.Context(), input)
		if err != nil {
			http.Error(w, "runtime initialization failed", 503)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(result)
	})
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		if s.exited.Load() {
			http.Error(w, "OpenCode unavailable", 503)
			return
		}
		w.WriteHeader(200)
	})
	return mux
}
func (s *Supervisor) Initialize(ctx context.Context, input Init) (InitResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if input.RunID == "" || len(input.Password) < 32 || !json.Valid(input.Config) {
		return InitResult{}, errors.New("invalid runtime initialization")
	}
	stable := input
	stable.TailnetKey = ""
	data, _ := json.Marshal(stable)
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	if s.digest != "" {
		if s.digest != digest {
			return InitResult{}, errors.New("runtime already assigned")
		}
		if s.exited.Load() {
			return InitResult{}, errors.New("OpenCode server exited")
		}
		return s.result, nil
	}
	root := s.Root
	if root == "" {
		root = "/workspace"
	}
	home, err := runtimeHome(root)
	if err != nil {
		return InitResult{}, err
	}
	defer func() {
		if s.digest == "" {
			_ = os.RemoveAll(home)
		}
	}()
	// Retain successful state until sandbox deletion so artifact collection can retry.
	config := filepath.Join(home, "opencode.json")
	if err := writeRuntimeConfig(home, input.Config); err != nil {
		return InitResult{}, err
	}
	var result InitResult
	if input.TailnetKey != "" {
		start := time.Now()
		keyFile, err := os.CreateTemp(home, "auth-")
		if err != nil {
			return result, err
		}
		keyPath := keyFile.Name()
		defer os.Remove(keyPath)
		if _, err = io.WriteString(keyFile, input.TailnetKey); err != nil {
			keyFile.Close()
			return result, err
		}
		if err = keyFile.Close(); err != nil {
			return result, err
		}
		enroll, cancel := context.WithTimeout(ctx, 65*time.Second)
		cmd := exec.CommandContext(enroll, "tailscale", "--socket="+s.TailscaleSocket, "up", "--auth-key=file:"+keyPath, "--hostname="+input.Hostname, "--accept-dns=false", "--accept-routes=false", "--shields-up", "--timeout=60s")
		err = cmd.Run()
		cancel()
		os.Remove(keyPath)
		if err != nil {
			return result, errors.New("Tailscale enrollment failed")
		}
		result.TailnetSeconds = time.Since(start).Seconds()
	}
	start := time.Now()
	binary := s.OpenCodeBinary
	if binary == "" {
		binary = "opencode"
	}
	cmd := exec.Command(binary, "serve", "--hostname", "0.0.0.0", "--port", "4096")
	cmd.Dir = root
	// Build a minimal environment instead of inheriting provisioner or runtime credentials.
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + home, "XDG_CONFIG_HOME=" + home + "/config", "XDG_DATA_HOME=" + home + "/data", "XDG_STATE_HOME=" + home + "/state", "XDG_CACHE_HOME=" + home + "/cache", "OPENCODE_CONFIG=" + config, "OPENCODE_SERVER_PASSWORD=" + input.Password, "OPENCODE_DISABLE_MODELS_FETCH=1", "GIT_TERMINAL_PROMPT=0"}
	if !input.AllowProjectConfig {
		cmd.Env = append(cmd.Env, "OPENCODE_DISABLE_PROJECT_CONFIG=1")
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return result, errors.New("OpenCode launch failed")
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	headers := http.Header{}
	req, _ := http.NewRequest("GET", "http://localhost", nil)
	req.SetBasicAuth("opencode", input.Password)
	headers.Set("Authorization", req.Header.Get("Authorization"))
	client, err := opencode.New("http://127.0.0.1:4096", headers, nil)
	if err != nil {
		return result, err
	}
	ready, cancel := context.WithTimeout(ctx, 60*time.Second)
	err = client.Ready(ready)
	cancel()
	if err != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-done
		return result, err
	}
	s.digest = digest
	lifetime := s.lifetime
	if lifetime == nil {
		lifetime = ctx
	}
	go func() {
		defer s.exited.Store(true)
		select {
		case <-lifetime.Done():
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
				<-done
			}
		case <-done:
		}
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}()
	result.HarnessSeconds = time.Since(start).Seconds()
	s.result = result
	return result, nil
}

func runtimeHome(root string) (string, error) {
	workspace, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	workspace, err = filepath.EvalSymlinks(workspace)
	if err != nil {
		return "", err
	}
	temp, err := filepath.Abs(os.TempDir())
	if err != nil {
		return "", err
	}
	temp, err = filepath.EvalSymlinks(temp)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(workspace, temp)
	if err != nil {
		return "", err
	}
	if rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("runtime temporary directory must be outside workspace")
	}
	return os.MkdirTemp(temp, "agent-home-")
}

// ApertureProxy uses a persistent userspace SOCKS transport, avoiding a tailscale nc process per connection.
func ApertureProxy(upstream string) (http.Handler, error) {
	target, err := url.Parse(upstream)
	if err != nil || target.Host == "" || (target.Scheme != "http" && target.Scheme != "https") {
		return nil, errors.New("invalid Aperture upstream")
	}
	dialer, err := proxy.SOCKS5("tcp", "127.0.0.1:1055", nil, &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second})
	if err != nil {
		return nil, err
	}
	contextDialer, ok := dialer.(proxy.ContextDialer)
	if !ok {
		return nil, errors.New("SOCKS dialer lacks context support")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = contextDialer.DialContext
	transport.MaxIdleConnsPerHost = 16
	p := httputil.NewSingleHostReverseProxy(target)
	p.Transport = transport
	p.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) { http.Error(w, "Aperture unavailable", 502) }
	return p, nil
}
