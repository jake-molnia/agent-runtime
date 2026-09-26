// Package opencode provides the complete pinned V2 HTTP operation set, SSE, and WebSockets.
package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"
)

const Version = "2.0.12"
const MaxResponse = 16 << 20

type Client struct {
	base   *url.URL
	http   *http.Client
	header http.Header
}

type Arguments struct {
	Path   map[string]string
	Query  url.Values
	Body   any
	Header http.Header
}

// New preserves streaming bodies; deadlines belong to individual request contexts.
func New(address string, header http.Header, transport http.RoundTripper) (*Client, error) {
	u, err := url.Parse(address)
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("invalid OpenCode endpoint")
	}
	if transport == nil {
		transport = http.DefaultTransport.(*http.Transport).Clone()
	}
	return &Client{base: u, header: header.Clone(), http: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (c *Client) request(ctx context.Context, method, pattern string, a Arguments) (*http.Request, error) {
	for k, v := range a.Path {
		if v == "" {
			return nil, errors.New("empty path parameter")
		}
		if k == "*" {
			pattern = strings.ReplaceAll(pattern, "*", url.PathEscape(v))
		} else {
			pattern = strings.ReplaceAll(pattern, "{"+k+"}", url.PathEscape(v))
		}
	}
	if strings.ContainsAny(pattern, "{}*") || !strings.HasPrefix(pattern, "/api/") {
		return nil, errors.New("unresolved or invalid API path")
	}
	var body io.Reader
	if a.Body != nil {
		b, err := json.Marshal(a.Body)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.base.String(), "/")+pattern, body)
	if err != nil {
		return nil, err
	}
	req.URL.RawQuery = a.Query.Encode()
	req.Header = c.header.Clone()
	if req.Header == nil {
		req.Header = make(http.Header)
	}
	for k, v := range a.Header {
		req.Header[k] = v
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return req, nil
}

// Do never retries mutations. HTTPError intentionally excludes potentially sensitive bodies.
func (c *Client) Do(ctx context.Context, method, path string, a Arguments) (*http.Response, error) {
	req, err := c.request(ctx, method, path, a)
	if err != nil {
		return nil, err
	}
	res, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("OpenCode transport failed: %w", err)
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		res.Body.Close()
		return nil, &HTTPError{Status: res.StatusCode}
	}
	return res, nil
}

type HTTPError struct{ Status int }

func (e *HTTPError) Error() string { return fmt.Sprintf("OpenCode HTTP %d", e.Status) }

func Decode[T any](res *http.Response, err error) (T, error) {
	var out T
	if err != nil {
		return out, err
	}
	defer res.Body.Close()
	b, err := io.ReadAll(io.LimitReader(res.Body, MaxResponse+1))
	if err != nil {
		return out, err
	}
	if len(b) > MaxResponse {
		return out, errors.New("OpenCode response exceeds limit")
	}
	err = json.Unmarshal(b, &out)
	return out, err
}

// Socket forwards the native PTY protocol without interpreting or logging its payloads.
func (c *Client) Socket(ctx context.Context, path string, args Arguments) (*websocket.Conn, error) {
	req, err := c.request(ctx, http.MethodGet, path, args)
	if err != nil {
		return nil, err
	}
	conn, res, err := websocket.Dial(ctx, req.URL.String(), &websocket.DialOptions{HTTPClient: c.http, HTTPHeader: req.Header})
	if err != nil {
		if res != nil {
			return nil, &HTTPError{Status: res.StatusCode}
		}
		return nil, errors.New("OpenCode websocket connection failed")
	}
	conn.SetReadLimit(MaxResponse)
	return conn, nil
}

func (c *Client) Ready(ctx context.Context) error {
	timer := time.NewTicker(100 * time.Millisecond)
	defer timer.Stop()
	for {
		res, err := c.ServerInfo(ctx, Arguments{})
		if err == nil {
			res.Body.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}
	}
}
