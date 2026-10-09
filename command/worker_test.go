package command

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jake-molnia/agent-runtime/sandbox"
	"github.com/jake-molnia/agent-runtime/tailnet"
	"k8s.io/client-go/rest"
)

type reaperTransport func(*http.Request) (*http.Response, error)

func (f reaperTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func reaperResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestReapTailnetRequiresCompleteClaimInventory(t *testing.T) {
	claim := "ar-" + strings.Repeat("a", 32)
	secondClaim := "ar-" + strings.Repeat("b", 32)
	for _, tt := range []struct {
		name          string
		namespaces    []string
		failNamespace string
		continued     bool
		wantErr       bool
	}{
		{name: "complete", namespaces: []string{"one", "two"}},
		{name: "second namespace unavailable", namespaces: []string{"one", "two"}, failNamespace: "two", wantErr: true},
		{name: "continued first list", namespaces: []string{"one", "two"}, continued: true, wantErr: true},
		{name: "empty inventory scope", wantErr: true},
		{name: "blank namespace", namespaces: []string{"one", ""}, wantErr: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			listed := map[string]bool{}
			control, err := sandbox.NewControl(&rest.Config{Host: "https://kubernetes.invalid", Transport: reaperTransport(func(r *http.Request) (*http.Response, error) {
				parts := strings.Split(r.URL.Path, "/")
				ns := parts[len(parts)-2]
				listed[ns] = true
				if r.URL.Query().Get("labelSelector") != "app.kubernetes.io/managed-by=agent-runtime" {
					t.Fatalf("missing ownership selector: %s", r.URL)
				}
				if ns == tt.failNamespace {
					return reaperResponse(403, `{"kind":"Status","apiVersion":"v1","reason":"Forbidden","code":403}`), nil
				}
				namespaceClaim := claim
				if ns == "two" {
					namespaceClaim = secondClaim
				}
				metadata := `{}`
				if tt.continued {
					metadata = `{"continue":"next-page"}`
				}
				return reaperResponse(200, fmt.Sprintf(`{"apiVersion":"extensions.agents.x-k8s.io/v1beta1","kind":"SandboxClaimList","metadata":%s,"items":[{"apiVersion":"extensions.agents.x-k8s.io/v1beta1","kind":"SandboxClaim","metadata":{"name":%q,"namespace":%q}}]}`, metadata, namespaceClaim, ns)), nil
			})})
			if err != nil {
				t.Fatal(err)
			}
			scope, err := tailnet.NewScope("deployment", []string{"one", "two"})
			if err != nil {
				t.Fatal(err)
			}
			var tailnetCalls int
			client := &tailnet.Client{Scope: scope, ClientID: "test", ClientSecret: func(context.Context) (string, error) { return "test", nil }}
			client.HTTP = &http.Client{Transport: reaperTransport(func(r *http.Request) (*http.Response, error) {
				tailnetCalls++
				if tt.wantErr {
					t.Fatalf("tailnet request with incomplete inventory: %s", r.URL)
				}
				if !listed["one"] || !listed["two"] {
					t.Fatal("reaping before all namespaces listed")
				}
				switch r.URL.Path {
				case "/api/v2/oauth/token":
					return reaperResponse(200, `{"access_token":"test","expires_in":3600}`), nil
				case "/api/v2/tailnet/-/devices":
					return reaperResponse(200, fmt.Sprintf(`{"devices":[{"id":"active-one","hostname":%q,"created":"2020-01-01T00:00:00Z","tags":["tag:agent-sandbox"]},{"id":"active-two","hostname":%q,"created":"2020-01-01T00:00:00Z","tags":["tag:agent-sandbox"]}]}`, client.Hostname(claim), client.Hostname(secondClaim))), nil
				default:
					t.Fatalf("unexpected device deletion or request: %s", r.URL)
					return nil, nil
				}
			})}
			err = reapTailnet(context.Background(), control, client, tt.namespaces)
			if (err != nil) != tt.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tt.wantErr)
			}
			if !tt.wantErr && tailnetCalls != 2 {
				t.Fatalf("tailnet calls = %d, want 2", tailnetCalls)
			}
		})
	}
}

func TestWorkerRequiresDeploymentOwnership(t *testing.T) {
	for _, tagged := range []bool{false, true} {
		t.Run(fmt.Sprintf("tagged-%t", tagged), func(t *testing.T) {
			dir := t.TempDir()
			profile := "version: 1\nprofiles:\n  default:\n    pool: pool\n    namespace: agents\n    directory: /workspace\n"
			clientID := "oauth-client"
			if tagged {
				profile += "    tags: [tag:agent-sandbox]\n"
				clientID = ""
			}
			agentDir := filepath.Join(dir, "agents", "agent")
			if err := os.MkdirAll(agentDir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(agentDir, "agent.yaml"), []byte("version: 1\ndescription: Test\nmodel: {provider: openai, id: gpt-5}\nexecution: {profile: default, timeout_seconds: 60}\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(agentDir, "instructions.md"), []byte("Test instructions"), 0600); err != nil {
				t.Fatal(err)
			}
			workflowDir := filepath.Join(dir, "workflows")
			if err := os.MkdirAll(workflowDir, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(agentDir, "output.schema.json"), []byte(`{}`), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(agentDir, "agent.yaml"), []byte("version: 1\ndescription: Test\nmodel: {provider: openai, id: gpt-5}\nexecution: {profile: default, timeout_seconds: 60}\noutput_schema: output.schema.json\n"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(workflowDir, "example.yaml"), []byte("steps:\n  first:\n    agent: agent\n    input: input\noutput: first\n"), 0600); err != nil {
				t.Fatal(err)
			}
			keyPath := filepath.Join(dir, "runtime-key")
			if err := os.WriteFile(filepath.Join(dir, "deployment.yaml"), []byte(profile), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(keyPath, []byte(strings.Repeat("x", 32)), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("AGENT_DEFINITIONS_FILE", "")
			t.Setenv("AGENT_DEFINITIONS_DIR", dir)
			t.Setenv("AGENT_SNAPSHOT_DIR", t.TempDir())
			t.Setenv("AGENT_SECRET_KEY_FILE", keyPath)
			t.Setenv("TAILSCALE_CLIENT_ID", clientID)
			t.Setenv("AGENT_DEPLOYMENT_ID", "")
			err := worker(context.Background())
			if err == nil || !strings.Contains(err.Error(), "AGENT_DEPLOYMENT_ID") {
				t.Fatalf("expected missing deployment ID before external setup, got %v", err)
			}
		})
	}
}
