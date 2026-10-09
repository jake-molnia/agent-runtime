package definitions

import (
	"encoding/json"
	"path/filepath"
	"reflect"
	"testing"
)

func TestDesktopExampleAgentGrants(t *testing.T) {
	catalog, err := Load(filepath.Join("..", "examples", "definitions"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range BuiltinNames() {
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
				Permissions []permission `json:"permissions"`
				Agents      map[string]struct {
					Permissions []permission `json:"permissions"`
				} `json:"agents"`
			}
			if err := json.Unmarshal(data, &config); err != nil {
				t.Fatal(err)
			}
			for _, grants := range [][]permission{config.Permissions, config.Agents["authored"].Permissions} {
				allowed := map[string]bool{}
				for _, grant := range grants {
					allowed[grant.Action] = grant.Effect == "allow"
				}
				for _, tool := range []string{"aio_sandbox_execute_bash", "aio_sandbox_file_operations", "aio_browser_gui_screenshot", "aio_browser_gui_execute_action", "aio_documents_convert_to_markdown", "browser_browser_navigate", "browser_browser_snapshot", "browser_browser_pdf_save"} {
					if !allowed[tool] {
						t.Errorf("example does not grant %s", tool)
					}
				}
				for _, tool := range []string{"*", "shell", "browser_browser_close", "browser_browser_install"} {
					if allowed[tool] {
						t.Errorf("example unexpectedly grants %s", tool)
					}
				}
			}
		})
	}
}
