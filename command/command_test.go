package command

import (
	"context"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

func TestServeValidatesUpstreamBeforeCreatingState(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SANDBOX_ROOT", filepath.Join(root, "workspace"))
	t.Setenv("TMPDIR", filepath.Join(root, "missing"))
	t.Setenv("APERTURE_UPSTREAM", "invalid")

	err := serve(context.Background())
	if err == nil || err.Error() != "invalid Aperture upstream" {
		t.Fatalf("expected upstream validation before temporary state creation, got %v", err)
	}
}

func TestHTTPServerWaitsForActiveRequestOnShutdown(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	entered, release := make(chan struct{}), make(chan struct{})
	defer close(release)
	finished := make(chan error, 1)
	go func() {
		finished <- httpServer(ctx, address, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(entered)
			<-release
			_, _ = io.WriteString(w, "finished")
		}))
	}()
	response := make(chan error, 1)
	go func() {
		client := &http.Client{Timeout: 3 * time.Second}
		deadline := time.Now().Add(3 * time.Second)
		for {
			res, err := client.Get("http://" + address)
			if err == nil {
				_, err = io.Copy(io.Discard, res.Body)
				res.Body.Close()
				response <- err
				return
			}
			if time.Now().After(deadline) {
				response <- err
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	select {
	case <-entered:
	case err := <-response:
		t.Fatalf("request failed: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("handler did not start")
	}
	cancel()
	select {
	case err := <-finished:
		t.Fatalf("server returned before active request finished: %v", err)
	case <-time.After(100 * time.Millisecond):
	}
	release <- struct{}{}
	if err := <-response; err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not finish shutdown")
	}
}

func TestHTTPServerReturnsBindFailure(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	go func() {
		finished <- httpServer(ctx, listener.Addr().String(), http.NotFoundHandler())
	}()
	select {
	case err := <-finished:
		if err == nil {
			t.Fatal("expected occupied port error")
		}
	case <-time.After(time.Second):
		t.Fatal("bind failure waited for context cancellation")
	}
}
