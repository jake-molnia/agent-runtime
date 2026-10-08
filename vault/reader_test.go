package vault

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func response(value any) string {
	data, _ := json.Marshal(map[string]any{"data": map[string]any{"data": map[string]any{"value": value}, "metadata": map[string]any{"version": 3, "destroyed": false, "deletion_time": ""}}})
	return string(data)
}

func tokenFile(t *testing.T, token string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(path, []byte(token), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReadsOnlyKV2AndRereadsExternalToken(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		index := calls.Add(1)
		if request.Method != http.MethodGet || request.URL.Path != "/v1/secret/data/github/app" || request.URL.RawQuery != "" || request.Header.Get("X-Vault-Namespace") != "team/production" || request.Header.Get("X-Vault-Request") != "true" {
			t.Errorf("invalid read request: %s %s %+v", request.Method, request.URL, request.Header)
		}
		if request.Header.Get("X-Vault-Token") != fmt.Sprintf("test-token-%d", index) {
			t.Error("external token not refreshed")
		}
		fmt.Fprint(writer, response(fmt.Sprintf("secret-%d\n", index)))
	}))
	defer server.Close()
	path := tokenFile(t, "test-token-1\n")
	reader, err := New(Config{Address: server.URL, Namespace: "team/production", Auth: "token-file", TokenFile: path})
	if err != nil {
		t.Fatal(err)
	}
	reference := Reference{Mount: "secret", Path: "github/app", Field: "value"}
	first, err := reader.Read(context.Background(), reference)
	if err != nil || string(first) != "secret-1\n" {
		t.Fatalf("secret bytes changed: %q %v", first, err)
	}
	if err := os.WriteFile(path, []byte("test-token-2"), 0600); err != nil {
		t.Fatal(err)
	}
	second, err := reader.Read(context.Background(), reference)
	if err != nil || string(second) != "secret-2\n" {
		t.Fatalf("secret or token cached: %q %v", second, err)
	}
	if calls.Load() != 2 {
		t.Fatal("unexpected Vault API calls")
	}
}

func TestAgentProxyDoesNotSendAuthenticationToken(t *testing.T) {
	t.Setenv("VAULT_TOKEN", "must-not-be-read")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-Vault-Token") != "" || request.Method != http.MethodGet || request.Header.Get("X-Vault-Request") != "true" {
			t.Error("proxy request supplied a token or a write")
		}
		fmt.Fprint(writer, response("retrieved"))
	}))
	defer server.Close()
	reader, err := New(Config{Address: server.URL, Auth: "proxy"})
	if err != nil {
		t.Fatal(err)
	}
	value, err := reader.Read(context.Background(), Reference{Mount: "secret", Path: "app", Field: "value"})
	if err != nil || string(value) != "retrieved" {
		t.Fatalf("proxy read failed: %q %v", value, err)
	}
}

func TestConfigurationAndReferencesFailClosed(t *testing.T) {
	for _, config := range []Config{
		{Address: "http://vault.example", Auth: "token-file", TokenFile: "/token"},
		{Address: "https://user:pass@vault.example", Auth: "token-file", TokenFile: "/token"},
		{Address: "https://vault.example/path", Auth: "token-file", TokenFile: "/token"},
		{Address: "https://vault.example?token=secret", Auth: "token-file", TokenFile: "/token"},
		{Address: "https://vault.example", Auth: "token-file"},
		{Address: "https://vault.example", Auth: "proxy"},
		{Address: "http://127.0.0.1:8200", Auth: "proxy", TokenFile: "/token"},
		{Address: "https://vault.example", Auth: "kubernetes"},
		{Address: "https://vault.example", Auth: "token-file", TokenFile: "/token", Namespace: "../team"},
	} {
		if _, err := New(config); err == nil {
			t.Fatalf("unsafe configuration accepted: %+v", config)
		}
	}
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	reader, _ := New(Config{Address: server.URL, Auth: "proxy"})
	for _, path := range []string{"", "../app", "/app", "app/..", "app//field", "app?query", "app#fragment", "app%2fother", "app\\other"} {
		if _, err := reader.Read(context.Background(), Reference{Mount: "secret", Path: path, Field: "value"}); err == nil {
			t.Fatalf("unsafe path accepted: %q", path)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("invalid references reached Vault")
	}
}

func TestReadFailureNeverExposesResponseOrFallsBack(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   string
	}{
		{"denied", 403, `{"errors":["secret-value-sentinel"]}`},
		{"missing", 404, `{"errors":["secret-value-sentinel"]}`},
		{"unavailable", 503, `secret-value-sentinel`},
		{"invalid JSON", 200, `secret-value-sentinel`},
		{"missing metadata", 200, `{"data":{"data":{"value":"secret-value-sentinel"}}}`},
		{"deleted", 200, strings.Replace(response("secret-value-sentinel"), `"deletion_time":""`, `"deletion_time":"2026-10-08T00:00:00Z"`, 1)},
		{"destroyed", 200, strings.Replace(response("secret-value-sentinel"), `"destroyed":false`, `"destroyed":true`, 1)},
		{"non-string", 200, response(42)},
		{"null", 200, response(nil)},
		{"empty", 200, response("")},
		{"trailing JSON", 200, response("secret-value-sentinel") + ` {}`},
		{"oversized", 200, strings.Repeat("s", maxResponseBytes+1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				if request.Method != http.MethodGet {
					t.Error("Vault mutation attempted")
				}
				writer.WriteHeader(test.status)
				fmt.Fprint(writer, test.body)
			}))
			defer server.Close()
			reader, _ := New(Config{Address: server.URL, Auth: "proxy"})
			value, err := reader.Read(context.Background(), Reference{Mount: "secret", Path: "app", Field: "value"})
			if err == nil || len(value) != 0 || strings.Contains(err.Error(), "secret-value-sentinel") {
				t.Fatalf("failure leaked or succeeded: %q %v", value, err)
			}
		})
	}
}

func TestReaderRefusesRedirects(t *testing.T) {
	var forwarded atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { forwarded.Add(1) }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Redirect(writer, request, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	reader, _ := New(Config{Address: server.URL, Auth: "token-file", TokenFile: tokenFile(t, "private-token")})
	if _, err := reader.Read(context.Background(), Reference{Mount: "secret", Path: "app", Field: "value"}); err == nil || forwarded.Load() != 0 {
		t.Fatalf("redirect followed: %v", err)
	}
}

func TestReaderVerifiesTLSAndSupportsExternalCA(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) { fmt.Fprint(writer, response("retrieved")) }))
	defer server.Close()
	config := Config{Address: server.URL, Auth: "token-file", TokenFile: tokenFile(t, "test-token")}
	reader, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	reference := Reference{Mount: "secret", Path: "app", Field: "value"}
	if _, err := reader.Read(context.Background(), reference); err == nil {
		t.Fatal("untrusted TLS certificate accepted")
	}
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	config.CAFile = filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(config.CAFile, certificate, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := x509.ParseCertificate(server.Certificate().Raw); err != nil {
		t.Fatal(err)
	}
	reader, err = New(config)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := reader.Read(context.Background(), reference); err != nil {
		t.Fatal(err)
	}
}

func TestReadCancellationAndMissingToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) { <-request.Context().Done() }))
	defer server.Close()
	reader, _ := New(Config{Address: server.URL, Auth: "proxy"})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := reader.Read(ctx, Reference{Mount: "secret", Path: "app", Field: "value"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("cancellation lost: %v", err)
	}
	reader, _ = New(Config{Address: server.URL, Auth: "token-file", TokenFile: filepath.Join(t.TempDir(), "missing")})
	if _, err := reader.Read(context.Background(), Reference{Mount: "secret", Path: "app", Field: "value"}); err == nil {
		t.Fatal("missing external token accepted")
	}
}

type failedTransport struct{}

func (failedTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("private-secret-sentinel")
}

func TestTransportFailureIsSanitized(t *testing.T) {
	reader, err := New(Config{Address: "http://127.0.0.1:8200", Auth: "proxy"})
	if err != nil {
		t.Fatal(err)
	}
	reader.http.Transport = failedTransport{}
	if _, err := reader.Read(context.Background(), Reference{Mount: "secret", Path: "app", Field: "value"}); err == nil || strings.Contains(err.Error(), "private-secret-sentinel") {
		t.Fatalf("transport failure leaked: %v", err)
	}
}
