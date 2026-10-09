package command

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"
)

func desktopHandler(ctx context.Context, ready <-chan struct{}, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ctx.Err() != nil {
			http.Error(w, "desktop unavailable", http.StatusServiceUnavailable)
			return
		}
		if r.URL.Path == "/health" {
			select {
			case <-ready:
			case <-ctx.Done():
				http.Error(w, "desktop unavailable", http.StatusServiceUnavailable)
				return
			default:
				http.Error(w, "desktop starting", http.StatusServiceUnavailable)
				return
			}
		}
		if r.Method == http.MethodPost && r.URL.Path == "/initialize" {
			select {
			case <-ready:
			case <-ctx.Done():
				http.Error(w, "desktop unavailable", http.StatusServiceUnavailable)
				return
			case <-r.Context().Done():
				return
			}
		}
		next.ServeHTTP(w, r)
	})
}

// runDesktop owns one display, browser profile, and tool suite for the sandbox's lifetime.
// Its home is separate from both the checkout and OpenCode's credential directory.
func runDesktop(ctx context.Context, root string, onReady func([]string)) (err error) {
	chromiumSandbox := env("SANDBOX_CHROMIUM_SANDBOX", "enabled")
	if chromiumSandbox != "enabled" && chromiumSandbox != "disabled" {
		return errors.New("SANDBOX_CHROMIUM_SANDBOX must be enabled or disabled")
	}
	state, err := os.MkdirTemp("", "agent-desktop-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(state)
	home := filepath.Join(state, "home")
	for _, dir := range []string{home, filepath.Join(state, "runtime")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
	}
	if err := os.CopyFS(home, os.DirFS(env("SANDBOX_DESKTOP_CONFIG", "/usr/local/share/agent-runtime/desktop"))); err != nil {
		return fmt.Errorf("desktop configuration: %w", err)
	}
	profile := filepath.Join(home, ".config", "chromium")
	if err := os.MkdirAll(filepath.Join(profile, "Default"), 0700); err != nil {
		return err
	}
	preferences, err := json.Marshal(map[string]any{"download": map[string]any{"default_directory": filepath.Join(root, "downloads"), "prompt_for_download": false}})
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(profile, "Default", "Preferences"), preferences, 0600); err != nil {
		return err
	}
	viewer := env("SANDBOX_NOVNC_WEB", "/usr/share/novnc")
	env := desktopEnv(root, state)
	xauth := filepath.Join(state, "Xauthority")
	cookie := make([]byte, 16)
	if _, err := rand.Read(cookie); err != nil {
		return err
	}
	if err := os.WriteFile(xauth, nil, 0600); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, "xauth", "-f", xauth, "add", ":99", ".", hex.EncodeToString(cookie))
	cmd.Env = env
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("desktop X authority: %w", err)
	}

	lifetime, stop := context.WithCancel(ctx)
	g, lifetime := errgroup.WithContext(lifetime)
	defer func() {
		stop()
		if childErr := g.Wait(); childErr != nil && (err == nil || errors.Is(err, context.Canceled)) {
			err = childErr
		}
	}()
	start := func(binary string, args ...string) {
		g.Go(func() error {
			cmd := exec.Command(binary, args...)
			cmd.Dir, cmd.Env = root, env
			return runChild(lifetime, cmd)
		})
	}
	probeCommand := func(binary string, args ...string) func(context.Context) error {
		return func(ctx context.Context) error {
			cmd := exec.CommandContext(ctx, binary, args...)
			cmd.Env = env
			return cmd.Run()
		}
	}

	start("dbus-daemon", "--session", "--nofork", "--nopidfile", "--address=unix:path="+filepath.Join(state, "bus"))
	start("Xtigervnc", ":99", "-geometry", "1440x900", "-depth", "24", "-rfbport", "5900", "-localhost", "-SecurityTypes", "None", "-auth", xauth, "-nolisten", "tcp", "-AlwaysShared")
	if err := waitDesktop(lifetime, "display", probeCommand("xdpyinfo", "-display", ":99")); err != nil {
		return err
	}
	if err := waitDesktop(lifetime, "session bus", probeCommand("dbus-send", "--session", "--print-reply", "--dest=org.freedesktop.DBus", "/", "org.freedesktop.DBus.ListNames")); err != nil {
		return err
	}
	start("xfce4-session")
	if err := waitDesktop(lifetime, "window manager", probeCommand("wmctrl", "-m")); err != nil {
		return err
	}
	browserArgs := []string{"--user-data-dir=" + profile, "--remote-debugging-address=127.0.0.1", "--remote-debugging-port=9222", "--no-first-run", "--no-default-browser-check", "--disable-dev-shm-usage", "--force-renderer-accessibility", "--password-store=basic", "--window-size=1280,800"}
	if chromiumSandbox == "disabled" {
		browserArgs = append(browserArgs, "--no-sandbox")
	}
	start("chromium", append(browserArgs, "about:blank")...)
	start("computer-use")
	start("markitdown-mcp", "--http", "--host", "127.0.0.1", "--port", "8932")
	start("aiod", "start", "--host", "127.0.0.1", "--port", "18091", "--runtime-dir", filepath.Join(state, "aio"))
	start("websockify", "--web="+viewer, "127.0.0.1:6080", "127.0.0.1:5900")
	if err := waitDesktop(lifetime, "Chromium", func(ctx context.Context) error {
		var version struct {
			WebSocket string `json:"webSocketDebuggerUrl"`
		}
		if err := desktopJSON(ctx, "http://127.0.0.1:9222/json/version", &version); err != nil {
			return err
		}
		if version.WebSocket == "" {
			return errors.New("CDP websocket unavailable")
		}
		return nil
	}); err != nil {
		return err
	}
	start("playwright-mcp", "--host", "127.0.0.1", "--allowed-hosts", "127.0.0.1:8931", "--port", "8931", "--cdp-endpoint", "http://127.0.0.1:9222", "--shared-browser-context", "--caps", "vision,pdf,devtools", "--no-webmcp", "--image-responses", "allow", "--output-dir", filepath.Join(state, "browser-output"))
	if err := waitDesktop(lifetime, "computer", func(ctx context.Context) error {
		var info struct {
			Data struct {
				Available bool `json:"available"`
			} `json:"data"`
		}
		if err := desktopJSON(ctx, "http://127.0.0.1:18091/v2/computer/info", &info); err != nil {
			return err
		}
		if !info.Data.Available {
			return errors.New("computer worker has no display")
		}
		return nil
	}); err != nil {
		return err
	}
	for _, service := range []struct {
		name, url string
		tools     []string
	}{
		{"AIO MCP", "http://127.0.0.1:18091/mcp", []string{"sandbox_execute_bash", "sandbox_execute_code", "sandbox_file_operations", "browser_gui_screenshot", "browser_gui_execute_action", "documents_convert_to_markdown"}},
		{"browser MCP", "http://127.0.0.1:8931/mcp", []string{"browser_navigate", "browser_snapshot", "browser_click", "browser_take_screenshot"}},
	} {
		if err := waitDesktop(lifetime, service.name, func(ctx context.Context) error {
			return desktopMCPReady(ctx, service.url, service.tools)
		}); err != nil {
			return err
		}
	}
	if err := waitDesktop(lifetime, "desktop viewer", func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:6080/vnc.html", nil)
		if err != nil {
			return err
		}
		res, err := desktopHTTP.Do(req)
		if err != nil {
			return err
		}
		defer res.Body.Close()
		if res.StatusCode != http.StatusOK {
			return fmt.Errorf("viewer HTTP status %d", res.StatusCode)
		}
		return nil
	}); err != nil {
		return err
	}
	onReady(env)
	<-lifetime.Done()
	return lifetime.Err()
}

func desktopEnv(root, state string) []string {
	home := filepath.Join(state, "home")
	return []string{
		"PATH=" + os.Getenv("PATH"), "HOME=" + home, "USER=node", "LOGNAME=node", "LANG=C.UTF-8",
		"DISPLAY=:99", "XAUTHORITY=" + filepath.Join(state, "Xauthority"),
		"XDG_RUNTIME_DIR=" + filepath.Join(state, "runtime"), "XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"XDG_CACHE_HOME=" + filepath.Join(home, ".cache"), "XDG_DATA_HOME=" + filepath.Join(home, ".local", "share"),
		"XDG_CURRENT_DESKTOP=XFCE", "DBUS_SESSION_BUS_ADDRESS=unix:path=" + filepath.Join(state, "bus"),
		"NO_AT_BRIDGE=0", "SANDBOX_ROOT=" + root, "GIT_TERMINAL_PROMPT=0",
		"AIO_COMPUTER_USE_LISTEN=127.0.0.1:18100", "AIO_COMPUTER_USE_URL=http://127.0.0.1:18100",
		"BROWSER_REMOTE_DEBUGGING_HOST=127.0.0.1", "BROWSER_REMOTE_DEBUGGING_PORT=9222",
		"PLAYWRIGHT_BROWSERS_PATH=" + env("PLAYWRIGHT_BROWSERS_PATH", "/opt/playwright-browsers"),
		"AIO_SKILLS_PATH=/usr/local/share/agent-runtime/skills",
		`EXTRA_MCP_SERVERS={"documents":{"url":"http://127.0.0.1:8932/mcp","prefix":"documents"}}`,
	}
}

func waitDesktop(ctx context.Context, name string, probe func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	for {
		attempt, stop := context.WithTimeout(ctx, 3*time.Second)
		err := probe(attempt)
		stop()
		if err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("desktop %s not ready (%v): %w", name, err, ctx.Err())
		case <-time.After(200 * time.Millisecond):
		}
	}
}

var desktopHTTP = &http.Client{Timeout: 3 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

func desktopJSON(ctx context.Context, url string, result any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	res, err := desktopHTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("desktop HTTP status %d", res.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(result)
}

func desktopMCPReady(ctx context.Context, url string, required []string) error {
	session := ""
	protocol := "2024-11-05"
	defer func() {
		if session == "" {
			return
		}
		closeCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(closeCtx, http.MethodDelete, url, nil)
		if err != nil {
			return
		}
		req.Header.Set("Mcp-Session-Id", session)
		req.Header.Set("MCP-Protocol-Version", protocol)
		if res, err := desktopHTTP.Do(req); err == nil {
			res.Body.Close()
		}
	}()
	call := func(id int, method string, params any) (json.RawMessage, error) {
		message := map[string]any{"jsonrpc": "2.0", "method": method, "params": params}
		if id != 0 {
			message["id"] = id
		}
		data, err := json.Marshal(message)
		if err != nil {
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(data))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		req.Header.Set("MCP-Protocol-Version", protocol)
		if session != "" {
			req.Header.Set("Mcp-Session-Id", session)
		}
		res, err := desktopHTTP.Do(req)
		if err != nil {
			return nil, err
		}
		defer res.Body.Close()
		if id == 0 && res.StatusCode >= 200 && res.StatusCode < 300 {
			return nil, nil
		}
		if res.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("MCP HTTP status %d", res.StatusCode)
		}
		if value := res.Header.Get("Mcp-Session-Id"); value != "" {
			session = value
		}
		var rpc struct {
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
		}
		body := io.LimitReader(res.Body, 2<<20)
		if strings.HasPrefix(res.Header.Get("Content-Type"), "text/event-stream") {
			scanner := bufio.NewScanner(body)
			scanner.Buffer(make([]byte, 4096), 2<<20)
			for scanner.Scan() {
				if data, ok := strings.CutPrefix(scanner.Text(), "data: "); ok {
					err = json.Unmarshal([]byte(data), &rpc)
					break
				}
			}
			if scanner.Err() != nil {
				return nil, scanner.Err()
			}
		} else {
			err = json.NewDecoder(body).Decode(&rpc)
		}
		if err != nil {
			return nil, err
		}
		if len(rpc.Error) != 0 || len(rpc.Result) == 0 {
			return nil, errors.New("MCP request failed")
		}
		return rpc.Result, nil
	}
	init, err := call(1, "initialize", map[string]any{"protocolVersion": protocol, "capabilities": map[string]any{}, "clientInfo": map[string]string{"name": "agent-runtime", "version": "1"}})
	if err != nil {
		return err
	}
	var negotiated struct {
		Protocol string `json:"protocolVersion"`
	}
	if err := json.Unmarshal(init, &negotiated); err != nil || negotiated.Protocol == "" {
		return errors.New("MCP protocol negotiation failed")
	}
	protocol = negotiated.Protocol
	if _, err := call(0, "notifications/initialized", map[string]any{}); err != nil {
		return err
	}
	data, err := call(2, "tools/list", map[string]any{})
	if err != nil {
		return err
	}
	var listing struct {
		Tools []struct{ Name string } `json:"tools"`
	}
	if err := json.Unmarshal(data, &listing); err != nil {
		return err
	}
	for _, name := range required {
		found := false
		for _, tool := range listing.Tools {
			found = found || tool.Name == name
		}
		if !found {
			return fmt.Errorf("MCP tool unavailable: %s", name)
		}
	}
	return nil
}
