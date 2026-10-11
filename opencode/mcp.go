package opencode

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"time"
)

var mcpName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$`)
var mcpHTTPStatus = regexp.MustCompile(`(?i)(?:HTTP\s+|status(?:\s+code)?[\s:=]*(?:\()?)([1-5][0-9]{2})(?:\b|\))`)

type MCPReadinessError struct {
	server, stage, state string
	httpStatus           int
	cause                error
}

func (e *MCPReadinessError) Error() string {
	return fmt.Sprintf("MCP server %s: stage=%s state=%s http=%d", e.server, e.stage, e.state, e.httpStatus)
}
func (e *MCPReadinessError) Unwrap() error { return e.cause }
func mcpFailure(server, stage, state string, status int, cause error) error {
	if !mcpName.MatchString(server) {
		server = "invalid"
	}
	return &MCPReadinessError{server: server, stage: stage, state: state, httpStatus: status, cause: cause}
}
func mcpState(raw string) string {
	switch raw {
	case "connected", "pending", "failed", "needs_auth", "disabled":
		return raw
	default:
		return "unknown"
	}
}

type mcpInventory struct {
	Data []struct {
		Name   string `json:"name"`
		Status struct {
			Status string `json:"status"`
			Error  string `json:"error"`
		} `json:"status"`
	} `json:"data"`
}

func (client *Client) ReadyMCP(ctx context.Context, directory string, servers []string) error {
	if len(servers) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	query := url.Values{"location[directory]": {directory}}
	inventory := func(server, stage string) (mcpInventory, error) {
		value, err := Decode[mcpInventory](client.Do(ctx, http.MethodGet, "/api/mcp", Arguments{Query: query}))
		if err != nil {
			code := 0
			var httpErr *HTTPError
			if errors.As(err, &httpErr) {
				code = httpErr.Status
			}
			state := "invalid_response"
			if ctx.Err() != nil {
				state = "cancelled"
			}
			return value, mcpFailure(server, stage, state, code, err)
		}
		return value, nil
	}
	pause := func(server, stage, state string) error {
		timer := time.NewTimer(50 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return mcpFailure(server, stage, state, 0, ctx.Err())
		case <-timer.C:
			return nil
		}
	}
	for _, server := range servers {
		if !mcpName.MatchString(server) {
			return mcpFailure(server, "configuration", "invalid", 0, nil)
		}
		// OpenCode's location can be ready before its configured MCP inventory loads.
		// A connect before discovery gets McpServerNotFoundError even with valid config.
		for {
			values, err := inventory(server, "inventory")
			if err != nil {
				return err
			}
			found := false
			for _, entry := range values.Data {
				if entry.Name == server {
					state := mcpState(entry.Status.Status)
					if state == "failed" || state == "needs_auth" || state == "disabled" {
						code := 0
						if match := mcpHTTPStatus.FindStringSubmatch(entry.Status.Error); len(match) == 2 {
							code, _ = strconv.Atoi(match[1])
						}
						return mcpFailure(server, "inventory", state, code, nil)
					}
					found = true
					break
				}
			}
			if found {
				break
			}
			if err = pause(server, "inventory", "missing"); err != nil {
				return err
			}
		}
		response, err := client.Do(ctx, http.MethodPost, "/api/experimental/mcp/{server}/connect", Arguments{Path: map[string]string{"server": server}, Query: query})
		if err != nil {
			code := 0
			var httpErr *HTTPError
			if errors.As(err, &httpErr) {
				code = httpErr.Status
			}
			return mcpFailure(server, "connect", "rejected", code, err)
		}
		response.Body.Close()
		for {
			values, err := inventory(server, "status")
			if err != nil {
				return err
			}
			state := "missing"
			code := 0
			for _, entry := range values.Data {
				if entry.Name == server {
					state = mcpState(entry.Status.Status)
					if match := mcpHTTPStatus.FindStringSubmatch(entry.Status.Error); len(match) == 2 {
						code, _ = strconv.Atoi(match[1])
					}
					break
				}
			}
			if state == "connected" {
				break
			}
			if state != "missing" && state != "pending" {
				return mcpFailure(server, "status", state, code, nil)
			}
			if err = pause(server, "status", state); err != nil {
				return err
			}
		}
	}
	return nil
}
