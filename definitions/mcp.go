package definitions

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
)

type MCPServer struct {
	URL        string   `yaml:"url" json:"url"`
	Tools      []string `yaml:"tools" json:"tools"`
	ToolPolicy string   `yaml:"tool_policy,omitempty" json:"tool_policy,omitempty"`
}

// Names and namespace behavior are pinned to the compiler policy's OpenCode release.
var nativeActions = []string{"external_directory", "doom_loop", "apply_patch", "list_mcp_resources", "read_mcp_resource", "opencode_list_mcp_resources", "opencode_read_mcp_resource", "shell", "read", "write", "edit", "glob", "grep", "skill", "subagent", "question", "webfetch", "websearch", "execute"}

var nativeToolName = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_.:-]*$`)
var nonToolCharacter = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

func mcpAction(server, tool string) string {
	return server + "_" + nonToolCharacter.ReplaceAllString(tool, "_")
}

func validateMCPServers(servers map[string]MCPServer) error {
	actions := map[string]bool{}
	for name, server := range servers {
		if !validName.MatchString(name) {
			return fmt.Errorf("invalid MCP server name: %q", name)
		}
		endpoint, err := url.Parse(server.URL)
		if err != nil || endpoint.Host == "" || endpoint.Hostname() == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.ForceQuery || endpoint.Fragment != "" || strings.Contains(server.URL, "#") || endpoint.Opaque != "" {
			return fmt.Errorf("MCP server %s: URL must have a host and no credentials, query, or fragment", name)
		}
		if endpoint.Scheme != "https" {
			address := net.ParseIP(endpoint.Hostname())
			if endpoint.Scheme != "http" || address == nil || !address.IsLoopback() {
				return fmt.Errorf("MCP server %s: HTTPS required except literal loopback HTTP", name)
			}
		}
		if server.ToolPolicy != "" && server.ToolPolicy != "broker_catalog" {
			return fmt.Errorf("MCP server %s: unsupported tool_policy", name)
		}
		if server.ToolPolicy == "broker_catalog" {
			for _, action := range nativeActions {
				if strings.HasPrefix(action, name+"_") {
					return fmt.Errorf("MCP server %s: broker namespace overlaps native action %s", name, action)
				}
			}
			if len(server.Tools) != 0 {
				return fmt.Errorf("MCP server %s: broker_catalog cannot specify tools", name)
			}
			for other := range servers {
				if other != name && (strings.HasPrefix(other+"_", name+"_") || strings.HasPrefix(name+"_", other+"_")) {
					return fmt.Errorf("MCP server %s: overlapping broker namespace with %s", name, other)
				}
			}
		}
		for _, tool := range server.Tools {
			action := mcpAction(name, tool)
			if !nativeToolName.MatchString(tool) || actions[action] || contains(nativeActions, action) {
				return fmt.Errorf("MCP server %s: invalid, duplicate, or colliding tool: %q", name, tool)
			}
			actions[action] = true
		}
	}
	return nil
}

func validateMCPReferences(names []string) error {
	seen := map[string]bool{}
	for _, name := range names {
		if !validName.MatchString(name) || seen[name] {
			return fmt.Errorf("invalid or repeated MCP reference: %q", name)
		}
		seen[name] = true
	}
	return nil
}
