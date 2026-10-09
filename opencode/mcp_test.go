package opencode

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
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
		{"connection rejection", `{}`, 403, false},
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
			err = client.ReadyMCP(context.Background(), "/workspace", []string{"broker"})
			if (err == nil) != test.valid {
				t.Fatalf("readiness: %v", err)
			}
			if calls < 1 || calls > 2 {
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
