package definitions

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDesktopExampleAgentConnections(t *testing.T) {
	catalog, err := Load(filepath.Join("..", "examples", "definitions"))
	if err != nil {
		t.Fatal(err)
	}
	// These examples opt into desktop tools; other built-in presets keep their own tool policy.
	for _, name := range []string{"code-review", "verify", "adversarial-review"} {
		t.Run(name, func(t *testing.T) {
			definition, err := catalog.Resolve(name)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(definition.MCPServers, []string{"aio", "browser"}) {
				t.Fatalf("missing desktop connections: %v", definition.MCPServers)
			}
			data, err := definition.Config(map[string]string{"openai": "example-test-credential"})
			if err != nil {
				t.Fatal(err)
			}
			var config struct {
				MCP struct {
					Servers map[string]struct {
						URL string `json:"url"`
					} `json:"servers"`
				} `json:"mcp"`
				Permissions []permission `json:"permissions"`
				Agents      map[string]struct {
					Permissions []permission `json:"permissions"`
				} `json:"agents"`
			}
			if err := json.Unmarshal(data, &config); err != nil {
				t.Fatal(err)
			}
			if len(config.MCP.Servers) != 2 || config.MCP.Servers["aio"].URL != "http://127.0.0.1:18091/mcp" || config.MCP.Servers["browser"].URL != "http://127.0.0.1:8931/mcp" {
				t.Fatalf("desktop connections do not use sandbox-local services: %+v", config.MCP.Servers)
			}
			for _, grants := range [][]permission{config.Permissions, config.Agents["authored"].Permissions} {
				if !reflect.DeepEqual(grants, []permission{{Action: "*", Resource: "*", Effect: "allow"}}) {
					t.Errorf("desktop connections changed the sandbox harness policy: %+v", grants)
				}
			}
		})
	}
}
