package opencode

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestReadyMCPReconnectsRecoverableInitialFailuresOnce(t *testing.T) {
	for _, message := range []string{"connection reset ECONNRESET", "HTTP 408", "HTTP 429", "HTTP 500", "HTTP 502", "HTTP 503", "HTTP 504"} {
		t.Run(message, func(t *testing.T) {
			connects := 0
			client, err := New("http://runtime.invalid", nil, mcpTransport(func(r *http.Request) (*http.Response, error) {
				body := `{}`
				switch r.URL.Path {
				case "/api/mcp":
					state := map[string]string{"status": "failed", "error": message}
					if connects == 1 {
						state = map[string]string{"status": "connected"}
					}
					encoded, _ := json.Marshal(map[string]any{"data": []any{map[string]any{"name": "broker", "status": state}}})
					body = string(encoded)
				case "/api/experimental/mcp/broker/connect":
					connects++
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			if err := client.ReadyMCP(t.Context(), "/workspace", []string{"broker"}); err != nil {
				t.Fatal(err)
			}
			if connects != 1 {
				t.Fatalf("expected one reconnect, got %d", connects)
			}
		})
	}
}

func TestReadyMCPDoesNotRepeatFailedReconnect(t *testing.T) {
	connects := 0
	client, err := New("http://runtime.invalid", nil, mcpTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodPost {
			connects++
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":[{"name":"broker","status":{"status":"failed","error":"HTTP 503 private-provider-token"}}]}`))}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	err = client.ReadyMCP(t.Context(), "/workspace", []string{"broker"})
	var diagnostic *MCPReadinessError
	if !errors.As(err, &diagnostic) || diagnostic.stage != "status" || diagnostic.httpStatus != 503 || connects != 1 {
		t.Fatalf("reconnect was not finite: %v connects=%d", err, connects)
	}
	if strings.Contains(err.Error(), "private") {
		t.Fatal("provider error payload leaked")
	}
}

func TestReadyMCPCancelsRecoverableReconnect(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	started := make(chan struct{})
	client, err := New("http://runtime.invalid", nil, mcpTransport(func(r *http.Request) (*http.Response, error) {
		if r.Method == http.MethodPost {
			close(started)
			<-r.Context().Done()
			return nil, r.Context().Err()
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":[{"name":"broker","status":{"status":"failed","error":"HTTP 503"}}]}`))}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- client.ReadyMCP(ctx, "/workspace", []string{"broker"}) }()
	select {
	case <-started:
	case err := <-done:
		t.Fatalf("reconnect never started: %v", err)
	case <-time.After(time.Second):
		t.Fatal("reconnect did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("reconnect ignored cancellation: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("reconnect did not stop")
	}
}

func TestReadyMCPRejectsPermanentInitialFailuresBeforeConnect(t *testing.T) {
	for _, test := range []struct{ state, message string }{
		{"needs_auth", ""}, {"disabled", ""},
		{"failed", "HTTP 400"}, {"failed", "HTTP 401"}, {"failed", "HTTP 403"}, {"failed", "HTTP 404"}, {"failed", "HTTP 422"}, {"failed", "HTTP 501"},
	} {
		t.Run(test.state+test.message, func(t *testing.T) {
			client, err := New("http://runtime.invalid", nil, mcpTransport(func(r *http.Request) (*http.Response, error) {
				if r.Method == http.MethodPost {
					t.Fatal("reconnected a permanent failure")
				}
				body, _ := json.Marshal(map[string]any{"data": []any{map[string]any{"name": "broker", "status": map[string]string{"status": test.state, "error": test.message}}}})
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			if err := client.ReadyMCP(t.Context(), "/workspace", []string{"broker"}); err == nil {
				t.Fatal("accepted permanent failure")
			}
		})
	}
}

type mcpTransport func(*http.Request) (*http.Response, error)

func (transport mcpTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return transport(request)
}

func TestReadyMCP(t *testing.T) {
	for _, test := range []struct {
		name, body    string
		connectStatus int
		valid         bool
	}{
		{"connected", `{"data":[{"name":"broker","status":{"status":"connected"}}]}`, 204, true},
		{"failed", `{"data":[{"name":"broker","status":{"status":"failed"}}]}`, 204, false},
		{"auth", `{"data":[{"name":"broker","status":{"status":"needs_auth"}}]}`, 204, false},
		{"missing", `{"data":[]}`, 204, false},
		{"invalid", "not-json", 204, false},
		{"connection rejection", `{"data":[{"name":"broker","status":{"status":"pending"}}]}`, 403, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			client, err := New("http://runtime.invalid", nil, mcpTransport(func(request *http.Request) (*http.Response, error) {
				calls++
				if request.URL.Query().Get("location[directory]") != "/workspace" {
					t.Fatal("lost scope")
				}
				status, body := 200, test.body
				if request.Method == http.MethodPost {
					if request.URL.Path != "/api/experimental/mcp/broker/connect" {
						t.Fatal("wrong broker selected")
					}
					status, body = test.connectStatus, ""
				}
				return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body))}, nil
			}))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
			defer cancel()
			err = client.ReadyMCP(ctx, "/workspace", []string{"broker"})
			if (err == nil) != test.valid {
				t.Fatalf("readiness: %v", err)
			}
			if calls < 2 || calls > 7 {
				t.Fatal("unexpected network calls")
			}
		})
	}
}

func TestReadyMCPNoServersAndCancelled(t *testing.T) {
	client, err := New("http://runtime.invalid", nil, mcpTransport(func(*http.Request) (*http.Response, error) { return nil, errors.New("must not call") }))
	if err != nil {
		t.Fatal(err)
	}
	if err := client.ReadyMCP(context.Background(), "/workspace", nil); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := client.ReadyMCP(ctx, "/workspace", []string{"broker"}); err == nil {
		t.Fatal("accepted cancelled readiness")
	}
}

func TestReadyMCPWaitsForColdLocationDiscovery(t *testing.T) {
	reads, connects := 0, 0
	activated := false
	client, err := New("http://runtime.invalid", nil, mcpTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/api/integration" {
			activated = true
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{}`))}, nil
		}
		if !activated {
			t.Fatal("inventory raced plugin activation")
		}
		body := `{"data":[]}`
		code := 200
		if r.Method == http.MethodPost {
			connects++
			if reads < 2 {
				t.Fatal("connect raced MCP registration")
			}
			code = 204
			body = ""
		} else {
			reads++
			if reads == 2 {
				body = `{"data":[{"name":"broker","status":{"status":"pending"}}]}`
			} else if reads > 2 {
				body = `{"data":[{"name":"broker","status":{"status":"connected"}}]}`
			}
		}
		return &http.Response{StatusCode: code, Body: io.NopCloser(strings.NewReader(body))}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err = client.ReadyMCP(t.Context(), "/workspace", []string{"broker"}); err != nil {
		t.Fatal(err)
	}
	if connects != 1 || reads != 3 {
		t.Fatalf("unexpected readiness sequence reads=%d connects=%d", reads, connects)
	}
}

func TestReadyMCPReportsFinitePolicyFailureWithoutRetryOrSecretText(t *testing.T) {
	calls := 0
	client, err := New("http://runtime.invalid", nil, mcpTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":[{"name":"broker","status":{"status":"failed","error":"HTTP 403 Bearer private-provider-token https://private.invalid"}}]}`))}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	err = client.ReadyMCP(t.Context(), "/workspace", []string{"broker"})
	var diagnostic *MCPReadinessError
	if !errors.As(err, &diagnostic) || diagnostic.httpStatus != 403 || diagnostic.state != "failed" || calls != 2 {
		t.Fatalf("policy failure not preserved safely: %v calls=%d", err, calls)
	}
	if strings.Contains(err.Error(), "private") {
		t.Fatal("provider error payload leaked")
	}
}

func TestReadyMCPCancellationStopsMissingInventory(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	calls := 0
	client, err := New("http://runtime.invalid", nil, mcpTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		cancel()
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"data":[]}`))}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err = client.ReadyMCP(ctx, "/workspace", []string{"broker"}); !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("cancellation lost %v calls=%d", err, calls)
	}
}
