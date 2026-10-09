package opencode

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"time"
)

func (client *Client) ReadyMCP(ctx context.Context, directory string, servers []string) error {
	if len(servers) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	query := url.Values{"location[directory]": {directory}}
	for _, server := range servers {
		response, err := client.Do(ctx, http.MethodPost, "/api/experimental/mcp/{server}/connect", Arguments{Path: map[string]string{"server": server}, Query: query})
		if err != nil {
			return errors.New("approved MCP connection failed")
		}
		response.Body.Close()
	}
	status, err := Decode[struct {
		Data []struct {
			Name   string `json:"name"`
			Status struct {
				Status string `json:"status"`
			} `json:"status"`
		} `json:"data"`
	}](client.Do(ctx, http.MethodGet, "/api/mcp", Arguments{Query: query}))
	if err != nil {
		return errors.New("approved MCP readiness unavailable")
	}
	for _, server := range servers {
		connected := false
		for _, entry := range status.Data {
			if entry.Name == server && entry.Status.Status == "connected" {
				connected = true
			}
		}
		if !connected {
			return errors.New("approved MCP is not connected; credentials or broker unavailable")
		}
	}
	timer := time.NewTimer(150 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
