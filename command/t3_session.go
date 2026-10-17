package command

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sync/errgroup"
)

// t3Session starts execution with the display while desktop tools finish initializing.
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
	displayReady := make(chan []string, 1)
	g.Go(func() error {
		return runDesktopWithEnvironment(lifetime, root, func(environment []string) { displayReady <- environment }, nil)
	})
	g.Go(func() error {
		select {
		case desktop := <-displayReady:
			return runT3SessionTools(lifetime, root, state, desktop)
		case <-lifetime.Done():
			return lifetime.Err()
		}
	})
	return g.Wait()
}

func runT3SessionTools(ctx context.Context, root, state string, desktop []string) error {
	started := time.Now()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	g, lifetime := errgroup.WithContext(ctx)
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	// This stable process owns all IDE children, including replacements. The worker
	// can keep tracking its descendants without mistaking an IDE restart for activity.
	ide := exec.Command(executable, "t3-ide")
	ide.Dir = root
	ide.Stdout, ide.Stderr = os.Stdout, os.Stderr
	ide.Env = t3IDEEnvironment(os.Environ(), desktop)
	for _, value := range []string{"T3_WORKSPACE_ROOT=" + root, "T3_WORKER_STATE_DIR=" + state, "T3_IDE_BINARY=" + env("T3_IDE_BINARY", "code-server")} {
		ide.Env = replaceEnvironment(ide.Env, value)
	}
	ideStarted := make(chan int, 1)
	g.Go(func() error {
		return runChildStarted(lifetime, ide, func(pid int) {
			slog.Info("sandbox startup", "stage", "ide-started", "elapsed_ms", time.Since(started).Milliseconds())
			ideStarted <- pid
		})
	})
	g.Go(func() error {
		var idePID int
		select {
		case idePID = <-ideStarted:
		case <-lifetime.Done():
			return lifetime.Err()
		}
		worker := exec.Command(env("T3_WORKER_NODE", "node"), env("T3_WORKER_ENTRYPOINT", "/opt/t3/dist/execution-worker.mjs"))
		worker.Dir = root
		worker.Env = replaceEnvironment(t3WorkerEnvironment(os.Environ(), desktop), "T3_IDE_PID="+strconv.Itoa(idePID))
		return runChildStarted(lifetime, worker, func(int) {
			slog.Info("sandbox startup", "stage", "worker-started", "elapsed_ms", time.Since(started).Milliseconds())
		})
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
		case "HOME", "USER", "LOGNAME", "T3_GIT_CREDENTIALS_FILE", "AZURE_EXTENSION_DIR", "AZURE_EXTENSION_USE_DYNAMIC_INSTALL", "AZURE_CORE_COLLECT_TELEMETRY":
			environment = replaceEnvironment(environment, item)
		}
	}
	// Runtime display sockets are temporary; terminal tool configuration is not.
	home := "/workspace/home"
	for _, item := range inherited {
		if value, ok := strings.CutPrefix(item, "HOME="); ok {
			home = value
		}
	}
	for _, item := range []string{
		"XDG_CONFIG_HOME=" + filepath.Join(home, ".config"),
		"XDG_CACHE_HOME=" + filepath.Join(home, ".cache"),
		"XDG_DATA_HOME=" + filepath.Join(home, ".local", "share"),
	} {
		environment = replaceEnvironment(environment, item)
	}
	for _, item := range inherited {
		key, _, _ := strings.Cut(item, "=")
		switch key {
		case "XDG_CONFIG_HOME", "XDG_CACHE_HOME", "XDG_DATA_HOME":
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
