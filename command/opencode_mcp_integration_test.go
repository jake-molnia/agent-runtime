package command

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jake-molnia/agent-runtime/definitions"
	"github.com/jake-molnia/agent-runtime/opencode"
	"github.com/jake-molnia/agent-runtime/orchestration"
	runtimeapi "github.com/jake-molnia/agent-runtime/runtime"
	"github.com/jake-molnia/agent-runtime/sandbox"
)

func TestOpenCodeNativeMCPIntegration(t *testing.T) {
	binary := os.Getenv("AGENT_RUNTIME_TEST_OPENCODE_BINARY")
	if binary == "" {
		t.Skip("set AGENT_RUNTIME_TEST_OPENCODE_BINARY to pinned OpenCode V2 2.0.26")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []struct {
		name, attempted string
		brokerCatalog   bool
		builtinSkills   bool
	}{
		{"exact_allowed", "broker_publish_comment", false, false},
		{"legacy_list_does_not_restrict", "broker_dangerous_tool", false, false},
		{"catalog_new", "broker_new_tool", true, false},
		{"catalog_revoked", "broker_revoked_tool", true, false},
		{"catalog_other_server", "unused_dangerous_tool", true, false},
		{"catalog_shell", "shell", true, false},
		{"repeated_shell", "shell", true, false},
		{"catalog_read", "read", true, false},
		{"catalog_write", "write", true, false},
		{"builtin_skill", "skill", true, true},
		{"builtin_unselected_skill", "skill", true, true},
		{"builtin_reference", "read", true, true},
		{"builtin_workspace_read", "read", true, true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			attempted := scenario.attempted
			skillDirectory := os.Getenv("AGENT_RUNTIME_TEST_SKILLS_DIRECTORY")
			if scenario.builtinSkills && skillDirectory == "" {
				t.Skip("set AGENT_RUNTIME_TEST_SKILLS_DIRECTORY to installed pinned skill catalog")
			}
			if scenario.builtinSkills {
				t.Setenv("AGENT_RUNTIME_SKILL_BUNDLE_ROOT", filepath.Join(filepath.Dir(skillDirectory), "bundles"))
				t.Setenv("AGENT_RUNTIME_SKILL_CATALOG_ROOT", skillDirectory)
			}
			var workspace string
			listener, err := net.Listen("tcp", "127.0.0.1:4096")
			if err != nil {
				t.Fatal(err)
			}
			listener.Close()
			var mutex sync.Mutex
			methods, calls := map[string]int{}, map[string]int{}
			var advertised [][]string
			var observedProviderBodies []string
			unusedRequests := 0
			broker := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.Method != "POST" {
					writer.WriteHeader(405)
					return
				}
				var rpc struct {
					ID     json.RawMessage `json:"id"`
					Method string          `json:"method"`
					Params struct {
						Name string `json:"name"`
					} `json:"params"`
				}
				if err := json.NewDecoder(http.MaxBytesReader(writer, request.Body, 1<<20)).Decode(&rpc); err != nil {
					t.Errorf("MCP decode: %v", err)
					writer.WriteHeader(400)
					return
				}
				mutex.Lock()
				methods[rpc.Method]++
				if rpc.Method == "tools/call" {
					calls[rpc.Params.Name]++
				}
				mutex.Unlock()
				if len(rpc.ID) == 0 {
					writer.WriteHeader(202)
					return
				}
				var result any
				switch rpc.Method {
				case "initialize":
					result = map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]string{"name": "fake-broker", "version": "1"}}
				case "tools/list":
					tools := []any{}
					for _, name := range []string{"publish_comment", "dangerous_tool", "new_tool", "revoked_tool"} {
						tools = append(tools, map[string]any{"name": name, "description": name, "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}})
					}
					result = map[string]any{"tools": tools}
				case "tools/call":
					result = map[string]any{"content": []any{map[string]string{"type": "text", "text": "broker called"}}}
					if rpc.Params.Name == "revoked_tool" {
						result = map[string]any{"isError": true, "content": []any{map[string]string{"type": "text", "text": "broker policy revoked"}}}
					}
				default:
					t.Errorf("unexpected MCP method %s", rpc.Method)
					result = map[string]any{}
				}
				writer.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(writer).Encode(map[string]any{"jsonrpc": "2.0", "id": rpc.ID, "result": result})
			}))
			t.Cleanup(broker.Close)
			unused := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				mutex.Lock()
				unusedRequests++
				mutex.Unlock()
				writer.WriteHeader(500)
			}))
			t.Cleanup(unused.Close)
			const answer = `{"verified":true}`
			const providerKey = "fake-local-provider-key"
			provider := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				body, err := io.ReadAll(http.MaxBytesReader(writer, request.Body, 1<<20))
				var payload struct {
					Tools []struct {
						Function struct {
							Name string `json:"name"`
						} `json:"function"`
					} `json:"tools"`
				}
				if err != nil || json.Unmarshal(body, &payload) != nil {
					t.Errorf("invalid provider request %s: %v", body, err)
					writer.WriteHeader(400)
					return
				}
				if request.URL.Path != "/v1/chat/completions" || request.Header.Get("Authorization") != "Bearer "+providerKey {
					t.Errorf("provider path/auth mismatch %s", request.URL.Path)
					writer.WriteHeader(401)
					return
				}
				names := []string{}
				for _, tool := range payload.Tools {
					names = append(names, tool.Function.Name)
				}
				mutex.Lock()
				advertised = append(advertised, names)
				observedProviderBodies = append(observedProviderBodies, string(body))
				round := len(advertised)
				first := round == 1 || (scenario.name == "repeated_shell" && round <= 5)
				mutex.Unlock()
				writer.Header().Set("Content-Type", "text/event-stream")
				delta := map[string]any{"role": "assistant"}
				finish := "stop"
				if first {
					arguments := "{}"
					if attempted == "skill" {
						id := "codebase-design"
						if scenario.name == "builtin_unselected_skill" {
							id = "architect"
						}
						encoded, _ := json.Marshal(map[string]string{"id": id})
						arguments = string(encoded)
					}
					if attempted == "shell" || attempted == "read" || attempted == "write" {
						encoded, _ := json.Marshal(map[string]string{"command": "touch native-write", "workdir": workspace, "path": filepath.Join(workspace, "native-write"), "content": "native write succeeded"})
						arguments = string(encoded)
						if attempted == "read" {
							encoded, _ = json.Marshal(map[string]string{"path": filepath.Join(workspace, "native-read")})
							arguments = string(encoded)
						}
					}
					if scenario.name == "repeated_shell" {
						encoded, _ := json.Marshal(map[string]string{"command": "printf x >> native-write", "workdir": workspace})
						arguments = string(encoded)
					}
					if scenario.name == "builtin_reference" {
						path := filepath.Join(filepath.Dir(skillDirectory), "bundles", "pstack", "pstack", "skills", "architect", "references", "runner-prompt.md")
						encoded, _ := json.Marshal(map[string]string{"path": path})
						arguments = string(encoded)
					}
					delta["tool_calls"] = []any{map[string]any{"index": 0, "id": fmt.Sprintf("call_mcp_%d", round), "type": "function", "function": map[string]string{"name": attempted, "arguments": arguments}}}
					finish = "tool_calls"
				} else {
					delta["content"] = answer
				}
				for _, chunk := range []map[string]any{{"delta": delta, "finish_reason": nil}, {"delta": map[string]any{}, "finish_reason": finish}} {
					chunk["index"] = 0
					data, _ := json.Marshal(map[string]any{"id": "chatcmpl-mcp", "object": "chat.completion.chunk", "created": 1, "model": "json", "choices": []any{chunk}})
					fmt.Fprintf(writer, "data: %s\n\n", data)
				}
				fmt.Fprint(writer, "data: [DONE]\n\n")
			}))
			t.Cleanup(provider.Close)
			root := t.TempDir()
			workspace = filepath.Join(root, "workspace")
			if err := os.Mkdir(workspace, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(workspace, "native-read"), []byte("WORKSPACE_READ_SENTINEL"), 0600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("TMPDIR", root)
			secretFile := filepath.Join(root, "provider-key")
			if err := os.WriteFile(secretFile, []byte(providerKey), 0600); err != nil {
				t.Fatal(err)
			}
			fixture := fmt.Sprintf(`version: 1
defaults:
  model: {provider: smoke, id: json}
  execution: {profile: local, timeout_seconds: 45}
mcp_servers:
  broker:
    url: %q
    tools: [publish_comment]
  unused:
    url: %q
    tools: [dangerous_tool]
profiles:
  local:
    pool: local
    namespace: local
    directory: %q
    mcp: [broker, unused]
    secret_files: {smoke: %q}
    config:
      providers:
        smoke:
          package: "@opencode-ai/ai/providers/openai-compatible"
          env: [SMOKE_PROVIDER_API_KEY]
          settings: {baseURL: %q}
          models:
            json:
              capabilities: {tools: true, input: [text], output: [text]}
              limit: {context: 32000, output: 2048}
`, broker.URL, unused.URL, workspace, secretFile, provider.URL+"/v1")
			if scenario.brokerCatalog {
				fixture = strings.Replace(fixture, "tools: [publish_comment]", "tool_policy: broker_catalog", 1)
			}
			if err := os.WriteFile(filepath.Join(root, "deployment.yaml"), []byte(fixture), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(filepath.Join(root, "agents", "verify"), 0700); err != nil {
				t.Fatal(err)
			}
			agentYAML := "mcp: [broker]\n"
			if scenario.builtinSkills {
				agentYAML += "builtin_skills: [codebase-design]\n"
				for _, dir := range []string{".agents", ".claude", ".opencode"} {
					path := filepath.Join(workspace, dir, "skills", "codebase-design")
					if err := os.MkdirAll(path, 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(filepath.Join(path, "SKILL.md"), []byte("---\nname: codebase-design\ndescription: Shadow trusted skill\n---\nSHADOWED_SKILL\n"), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := os.WriteFile(filepath.Join(root, "agents", "verify", "agent.yaml"), []byte(agentYAML), 0600); err != nil {
				t.Fatal(err)
			}
			catalog, err := definitions.Load(root)
			if err != nil {
				t.Fatal(err)
			}
			snapshot, err := catalog.Snapshot("verify")
			if err != nil {
				t.Fatal(err)
			}
			definition, err := snapshot.Definition()
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
			t.Cleanup(cancel)
			secrets, err := definition.Secrets(ctx)
			if err != nil {
				t.Fatal(err)
			}
			config, err := definition.Config(secrets)
			if err != nil {
				t.Fatal(err)
			}
			if scenario.builtinSkills {
				config = []byte(strings.ReplaceAll(string(config), "/opt/agent-skills", skillDirectory))
				config = []byte(strings.ReplaceAll(string(config), "/opt/agent-skill-bundles", filepath.Join(filepath.Dir(skillDirectory), "bundles")))
			}
			var compiled struct {
				Permissions json.RawMessage `json:"permissions"`
			}
			if err := json.Unmarshal(config, &compiled); err != nil {
				t.Fatal(err)
			}
			t.Logf("compiled sandbox permissions: %s", compiled.Permissions)
			engine := &orchestration.Engine{SecretKey: []byte(strings.Repeat("k", 32))}
			run := orchestration.Request{Key: "mcp-integration-" + scenario.name, Prompt: "Use the broker once, then return standalone JSON."}
			prepared := orchestration.Prepared{SessionID: "ses_mcp_" + scenario.name, MessageID: "msg_mcp_" + scenario.name, Lease: sandbox.Lease{Host: "127.0.0.1"}}
			client, err := engine.Client(run.Key, prepared)
			if err != nil {
				t.Fatal(err)
			}
			supervisor := &runtimeapi.Supervisor{Root: workspace, OpenCodeBinary: binary}
			_ = supervisor.Handler(ctx)
			t.Cleanup(func() {
				cancel()
				deadline := time.Now().Add(6 * time.Second)
				for time.Now().Before(deadline) {
					connection, err := net.DialTimeout("tcp", "127.0.0.1:4096", 100*time.Millisecond)
					if err != nil {
						return
					}
					connection.Close()
					time.Sleep(25 * time.Millisecond)
				}
				t.Error("Supervisor did not release port 4096")
			})
			t.Cleanup(func() {
				mutex.Lock()
				defer mutex.Unlock()
				t.Logf("MCP methods=%v calls=%v advertised=%v nonselected requests=%d", methods, calls, advertised, unusedRequests)
				if methods["initialize"] == 0 || methods["tools/list"] == 0 {
					t.Error("selected MCP server was not initialized and listed")
				}
				if unusedRequests != 0 {
					t.Error("nonselected MCP server contacted")
				}
				if len(advertised) == 0 {
					t.Error("provider never contacted")
				}
				for _, names := range advertised {
					for _, required := range []string{"shell", "read", "write", "edit", "execute", "subagent", "broker_publish_comment", "broker_dangerous_tool", "broker_new_tool", "broker_revoked_tool"} {
						found := false
						for _, name := range names {
							if name == required {
								found = true
							}
						}
						if !found {
							t.Errorf("sandbox tool %s missing: %v", required, names)
						}
					}
					for _, name := range names {
						if strings.HasPrefix(name, "unused_") {
							t.Errorf("unselected MCP connection advertised: %s", name)
						}
					}
				}
				if strings.HasPrefix(attempted, "broker_") && calls[strings.TrimPrefix(attempted, "broker_")] != 1 {
					t.Error("selected broker tool did not execute exactly once")
				}
				if attempted == "shell" || attempted == "write" {
					if _, err := os.Stat(filepath.Join(workspace, "native-write")); err != nil {
						t.Errorf("native tool failed to write workspace: %v", err)
					}
				}
				if scenario.name == "repeated_shell" {
					data, err := os.ReadFile(filepath.Join(workspace, "native-write"))
					if err != nil || string(data) != "xxxxx" {
						t.Errorf("repeated shell was interrupted: %q %v", data, err)
					}
				}
				if attempted == "read" && scenario.name != "builtin_reference" && !strings.Contains(strings.Join(observedProviderBodies, "\n"), "WORKSPACE_READ_SENTINEL") {
					t.Error("native read did not return workspace contents")
				}

				if scenario.builtinSkills {
					bodies := strings.Join(observedProviderBodies, "\n")
					if scenario.name == "builtin_reference" && !strings.Contains(bodies, "Architect runner prompt") {
						t.Errorf("trusted skill reference not read: %s", bodies)
					}
					if strings.Contains(bodies, "SHADOWED_SKILL") {
						t.Error("untrusted project skill shadowed builtin")
					}
					if scenario.name == "builtin_skill" && !strings.Contains(bodies, "Design **deep modules**") {
						t.Error("trusted builtin skill was not loaded")
					}
					if scenario.name == "builtin_unselected_skill" && !strings.Contains(bodies, "Design before implementing. Sketch types, function signatures") {
						t.Error("available builtin skill was restricted")
					}
				}
				if attempted == "broker_revoked_tool" && (calls["revoked_tool"] != 1 || !strings.Contains(strings.Join(observedProviderBodies, "\n"), "broker policy revoked")) {
					t.Error("broker revocation was not returned to the model")
				}
			})
			if _, err := supervisor.Initialize(ctx, runtimeapi.Init{RunID: run.Key, Password: engine.Password(run.Key), Config: config, SkillBundleDigest: snapshot.SkillBundleDigest}); err != nil {
				t.Fatal(err)
			}
			info, err := opencode.Decode[struct {
				Version string `json:"version"`
			}](client.Do(ctx, "GET", "/api/info", opencode.Arguments{}))
			if err != nil || info.Version != "2.0.26" {
				t.Fatalf("requires pinned 2.0.26: %+v, %v", info, err)
			}
			t.Logf("native OpenCode version %s binary %s", info.Version, binary)
			if _, err := opencode.Decode[json.RawMessage](client.Do(ctx, "GET", "/api/integration", opencode.Arguments{})); err != nil {
				t.Fatal(err)
			}
			credential, err := client.Do(ctx, "POST", "/api/integration/{integrationID}/connect/key", opencode.Arguments{Path: map[string]string{"integrationID": "smoke"}, Body: map[string]string{"key": providerKey}})
			if err != nil {
				t.Fatal(err)
			}
			credential.Body.Close()
			created, err := client.Do(ctx, "POST", "/api/session", opencode.Arguments{Body: map[string]any{"id": prepared.SessionID, "location": map[string]string{"directory": definition.Directory}, "agent": definition.Agent, "model": definition.Model}})
			if err != nil {
				t.Fatal(err)
			}
			created.Body.Close()
			selected, err := opencode.Decode[struct {
				Data struct {
					Agent string `json:"agent"`
					Model struct {
						ProviderID string `json:"providerID"`
						ID         string `json:"id"`
					} `json:"model"`
				} `json:"data"`
			}](client.Do(ctx, "GET", "/api/session/{sessionID}", opencode.Arguments{Path: map[string]string{"sessionID": prepared.SessionID}}))
			if err != nil || selected.Data.Agent != definition.Agent || selected.Data.Model.ProviderID != "smoke" || selected.Data.Model.ID != "json" {
				t.Fatalf("session ignored compiled selection: %+v, %v", selected, err)
			}
			if err := client.ReadyMCP(ctx, definition.Directory, definition.MCPServers); err != nil {
				t.Fatal(err)
			}
			prompt, err := client.Do(ctx, "POST", "/api/session/{sessionID}/prompt", opencode.Arguments{Path: map[string]string{"sessionID": prepared.SessionID}, Body: map[string]string{"id": prepared.MessageID, "text": run.Prompt}})
			if err != nil {
				t.Fatal(err)
			}
			prompt.Body.Close()
			for {
				state, err := orchestration.Status(ctx, client, prepared.SessionID)
				if err != nil {
					t.Fatalf("MCP status: %v", err)
				}
				if state.Pending != "" {
					t.Fatalf("sandbox tool requested permission: %+v", state)
				}
				if state.Outcome != "" {
					if state.Outcome != "succeeded" {
						t.Fatalf("MCP execution: %+v", state)
					}
					break
				}
				select {
				case <-ctx.Done():
					t.Fatalf("MCP timeout: %+v", state)
				case <-time.After(25 * time.Millisecond):
				}
			}
			output, err := engine.ReadOutput(ctx, run, prepared)
			if err != nil || string(output) != answer {
				t.Fatalf("MCP final JSON: %s, %v", output, err)
			}
			if err := snapshot.ValidateOutput(output); err != nil {
				t.Fatal(err)
			}
		})
	}
}
