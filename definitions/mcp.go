package definitions

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
)

type MCPServer struct {
	URL   string   `yaml:"url" json:"url"`
	Tools []string `yaml:"tools" json:"tools"`
}

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
		if len(server.Tools) == 0 {
			return fmt.Errorf("MCP server %s: approved tools required", name)
		}
		for _, tool := range server.Tools {
			action := mcpAction(name, tool)
			if !nativeToolName.MatchString(tool) || actions[action] {
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
