package opencode

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

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
			if calls < 1 || calls > 6 {
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
	client, err := New("http://runtime.invalid", nil, mcpTransport(func(r *http.Request) (*http.Response, error) {
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
	if !errors.As(err, &diagnostic) || diagnostic.httpStatus != 403 || diagnostic.state != "failed" || calls != 1 {
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
