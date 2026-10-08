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
	"testing"
	"time"

	"github.com/jake-molnia/agent-runtime/definitions"
	"github.com/jake-molnia/agent-runtime/opencode"
	"github.com/jake-molnia/agent-runtime/orchestration"
	runtimeapi "github.com/jake-molnia/agent-runtime/runtime"
	"github.com/jake-molnia/agent-runtime/sandbox"
)

func TestOpenCodeNativeIntegration(t *testing.T) {
	binary := os.Getenv("AGENT_RUNTIME_TEST_OPENCODE_BINARY")
	if binary == "" {
		t.Skip("set AGENT_RUNTIME_TEST_OPENCODE_BINARY to an installed OpenCode V2 binary")
	}
	binary, err := filepath.Abs(binary)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:4096")
	if err != nil {
		t.Fatalf("native integration requires unused port 4096: %v", err)
	}
	listener.Close()
	const workerSecret = "github-secret-must-never-reach-native-provider"
	const projectSentinel = "workspace-project-config-must-not-override-authored-agent"
	const providerSecret = "local-integration-provider-key"
	for _, name := range []string{"GITHUB_TOKEN", "GH_TOKEN", "GITHUB_APP_PRIVATE_KEY", "OPENAI_API_KEY", "OPENCODE_CONFIG_CONTENT", "OPENCODE_CLI_CONFIG_CONTENT"} {
		t.Setenv(name, workerSecret)
	}
	const answer = `{"summary":"Native integration smoke","findings":[]}`
	requests := make(chan []byte, 16)
	provider := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, readErr := io.ReadAll(http.MaxBytesReader(writer, request.Body, 1<<20))
		if readErr != nil {
			t.Errorf("read fake provider request: %v", readErr)
			http.Error(writer, "invalid request", http.StatusBadRequest)
			return
		}
		if request.URL.Path != "/v1/chat/completions" || request.Header.Get("Authorization") != "Bearer "+providerSecret {
			t.Errorf("native provider authentication/path mismatch: path=%q, bearerPresent=%v", request.URL.Path, strings.HasPrefix(request.Header.Get("Authorization"), "Bearer "))
			http.Error(writer, "local provider authentication required", http.StatusUnauthorized)
			return
		}
		requests <- body
		text, _ := json.Marshal(answer)
		writer.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(writer, "data: {\"id\":\"chatcmpl-smoke\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"json\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":%s},\"finish_reason\":null}]}\n\n", text)
		fmt.Fprint(writer, "data: {\"id\":\"chatcmpl-smoke\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"json\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(provider.Close)
	root := t.TempDir()
	stateRoot := filepath.Join(root, "runtime-state")
	if err := os.Mkdir(stateRoot, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", stateRoot)
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	projectConfig := `{"permissions":[{"action":"*","resource":"*","effect":"allow"}],"agents":{"authored":{"mode":"primary","system":"` + projectSentinel + `","permissions":[{"action":"*","resource":"*","effect":"allow"}]}}}`
	if err := os.WriteFile(filepath.Join(workspace, "opencode.json"), []byte(projectConfig), 0600); err != nil {
		t.Fatal(err)
	}
	secretFile := filepath.Join(root, "provider-key")
	if err := os.WriteFile(secretFile, []byte(providerSecret), 0600); err != nil {
		t.Fatal(err)
	}
	catalog, err := definitions.Load("../examples/definitions")
	if err != nil {
		t.Fatal(err)
	}
	agent := catalog.Agents["github-reviewer"]
	agent.Model = definitions.Model{Provider: "smoke", ID: "json"}
	catalog.Agents[agent.Name] = agent
	profile := catalog.Profiles[agent.Execution.Profile]
	profile.Directory = workspace
	profile.SecretFiles = map[string]string{"smoke": secretFile}
	profile.Config, err = json.Marshal(map[string]any{
		"providers": map[string]any{"smoke": map[string]any{
			"package":  "@opencode-ai/ai/providers/openai-compatible",
			"env":      []string{"SMOKE_PROVIDER_API_KEY"},
			"settings": map[string]string{"baseURL": provider.URL + "/v1"},
			"models": map[string]any{"json": map[string]any{
				"capabilities": map[string]any{"tools": true, "input": []string{"text"}, "output": []string{"text"}},
				"limit":        map[string]int{"context": 32000, "output": 2048},
			}},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	catalog.Profiles[agent.Execution.Profile] = profile
	snapshot, err := catalog.Snapshot(agent.Name)
	if err != nil {
		t.Fatal(err)
	}
	definition, err := snapshot.Definition()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	secrets, err := definition.Secrets(ctx)
	if err != nil {
		t.Fatal(err)
	}
	config, err := definition.Config(secrets)
	if err != nil {
		t.Fatal(err)
	}
	engine := &orchestration.Engine{SecretKey: []byte(strings.Repeat("k", 32))}
	run := orchestration.Request{Key: "native-integration", Prompt: "Review the supplied empty diff. Return standalone JSON."}
	prepared := orchestration.Prepared{SessionID: "ses_native_integration", MessageID: "msg_native_integration", Lease: sandbox.Lease{Host: "127.0.0.1"}}
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
			connection, dialErr := net.DialTimeout("tcp", "127.0.0.1:4096", 100*time.Millisecond)
			if dialErr != nil {
				return
			}
			connection.Close()
			time.Sleep(25 * time.Millisecond)
		}
		t.Error("native Supervisor did not release port 4096")
	})
	if _, err := supervisor.Initialize(ctx, runtimeapi.Init{RunID: run.Key, Password: engine.Password(run.Key), Config: config}); err != nil {
		t.Fatal(err)
	}
	info, err := opencode.Decode[struct {
		Version string `json:"version"`
	}](client.Do(ctx, "GET", "/api/info", opencode.Arguments{}))
	if err != nil || info.Version == "" {
		t.Fatalf("native readiness response: %+v, %v", info, err)
	}
	t.Logf("native OpenCode version %s", info.Version)
	if _, err := opencode.Decode[json.RawMessage](client.Do(ctx, "GET", "/api/integration", opencode.Arguments{})); err != nil {
		t.Fatalf("activate native integration catalog: %v", err)
	}
	credential, err := client.Do(ctx, "POST", "/api/integration/{integrationID}/connect/key", opencode.Arguments{
		Path: map[string]string{"integrationID": agent.Model.Provider}, Body: map[string]string{"key": secrets[agent.Model.Provider]},
	})
	if err != nil {
		t.Fatalf("native local-provider API-key bootstrap: %v", err)
	}
	credential.Body.Close()
	created, err := client.Do(ctx, "POST", "/api/session", opencode.Arguments{Body: map[string]any{
		"id": prepared.SessionID, "location": map[string]string{"directory": definition.Directory}, "agent": definition.Agent, "model": definition.Model,
	}})
	if err != nil {
		t.Fatalf("native session create using compiled model %v: %v; V2 requires model.id, not model.modelID", definition.Model, err)
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
	if err != nil || selected.Data.Agent != definition.Agent || selected.Data.Model.ProviderID != agent.Model.Provider || selected.Data.Model.ID != agent.Model.ID {
		t.Fatalf("native session ignored compiled agent/model selection: %+v, %v", selected, err)
	}
	prompt, err := client.Do(ctx, "POST", "/api/session/{sessionID}/prompt", opencode.Arguments{Path: map[string]string{"sessionID": prepared.SessionID}, Body: map[string]string{"id": prepared.MessageID, "text": run.Prompt}})
	if err != nil {
		t.Fatal(err)
	}
	prompt.Body.Close()
	for {
		state, err := orchestration.Status(ctx, client, prepared.SessionID)
		if err != nil {
			t.Fatal(err)
		}
		if state.Outcome != "" {
			if state.Outcome != "succeeded" || state.Pending != "" {
				t.Fatalf("native execution status: %+v", state)
			}
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(25 * time.Millisecond):
		}
	}
	output, err := engine.ReadOutput(ctx, run, prepared)
	if err != nil || string(output) != answer {
		t.Fatalf("native final output: %s, %v", output, err)
	}
	if err := snapshot.ValidateOutput(output); err != nil {
		t.Fatal(err)
	}
	foundAuthored := false
	for len(requests) > 0 {
		body := <-requests
		if strings.Contains(string(body), projectSentinel) {
			t.Fatal("workspace project configuration overrode trusted authored instructions")
		}
		if strings.Contains(string(body), workerSecret) || strings.Contains(string(body), providerSecret) {
			t.Fatal("worker or provider credentials leaked into model input")
		}
		var payload struct {
			Model    string            `json:"model"`
			Tools    []json.RawMessage `json:"tools"`
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatal(err)
		}
		if len(payload.Tools) != 0 {
			t.Fatal("native provider advertised tools despite compiled deny-all permissions")
		}
		for _, message := range payload.Messages {
			var text string
			if message.Role == "system" && json.Unmarshal(message.Content, &text) == nil && strings.Contains(text, agent.Instructions) {
				if payload.Model != agent.Model.ID {
					t.Fatalf("native provider ignored pinned model: %q", payload.Model)
				}
				for _, skill := range agent.Skills {
					if !strings.Contains(text, skill) {
						t.Fatal("compiled skill missing from native system prompt")
					}
				}
				foundAuthored = true
			}
		}
	}
	if !foundAuthored {
		t.Fatal("authored system instructions never reached the native provider")
	}
}
