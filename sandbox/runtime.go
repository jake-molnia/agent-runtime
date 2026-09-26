package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	process "sigs.k8s.io/agent-sandbox/packages/sandboxd/spec/process/v1"
)

type Runtime struct {
	// Processes exposes Start, Execute, WriteStdin, SendSignal and ResizeTTY without narrowing the protocol.
	Processes  process.ProcessServiceClient
	Files      *Files
	connection *grpc.ClientConn
}

func Connect(host string, transport http.RoundTripper, opts ...grpc.DialOption) (*Runtime, error) {
	if host == "" || strings.ContainsAny(host, "/ :?#@") {
		return nil, errors.New("invalid sandbox hostname")
	}
	if len(opts) == 0 {
		opts = []grpc.DialOption{grpc.WithTransportCredentials(insecure.NewCredentials())}
	}
	opts = append(opts, grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(16<<20)))
	conn, err := grpc.NewClient(net.JoinHostPort(host, "9090"), opts...)
	if err != nil {
		return nil, err
	}
	f, err := NewFiles("http://"+net.JoinHostPort(host, "8080"), transport)
	if err != nil {
		conn.Close()
		return nil, err
	}
	return &Runtime{Processes: process.NewProcessServiceClient(conn), Files: f, connection: conn}, nil
}
func (r *Runtime) Close() error { return r.connection.Close() }

type Files struct {
	base string
	http *http.Client
}

func NewFiles(base string, transport http.RoundTripper) (*Files, error) {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return nil, errors.New("invalid sandbox endpoint")
	}
	if transport == nil {
		transport = http.DefaultTransport.(*http.Transport).Clone()
	}
	return &Files{base: strings.TrimRight(base, "/"), http: &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func filePath(path string) (string, error) {
	if strings.HasPrefix(path, "/") || strings.ContainsRune(path, 0) {
		return "", errors.New("file path must be relative")
	}
	parts := strings.Split(path, "/")
	for i, p := range parts {
		if p == ".." {
			return "", errors.New("parent paths are forbidden")
		}
		parts[i] = url.PathEscape(p)
	}
	return "/v1/files/" + strings.Join(parts, "/"), nil
}
func (f *Files) request(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, f.base+path, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/octet-stream")
	}
	res, err := f.http.Do(req)
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		res.Body.Close()
		return nil, &HTTPError{Status: res.StatusCode}
	}
	return res, nil
}

type HTTPError struct{ Status int }

func (e *HTTPError) Error() string { return fmt.Sprintf("sandbox runtime HTTP %d", e.Status) }

// Read streams files without buffering; directories return the native JSON listing.
func (f *Files) Read(ctx context.Context, path string) (io.ReadCloser, error) {
	p, err := filePath(path)
	if err != nil {
		return nil, err
	}
	res, err := f.request(ctx, "GET", p, nil)
	if err != nil {
		return nil, err
	}
	return res.Body, nil
}
func (f *Files) Stat(ctx context.Context, path string) (http.Header, error) {
	p, err := filePath(path)
	if err != nil {
		return nil, err
	}
	res, err := f.request(ctx, "HEAD", p, nil)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	return res.Header, nil
}
func (f *Files) Write(ctx context.Context, path string, body io.Reader, mode uint32) error {
	if mode > 0777 {
		return errors.New("invalid file mode")
	}
	p, err := filePath(path)
	if err != nil {
		return err
	}
	res, err := f.request(ctx, "PUT", p+fmt.Sprintf("?mode=0%03o", mode), body)
	if err != nil {
		return err
	}
	res.Body.Close()
	return nil
}
func (f *Files) Delete(ctx context.Context, path string, recursive bool) error {
	p, err := filePath(path)
	if err != nil {
		return err
	}
	if recursive {
		p += "?recursive=true"
	}
	res, err := f.request(ctx, "DELETE", p, nil)
	if err != nil {
		return err
	}
	res.Body.Close()
	return nil
}
func (f *Files) Metadata(ctx context.Context) (map[string]string, error) {
	res, err := f.request(ctx, "GET", "/v1/metadata", nil)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	var out struct {
		Env map[string]string `json:"env"`
	}
	err = json.NewDecoder(io.LimitReader(res.Body, 65536)).Decode(&out)
	return out.Env, err
}
func (f *Files) Ready(ctx context.Context) error {
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		res, err := f.request(ctx, "GET", "/v1/health", nil)
		if err == nil {
			res.Body.Close()
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
		}
	}
}

// List decodes the daemon's directory listing; Read remains available for streaming large files.
func (f *Files) List(ctx context.Context, path string) (DirectoryListing, error) {
	body, err := f.Read(ctx, path)
	if err != nil {
		return DirectoryListing{}, err
	}
	defer body.Close()
	var out DirectoryListing
	data, err := io.ReadAll(io.LimitReader(body, (8<<20)+1))
	if err != nil {
		return out, err
	}
	if len(data) > 8<<20 {
		return out, errors.New("directory listing exceeds limit")
	}
	err = json.Unmarshal(data, &out)
	return out, err
}

type DirectoryListing struct {
	Path    string      `json:"path"`
	Entries []FileEntry `json:"entries"`
}
type FileEntry struct {
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	Type       string `json:"type"`
	ModifiedAt string `json:"modified_at"`
	Mode       string `json:"mode"`
}

func (f *Files) Exists(ctx context.Context, path string) (bool, error) {
	_, err := f.Stat(ctx, path)
	var httpErr *HTTPError
	if errors.As(err, &httpErr) && httpErr.Status == 404 {
		return false, nil
	}
	return err == nil, err
}
