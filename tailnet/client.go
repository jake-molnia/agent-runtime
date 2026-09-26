// Package tailnet issues single-use sandbox identities without sending OAuth credentials to sandboxes.
package tailnet

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sync"
	"time"
)

type Client struct {
	ClientID     string
	ClientSecret func(context.Context) (string, error)
	HTTP         *http.Client
	mu           sync.Mutex
	token        string
	expires      time.Time
}
type Identity struct {
	ID       string `json:"id"`
	Key      string `json:"key"`
	Hostname string `json:"hostname"`
}

func (c *Client) client() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
func (c *Client) accessToken(ctx context.Context) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Until(c.expires) > time.Minute {
		return c.token, nil
	}
	if c.ClientSecret == nil {
		return "", errors.New("missing Tailscale OAuth secret provider")
	}
	secret, err := c.ClientSecret(ctx)
	if err != nil {
		return "", errors.New("Tailscale OAuth secret unavailable")
	}
	values := url.Values{"grant_type": {"client_credentials"}, "client_id": {c.ClientID}, "client_secret": {secret}}
	req, err := http.NewRequestWithContext(ctx, "POST", "https://api.tailscale.com/api/v2/oauth/token", bytes.NewBufferString(values.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	res, err := c.client().Do(req)
	if err != nil {
		return "", errors.New("Tailscale OAuth request failed")
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		return "", fmt.Errorf("Tailscale OAuth HTTP %d", res.StatusCode)
	}
	var out struct {
		Token   string `json:"access_token"`
		Expires int    `json:"expires_in"`
	}
	if err = json.NewDecoder(io.LimitReader(res.Body, 65536)).Decode(&out); err != nil || out.Token == "" {
		return "", errors.New("invalid Tailscale OAuth response")
	}
	c.token = out.Token
	c.expires = time.Now().Add(time.Duration(out.Expires) * time.Second)
	return c.token, nil
}
func (c *Client) request(ctx context.Context, method, path string, body any, out any) error {
	token, err := c.accessToken(ctx)
	if err != nil {
		return err
	}
	var data []byte
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, "https://api.tailscale.com/api/v2/"+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	res, err := c.client().Do(req)
	if err != nil {
		return errors.New("Tailscale API request failed")
	}
	defer res.Body.Close()
	if method == "DELETE" && res.StatusCode == 404 {
		return nil
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("Tailscale API HTTP %d", res.StatusCode)
	}
	if out == nil {
		return nil
	}
	if err = json.NewDecoder(io.LimitReader(res.Body, 4<<20)).Decode(out); err != nil {
		return errors.New("invalid Tailscale API response")
	}
	return nil
}

var tagPattern = regexp.MustCompile(`^tag:[a-z][a-z0-9-]*$`)

func (c *Client) Issue(ctx context.Context, hostname string, tags []string) (Identity, error) {
	if hostname == "" || len(tags) == 0 {
		return Identity{}, errors.New("identity requires hostname and policy tags")
	}
	found := false
	for _, tag := range tags {
		if !tagPattern.MatchString(tag) {
			return Identity{}, errors.New("invalid policy tag")
		}
		if tag == "tag:agent-sandbox" {
			found = true
		}
	}
	if !found {
		return Identity{}, errors.New("sandbox identity tag required")
	}
	body := map[string]any{"expirySeconds": 300, "description": hostname, "capabilities": map[string]any{"devices": map[string]any{"create": map[string]any{"reusable": false, "ephemeral": true, "preauthorized": true, "tags": tags}}}}
	out := Identity{Hostname: hostname}
	err := c.request(ctx, "POST", "tailnet/-/keys", body, &out)
	return out, err
}
func (c *Client) Revoke(ctx context.Context, identity Identity) error {
	var keyErr error
	if identity.ID != "" {
		keyErr = c.request(ctx, "DELETE", "tailnet/-/keys/"+url.PathEscape(identity.ID), nil, nil)
	}
	var list struct {
		Devices []struct {
			ID       string   `json:"id"`
			Hostname string   `json:"hostname"`
			Tags     []string `json:"tags"`
		} `json:"devices"`
	}
	if err := c.request(ctx, "GET", "tailnet/-/devices", nil, &list); err != nil {
		return errors.Join(keyErr, err)
	}
	for _, d := range list.Devices {
		if d.Hostname != identity.Hostname {
			continue
		}
		for _, tag := range d.Tags {
			if tag == "tag:agent-sandbox" {
				keyErr = errors.Join(keyErr, c.request(ctx, "DELETE", "device/"+url.PathEscape(d.ID), nil, nil))
				break
			}
		}
	}
	return keyErr
}

// Reap removes orphaned identities only after the caller has successfully listed all owned claims.
func (c *Client) Reap(ctx context.Context, active map[string]bool) error {
	var list struct {
		Devices []struct {
			ID       string    `json:"id"`
			Hostname string    `json:"hostname"`
			Created  time.Time `json:"created"`
			Tags     []string  `json:"tags"`
		} `json:"devices"`
	}
	if err := c.request(ctx, "GET", "tailnet/-/devices", nil, &list); err != nil {
		return err
	}
	var result error
	for _, d := range list.Devices {
		if active[d.Hostname] || d.Created.IsZero() || time.Since(d.Created) < 5*time.Minute {
			continue
		}
		// Only this library's deterministic names are eligible, never other sandbox users.
		if !regexp.MustCompile(`^ar-[a-f0-9]{32}$`).MatchString(d.Hostname) {
			continue
		}
		for _, tag := range d.Tags {
			if tag == "tag:agent-sandbox" {
				result = errors.Join(result, c.request(ctx, "DELETE", "device/"+url.PathEscape(d.ID), nil, nil))
				break
			}
		}
	}
	return result
}
