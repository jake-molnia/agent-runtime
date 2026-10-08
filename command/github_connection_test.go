package command

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jake-molnia/agent-runtime/githubreview"
)

func githubYAML(address string) string {
	return fmt.Sprintf(`github:
  app_id: 19
  credentials:
    provider: vault
    private_key_ref: {mount: secret, path: github/reviewer, field: private_key}
    webhook_secret_ref: {mount: secret, path: github/reviewer, field: webhook_secret}
vault:
  address: %q
  auth: proxy
`, address)
}

func githubFiles(t *testing.T, config string) string {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "github.yaml")
	if err := os.WriteFile(path, []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GITHUB_CONNECTION_FILE", path)
	allowlist := filepath.Join(root, "repositories.json")
	if err := os.WriteFile(allowlist, []byte(`{"11":7}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GITHUB_REPOSITORIES_FILE", allowlist)
	return root
}

func TestGitHubConnectionReadsVaultAndDoesNotPersistCredentials(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	privateKey := string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}))
	var calls atomic.Int32
	var webhook atomic.Value
	webhook.Store("exact-webhook-secret\n")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		if request.Method != http.MethodGet || request.URL.Path != "/v1/secret/data/github/reviewer" || request.Header.Get("X-Vault-Token") != "" {
			t.Errorf("unexpected Vault access: %s %s", request.Method, request.URL)
		}
		json.NewEncoder(writer).Encode(map[string]any{"data": map[string]any{"data": map[string]string{"private_key": privateKey, "webhook_secret": webhook.Load().(string)}, "metadata": map[string]any{"version": 1}}})
	}))
	defer server.Close()
	root := githubFiles(t, githubYAML(server.URL))
	access, err := githubConnection(context.Background())
	if err != nil || access.Client == nil || access.Allowed[11] != 7 || calls.Load() != 2 {
		t.Fatalf("connection initialization failed: %+v %v calls=%d", access.Allowed, err, calls.Load())
	}
	value, err := access.WebhookSecret(context.Background())
	if err != nil || string(value) != "exact-webhook-secret\n" {
		t.Fatalf("webhook bytes changed: %q %v", value, err)
	}
	webhook.Store("rotated-webhook-secret")
	value, err = access.WebhookSecret(context.Background())
	if err != nil || string(value) != "rotated-webhook-secret" {
		t.Fatalf("secret was cached: %q %v", value, err)
	}
	files, err := os.ReadDir(root)
	if err != nil || len(files) != 2 {
		t.Fatalf("unexpected credential files written: %v %v", files, err)
	}
	for _, file := range files {
		data, err := os.ReadFile(filepath.Join(root, file.Name()))
		if err != nil || strings.Contains(string(data), privateKey) || strings.Contains(string(data), "exact-webhook-secret") || strings.Contains(string(data), "rotated-webhook-secret") {
			t.Fatal("credential persisted to configuration")
		}
	}
	handler, err := githubreview.NewWebhookHandler(githubreview.WebhookConfig{Secret: access.WebhookSecret, Actions: []string{"opened"}, Allowed: access.Allowed, Submit: func(context.Context, githubreview.Input) error { return nil }})
	if err != nil || handler == nil {
		t.Fatalf("retrieved source could not be wired into ingress: %v", err)
	}
}

func TestGitHubConnectionRejectsSecretValuesAndUnsafeConfig(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer server.Close()
	base := githubYAML(server.URL)
	for name, config := range map[string]string{
		"inline token":       base + "  token: private-secret-sentinel\n",
		"inline key":         strings.Replace(base, "    provider: vault", "    provider: vault\n    private_key: private-secret-sentinel", 1),
		"wrong provider":     strings.Replace(base, "provider: vault", "provider: files", 1),
		"invalid ID":         strings.Replace(base, "app_id: 19", "app_id: -1", 1),
		"path escape":        strings.Replace(base, "path: github/reviewer", "path: ../reviewer", 1),
		"duplicate YAML":     base + "vault: {}\n",
		"multiple documents": base + "---\ngithub: {}\n",
		"oversized":          strings.Repeat("s", (64<<10)+1),
	} {
		t.Run(name, func(t *testing.T) {
			githubFiles(t, config)
			if _, err := githubConnection(context.Background()); err == nil || strings.Contains(err.Error(), "private-secret-sentinel") {
				t.Fatalf("invalid config accepted or leaked: %v", err)
			}
		})
	}
	if calls.Load() != 0 {
		t.Fatal("unsafe configuration reached Vault")
	}
}

func TestGitHubConnectionNoLocalSecretFallback(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusForbidden)
		fmt.Fprint(writer, `{"errors":["private-secret-sentinel"]}`)
	}))
	defer server.Close()
	root := githubFiles(t, githubYAML(server.URL))
	t.Setenv("GITHUB_APP_ID", "19")
	t.Setenv("GITHUB_APP_KEY_FILE", filepath.Join(root, "must-not-be-read.pem"))
	t.Setenv("GITHUB_WEBHOOK_SECRET_FILE", filepath.Join(root, "must-not-be-read"))
	if _, err := githubConnection(context.Background()); err == nil || strings.Contains(err.Error(), "private-secret-sentinel") {
		t.Fatalf("Vault denial did not fail closed: %v", err)
	}
	t.Setenv("GITHUB_CONNECTION_FILE", filepath.Join(root, "missing.yaml"))
	if _, err := githubConnection(context.Background()); err == nil {
		t.Fatal("legacy environment accepted without Vault configuration")
	}
}

func TestParseGitHubKeyEncodingsAndFailures(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	pkcs8, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	for _, block := range []*pem.Block{{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}, {Type: "PRIVATE KEY", Bytes: pkcs8}} {
		parsed, err := parseGitHubKey(pem.EncodeToMemory(block))
		if err != nil || parsed.N.Cmp(key.N) != 0 {
			t.Fatalf("key encoding rejected: %v", err)
		}
	}
	for _, data := range [][]byte{[]byte("private-secret-sentinel"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: pkcs8}), append(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pkcs8}), []byte("trailing data")...)} {
		if _, err := parseGitHubKey(data); err == nil || strings.Contains(err.Error(), "private-secret-sentinel") {
			t.Fatalf("invalid key accepted or leaked: %v", err)
		}
	}
	weak, err := rsa.GenerateKey(rand.Reader, 1024)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseGitHubKey(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(weak)})); err == nil {
		t.Fatal("weak RSA key accepted")
	}
}

func TestGitHubVaultIntegration(t *testing.T) {
	config := os.Getenv("GITHUB_VAULT_TEST_CONFIG")
	repositories := os.Getenv("GITHUB_VAULT_TEST_REPOSITORIES_FILE")
	if config == "" || repositories == "" {
		t.Skip("requires an externally provisioned disposable Vault fixture")
	}
	t.Setenv("GITHUB_CONNECTION_FILE", config)
	t.Setenv("GITHUB_REPOSITORIES_FILE", repositories)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	access, err := githubConnection(ctx)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := access.WebhookSecret(ctx)
	if err != nil || string(secret) != "integration-webhook-secret" {
		t.Fatal("Vault fixture webhook secret missing or incorrect")
	}
	submitted := false
	handler, err := githubreview.NewWebhookHandler(githubreview.WebhookConfig{Secret: access.WebhookSecret, Allowed: access.Allowed, Actions: []string{"opened"}, Submit: func(context.Context, githubreview.Input) error { submitted = true; return nil }})
	if err != nil {
		t.Fatal(err)
	}
	body := fmt.Sprintf(`{"action":"opened","number":9,"installation":{"id":7},"repository":{"id":11,"full_name":"owner/repo"},"pull_request":{"number":9,"base":{"sha":%q,"repo":{"id":11,"full_name":"owner/repo"}},"head":{"sha":%q}}}`, strings.Repeat("a", 40), strings.Repeat("b", 40))
	request := httptest.NewRequest(http.MethodPost, "/webhooks/github-pr-review", strings.NewReader(body)).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-GitHub-Event", "pull_request")
	request.Header.Set("X-GitHub-Delivery", "integration-delivery")
	mac := hmac.New(sha256.New, []byte("integration-webhook-secret"))
	mac.Write([]byte(body))
	request.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusAccepted || !submitted {
		t.Fatalf("Vault-backed webhook not accepted: %d", recorder.Code)
	}
}
