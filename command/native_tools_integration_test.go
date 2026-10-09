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

func TestOpenCodeNativeToolGrants(t *testing.T) {
	binary := os.Getenv("AGENT_RUNTIME_TEST_OPENCODE_BINARY")
	if binary == "" {
		t.Skip("set AGENT_RUNTIME_TEST_OPENCODE_BINARY to pinned OpenCode 2.0.26")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range []string{"webfetch", "shell"} {
		for _, allowed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/allowed=%t", action, allowed), func(t *testing.T) {
				root := t.TempDir()
				workspace := filepath.Join(root, "workspace")
				if err := os.Mkdir(workspace, 0700); err != nil {
					t.Fatal(err)
				}
				t.Setenv("TMPDIR", root)
				var mu sync.Mutex
				fetched, calls := 0, 0
				var advertised [][]string
				source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					mu.Lock()
					fetched++
					mu.Unlock()
					fmt.Fprint(w, "native-fetch-proof")
				}))
				t.Cleanup(source.Close)
				marker := filepath.Join(workspace, "native-shell-proof")
				arguments := map[string]any{"url": source.URL, "format": "text"}
				if action == "shell" {
					arguments = map[string]any{"command": "printf native-shell-proof > native-shell-proof", "timeout": 10000}
				}
				argumentsJSON, _ := json.Marshal(arguments)
				provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					var body struct {
						Tools []struct {
							Function struct {
								Name string `json:"name"`
							} `json:"function"`
						} `json:"tools"`
					}
					data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
					if err != nil || json.Unmarshal(data, &body) != nil {
						t.Error("invalid provider payload")
						w.WriteHeader(400)
						return
					}
					names := []string{}
					for _, tool := range body.Tools {
						names = append(names, tool.Function.Name)
					}
					mu.Lock()
					calls++
					first := calls == 1
					advertised = append(advertised, names)
					mu.Unlock()
					delta := map[string]any{"role": "assistant", "content": `{"proof":true}`}
					finish := "stop"
					if first {
						delete(delta, "content")
						delta["tool_calls"] = []any{map[string]any{"index": 0, "id": "call_native", "type": "function", "function": map[string]string{"name": action, "arguments": string(argumentsJSON)}}}
						finish = "tool_calls"
					}
					w.Header().Set("Content-Type", "text/event-stream")
					for _, chunk := range []map[string]any{{"delta": delta, "finish_reason": nil}, {"delta": map[string]any{}, "finish_reason": finish}} {
						chunk["index"] = 0
						encoded, _ := json.Marshal(map[string]any{"id": "chatcmpl-native", "object": "chat.completion.chunk", "created": 1, "model": "json", "choices": []any{chunk}})
						fmt.Fprintf(w, "data: %s\n\n", encoded)
					}
					fmt.Fprint(w, "data: [DONE]\n\n")
				}))
				t.Cleanup(provider.Close)
				catalog, err := definitions.Load("../examples/definitions")
				if err != nil {
					t.Fatal(err)
				}
				agent := catalog.Agents["verify"]
				agent.Model = definitions.Model{Provider: "smoke", ID: "json"}
				agent.Tools = []string{"read"}
				if allowed {
					agent.Tools = []string{action}
				}
				catalog.Agents[agent.Name] = agent
				profile := catalog.Profiles[agent.Execution.Profile]
				profile.Directory = workspace
				profile.SecretFiles = nil
				profile.Tools = []string{"read", "webfetch", "shell"}
				profile.Config, _ = json.Marshal(map[string]any{"providers": map[string]any{"smoke": map[string]any{"package": "@opencode-ai/ai/providers/openai-compatible", "env": []string{"SMOKE_PROVIDER_API_KEY"}, "settings": map[string]string{"baseURL": provider.URL + "/v1"}, "models": map[string]any{"json": map[string]any{"capabilities": map[string]any{"tools": true, "input": []string{"text"}, "output": []string{"text"}}, "limit": map[string]int{"context": 32000, "output": 2048}}}}}})
				catalog.Profiles[agent.Execution.Profile] = profile
				snapshot, err := catalog.Snapshot(agent.Name)
				if err != nil {
					t.Fatal(err)
				}
				definition, err := snapshot.Definition()
				if err != nil {
					t.Fatal(err)
				}
				config, err := definition.Config(nil)
				if err != nil {
					t.Fatal(err)
				}
				ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
				engine := &orchestration.Engine{SecretKey: []byte(strings.Repeat("k", 32))}
				run := orchestration.Request{Key: fmt.Sprintf("native-%s-%t", action, allowed), Prompt: "Use the requested tool once, then return JSON."}
				prepared := orchestration.Prepared{SessionID: "ses_native_tools", MessageID: "msg_native_tools", Lease: sandbox.Lease{Host: "127.0.0.1"}}
				client, err := engine.Client(run.Key, prepared)
				if err != nil {
					cancel()
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
					t.Error("native supervisor did not stop")
				})
				if _, err := supervisor.Initialize(ctx, runtimeapi.Init{RunID: run.Key, Password: engine.Password(run.Key), Config: config}); err != nil {
					t.Fatal(err)
				}
				version, err := opencode.Decode[struct {
					Version string `json:"version"`
				}](client.Do(ctx, "GET", "/api/info", opencode.Arguments{}))
				if err != nil || version.Version != "2.0.26" {
					t.Fatalf("requires pinned native version: %+v %v", version, err)
				}
				if _, err := opencode.Decode[json.RawMessage](client.Do(ctx, "GET", "/api/integration", opencode.Arguments{})); err != nil {
					t.Fatal(err)
				}
				response, err := client.Do(ctx, "POST", "/api/integration/{integrationID}/connect/key", opencode.Arguments{Path: map[string]string{"integrationID": "smoke"}, Body: map[string]string{"key": "local-fake-key"}})
				if err != nil {
					t.Fatal(err)
				}
				response.Body.Close()
				response, err = client.Do(ctx, "POST", "/api/session", opencode.Arguments{Body: map[string]any{"id": prepared.SessionID, "location": map[string]string{"directory": definition.Directory}, "agent": definition.Agent, "model": definition.Model}})
				if err != nil {
					t.Fatal(err)
				}
				response.Body.Close()
				response, err = client.Do(ctx, "POST", "/api/session/{sessionID}/prompt", opencode.Arguments{Path: map[string]string{"sessionID": prepared.SessionID}, Body: map[string]string{"id": prepared.MessageID, "text": run.Prompt}})
				if err != nil {
					t.Fatal(err)
				}
				response.Body.Close()
				for {
					state, err := orchestration.Status(ctx, client, prepared.SessionID)
					if err != nil {
						t.Fatal(err)
					}
					if state.Pending != "" {
						t.Fatalf("unexpected permission question: %+v", state)
					}
					if state.Outcome != "" {
						if state.Outcome != "succeeded" {
							t.Fatalf("native failure: %+v", state)
						}
						break
					}
					select {
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					case <-time.After(25 * time.Millisecond):
					}
				}
				result, err := engine.ReadOutput(ctx, run, prepared)
				if err != nil || string(result) != `{"proof":true}` {
					t.Fatalf("result: %s %v", result, err)
				}
				mu.Lock()
				defer mu.Unlock()
				for _, names := range advertised {
					if len(names) != 1 || names[0] != agent.Tools[0] {
						t.Fatalf("advertised unapproved tools: %v", names)
					}
				}
				if calls < 2 {
					t.Fatal("no tool response round")
				}
				if action == "webfetch" {
					want := 0
					if allowed {
						want = 1
					}
					if fetched != want {
						t.Fatalf("fetch requests: %d want %d", fetched, want)
					}
				}
				if action == "shell" {
					data, err := os.ReadFile(marker)
					if allowed && (err != nil || string(data) != "native-shell-proof") {
						t.Fatalf("shell did not execute: %q %v", data, err)
					}
					if !allowed && !os.IsNotExist(err) {
						t.Fatal("denied shell wrote marker")
					}
				}
			})
		}
	}
}
