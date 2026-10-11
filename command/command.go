package command

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	runtimeapi "github.com/jake-molnia/agent-runtime/runtime"
	"golang.org/x/sync/errgroup"
)

// Run executes the runtime supervisor or Hatchet worker with the caller's shutdown context.
func Run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: agent-runtime serve|worker|t3-worker|t3-session|version|agents|workflows|run|submit|reviews")
	}
	if args[0] == "reviews" {
		return reviewsCommand(ctx, args[1:])
	}
	if args[0] == "agents" {
		return agentsCommand(args[1:])
	}
	if args[0] == "workflows" {
		return workflowsCommand(args[1:])
	}
	if args[0] == "run" {
		return runCommand(ctx, args[1:])
	}
	if args[0] == "submit" {
		return submitCommand(ctx, args[1:])
	}
	if len(args) != 1 {
		return errors.New("unexpected command arguments")
	}
	switch args[0] {
	case "serve":
		return serve(ctx)
	case "t3-session":
		return t3Session(ctx)
	case "worker":
		return worker(ctx)
	case "t3-worker":
		return t3Worker(ctx)
	case "version":
		fmt.Println("agent-runtime; upstream OpenCode V2; sandboxd v1.0.3")
		return nil
	default:
		return errors.New("unknown command")
	}
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
func serve(ctx context.Context) error {
	root := env("SANDBOX_ROOT", "/workspace")
	if err := os.MkdirAll(root, 0700); err != nil {
		return err
	}
	g, ctx := errgroup.WithContext(ctx)
	var socket string
	if upstream := os.Getenv("APERTURE_UPSTREAM"); upstream != "" {
		handler, err := runtimeapi.ApertureProxy(upstream)
		if err != nil {
			return err
		}
		state, err := os.MkdirTemp("", "agent-tailnet-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(state)
		socket = filepath.Join(state, "tailscaled.sock")
		g.Go(func() error {
			return child(ctx, "tailscaled", "--tun=userspace-networking", "--state=mem:", "--statedir="+state, "--socket="+socket, "--socks5-server=127.0.0.1:1055", "--no-logs-no-support")
		})
		g.Go(func() error { return httpServer(ctx, "127.0.0.1:8082", handler) })
	}
	g.Go(func() error { return child(ctx, "sandboxd", "--root-dir="+root) })
	desktopReady := make(chan struct{})
	g.Go(func() error { return runDesktop(ctx, root, func([]string) { close(desktopReady) }) })
	supervisor := &runtimeapi.Supervisor{Root: root, OpenCodeBinary: os.Getenv("OPENCODE_BINARY"), TailscaleSocket: socket}
	g.Go(func() error {
		return httpServer(ctx, ":8081", desktopHandler(ctx, desktopReady, supervisor.Handler(ctx)))
	})
	return g.Wait()
}
func httpServer(ctx context.Context, addr string, handler http.Handler) error {
	server := &http.Server{Addr: addr, Handler: handler, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 32 << 10}
	done := make(chan struct{})
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		select {
		case <-ctx.Done():
			stop, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if err := server.Shutdown(stop); err != nil {
				_ = server.Close()
			}
		case <-done:
		}
	}()
	err := server.ListenAndServe()
	close(done)
	<-stopped
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
func child(ctx context.Context, binary string, args ...string) error {
	cmd := exec.Command(binary, args...)
	return runChild(ctx, cmd)
}

func runChild(ctx context.Context, cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Raw subprocess diagnostics can include credentials. Health endpoints and exit status are the public contract.
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("cannot start %s: %w", cmd.Path, err)
	}
	defer syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-done
		}
		return ctx.Err()
	case <-done:
		return fmt.Errorf("%s exited", cmd.Path)
	}
}
