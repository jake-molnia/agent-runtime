package command

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/sync/errgroup"
)

// t3Session starts the execution worker only after its desktop and IDE are ready.
// The worker owns authenticated routing; these services remain on pod loopback.
func t3Session(ctx context.Context) error {
	root := env("T3_WORKSPACE_ROOT", "/workspace")
	state := env("T3_WORKER_STATE_DIR", filepath.Join(root, ".t3-worker"))
	if !filepath.IsAbs(root) || !filepath.IsAbs(state) {
		return errors.New("T3 workspace and state directories must be absolute")
	}
	for _, dir := range []string{root, state, filepath.Join(state, "ide", "user-data"), filepath.Join(state, "ide", "extensions")} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
	}
	lifetime, cancel := context.WithCancel(ctx)
	defer cancel()
	g, lifetime := errgroup.WithContext(lifetime)
	desktopReady := make(chan []string, 1)
	g.Go(func() error {
		return runDesktop(lifetime, root, func(environment []string) { desktopReady <- environment })
	})
	g.Go(func() error {
		select {
		case desktop := <-desktopReady:
			return runT3SessionTools(lifetime, root, state, desktop)
		case <-lifetime.Done():
			return lifetime.Err()
		}
	})
	return g.Wait()
}

func runT3SessionTools(ctx context.Context, root, state string, desktop []string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	g, lifetime := errgroup.WithContext(ctx)
	ide := exec.Command(env("T3_IDE_BINARY", "code-server"),
		"--bind-addr", "127.0.0.1:8085", "--auth", "none", "--disable-telemetry", "--disable-update-check", "--disable-proxy",
		"--config", filepath.Join(state, "ide", "config.yaml"),
		"--user-data-dir", filepath.Join(state, "ide", "user-data"), "--extensions-dir", filepath.Join(state, "ide", "extensions"), root)
	ide.Dir = root
	ide.Env = t3IDEEnvironment(os.Environ(), desktop)
	g.Go(func() error { return runChild(lifetime, ide) })
	g.Go(func() error {
		if err := waitDesktop(lifetime, "VS Code", func(ctx context.Context) error {
			var health struct {
				Status string `json:"status"`
			}
			if err := desktopJSON(ctx, "http://127.0.0.1:8085/healthz", &health); err != nil {
				return err
			}
			if health.Status != "alive" && health.Status != "expired" {
				return fmt.Errorf("unexpected IDE health status %q", health.Status)
			}
			return nil
		}); err != nil {
			return err
		}
		worker := exec.Command(env("T3_WORKER_NODE", "node"), env("T3_WORKER_ENTRYPOINT", "/opt/t3/dist/execution-worker.mjs"))
		worker.Dir = root
		worker.Env = t3WorkerEnvironment(os.Environ(), desktop)
		return runChild(lifetime, worker)
	})
	return g.Wait()
}

func t3WorkerEnvironment(inherited, desktop []string) []string {
	environment := append([]string(nil), inherited...)
	for _, item := range desktop {
		key, _, _ := strings.Cut(item, "=")
		switch key {
		case "DISPLAY", "XAUTHORITY", "DBUS_SESSION_BUS_ADDRESS", "XDG_RUNTIME_DIR", "XDG_CURRENT_DESKTOP":
			environment = replaceEnvironment(environment, item)
		}
	}
	for _, item := range []string{
		"T3CODE_SANDBOX_IDE_URL=http://127.0.0.1:8085",
		"T3CODE_SANDBOX_CDP_URL=http://127.0.0.1:9222",
		"T3CODE_SANDBOX_COMPUTER_URL=http://127.0.0.1:18091",
	} {
		environment = replaceEnvironment(environment, item)
	}
	return environment
}

func t3IDEEnvironment(inherited, desktop []string) []string {
	environment := append([]string(nil), desktop...)
	for _, item := range inherited {
		key, _, _ := strings.Cut(item, "=")
		switch key {
		case "HOME", "USER", "LOGNAME", "T3_GIT_CREDENTIALS_FILE":
			environment = replaceEnvironment(environment, item)
		}
	}
	return environment
}

func replaceEnvironment(environment []string, value string) []string {
	key, _, _ := strings.Cut(value, "=")
	result := make([]string, 0, len(environment)+1)
	for _, item := range environment {
		if !strings.HasPrefix(item, key+"=") {
			result = append(result, item)
		}
	}
	return append(result, value)
}
