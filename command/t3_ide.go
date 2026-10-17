package command

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// t3IDE keeps one process identity while restarting the editor beneath it.
func t3IDE(ctx context.Context) error {
	root := env("T3_WORKSPACE_ROOT", "/workspace")
	state := env("T3_WORKER_STATE_DIR", filepath.Join(root, ".t3-worker"))
	logPath := filepath.Join(state, "ide", "code-server.log")
	log := newIDELog(logPath, 1<<20)
	defer log.Close()
	for ctx.Err() == nil {
		attempt, cancel := context.WithCancel(ctx)
		cmd := exec.Command(env("T3_IDE_BINARY", "code-server"),
			"--bind-addr", "127.0.0.1:8085", "--auth", "none",
			"--disable-telemetry", "--disable-update-check", "--disable-proxy", "--disable-workspace-trust",
			"--config", filepath.Join(state, "ide", "config.yaml"),
			"--user-data-dir", filepath.Join(state, "ide", "user-data"),
			"--extensions-dir", filepath.Join(state, "ide", "extensions"), root)
		cmd.Dir = root
		// Extension hosts can retain the editor's output pipes. Two seconds to stop
		// plus one to drain leaves time inside the outer five-second shutdown budget.
		cmd.WaitDelay = time.Second
		// Raw diagnostics stay in bounded private files, outside the public service log.
		cmd.Stdout, cmd.Stderr = log, log
		var warming chan struct{}
		err := runChildUntilExitStarted(attempt, cmd, func(int) {
			warming = make(chan struct{})
			go func() {
				defer close(warming)
				if warmErr := warmT3IDE(attempt, "http://127.0.0.1:8085/"); warmErr != nil {
					if attempt.Err() == nil {
						fmt.Fprintln(log, warmErr)
					}
					cancel()
				}
			}()
		}, 2*time.Second)
		cancel()
		if warming != nil {
			<-warming
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			fmt.Fprintln(log, err)
		}
		// IDE availability does not own the worker lifetime, including on cold startup.
		// Failed readiness is visible through the IDE proxy and private diagnostics.
		slog.Warn("sandbox IDE restarting", "diagnostics", logPath)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return ctx.Err()
}

func warmT3IDE(ctx context.Context, origin string) error {
	// /healthz only checks code-server's listener. GET / loads the VS Code backend
	// before redirecting to the last workspace. Do not follow that redirect or
	// supply a folder: warming must not change the user's last-opened workspace.
	return waitDesktop(ctx, "VS Code backend", func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, origin, nil)
		if err != nil {
			return err
		}
		response, err := desktopHTTP.Do(req)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusFound {
			return fmt.Errorf("IDE backend HTTP status %d", response.StatusCode)
		}
		_, err = io.Copy(io.Discard, io.LimitReader(response.Body, 1<<20))
		return err
	})
}

// Keep the current and previous chunk so crash loops cannot fill workspace storage.
type ideLog struct {
	mu     sync.Mutex
	file   *os.File
	path   string
	size   int64
	limit  int64
	warned bool
}

func newIDELog(path string, limit int64) *ideLog {
	return &ideLog{path: path, limit: limit}
}

func (l *ideLog) open() error {
	if err := os.MkdirAll(filepath.Dir(l.path), 0700); err != nil {
		return err
	}
	file, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	if err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil {
		file.Close()
		return err
	}
	l.file, l.size = file, info.Size()
	return nil
}

func (l *ideLog) Write(data []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	// Keep draining the child's pipes even if storage is unavailable. A later
	// write retries opening/rotation; raw diagnostics never go to the public log.
	if err := l.write(data); err != nil {
		if l.file != nil {
			l.file.Close()
			l.file = nil
		}
		if !l.warned {
			slog.Warn("sandbox IDE diagnostics unavailable", "path", l.path)
		}
		l.warned = true
	} else {
		l.warned = false
	}
	return len(data), nil
}

func (l *ideLog) write(data []byte) error {
	if l.file == nil {
		if err := l.open(); err != nil {
			return err
		}
	}
	for len(data) > 0 {
		if l.size >= l.limit {
			if err := l.file.Close(); err != nil {
				l.file = nil
				return err
			}
			l.file = nil
			if err := os.Rename(l.path, l.path+".1"); err != nil {
				return err
			}
			if err := l.open(); err != nil {
				return err
			}
		}
		n, err := l.file.Write(data[:min(int64(len(data)), l.limit-l.size)])
		l.size += int64(n)
		if err != nil {
			return err
		}
		data = data[n:]
	}
	return nil
}

func (l *ideLog) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file == nil {
		return nil
	}
	err := l.file.Close()
	l.file = nil
	return err
}
