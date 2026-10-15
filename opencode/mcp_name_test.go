package opencode

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestReadyMCPAcceptsLongConfiguredServerNames(t *testing.T) {
	name := strings.Repeat("a", 129)
	connected := false
	client, err := New("http://runtime.invalid", nil, mcpTransport(func(r *http.Request) (*http.Response, error) {
		body := `{}`
		switch r.URL.Path {
		case "/api/mcp":
			status := "pending"
			if connected {
				status = "connected"
			}
			data, _ := json.Marshal(map[string]any{"data": []any{map[string]any{"name": name, "status": map[string]string{"status": status}}}})
			body = string(data)
		case "/api/experimental/mcp/" + name + "/connect":
			connected = true
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err = client.ReadyMCP(t.Context(), "/workspace", []string{name}); err != nil {
		t.Fatal(err)
	}
	if !connected {
		t.Fatal("accepted server was never connected")
	}
}

func TestMCPReadinessDiagnosticBoundsLongAcceptedName(t *testing.T) {
	name := strings.Repeat("a", 4096)
	err := mcpFailure(name, "inventory", "failed", 503, nil)
	if !strings.Contains(err.Error(), name[:128]+"...") || strings.Contains(err.Error(), name) || len(err.Error()) > 220 {
		t.Fatal("unbounded diagnostic", len(err.Error()))
	}
	if !strings.Contains(err.Error(), "stage=inventory state=failed http=503") {
		t.Fatal("lost diagnostic context", err)
	}
}
