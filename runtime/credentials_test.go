package runtime

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func readCredentialFixture(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func assertCredentialPermissions(t *testing.T, path string, expected os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != expected {
		t.Fatalf("%s permissions = %o, want %o", path, info.Mode().Perm(), expected)
	}
}

func TestWriteRuntimeConfigExtractsAuthToV2BootstrapPath(t *testing.T) {
	home := t.TempDir()
	raw := json.RawMessage(`{"agents":{"authored":{"system":"Review","mode":"primary","permissions":[{"action":"*","resource":"*","effect":"deny"}]}},"providers":{"openai":{"settings":{"baseURL":"https://example.invalid"}}},"auth":{"openai":{"type":"api","key":"openai-private-key"},"anthropic":{"type":"api","key":"anthropic-private-key"}}}`)
	if err := writeRuntimeConfig(home, raw); err != nil {
		t.Fatal(err)
	}
	authPath := filepath.Join(home, "data", "opencode", "auth.json")
	var auth map[string]runtimeCredential
	if err := json.Unmarshal(readCredentialFixture(t, authPath), &auth); err != nil {
		t.Fatal(err)
	}
	expected := map[string]runtimeCredential{
		"openai":    {Type: "api", Key: "openai-private-key"},
		"anthropic": {Type: "api", Key: "anthropic-private-key"},
	}
	if !reflect.DeepEqual(auth, expected) {
		t.Fatalf("auth store = %#v", auth)
	}
	configBytes := readCredentialFixture(t, filepath.Join(home, "opencode.json"))
	var config, input map[string]json.RawMessage
	if err := json.Unmarshal(configBytes, &config); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &input); err != nil {
		t.Fatal(err)
	}
	delete(input, "auth")
	if !reflect.DeepEqual(config, input) {
		t.Fatalf("OpenCode settings changed: %s", configBytes)
	}
	if strings.Contains(string(configBytes), "private-key") {
		t.Fatal("provider credentials leaked into OpenCode config")
	}
	for _, parent := range []string{home, filepath.Join(home, "data"), filepath.Join(home, "data", "opencode")} {
		assertCredentialPermissions(t, parent, 0700)
	}
	assertCredentialPermissions(t, authPath, 0600)
	assertCredentialPermissions(t, filepath.Join(home, "opencode.json"), 0600)
	for _, wrongPath := range []string{"auth.json", "data/auth.json", "config/opencode/auth.json", ".local/share/opencode/auth.json"} {
		if _, err := os.Stat(filepath.Join(home, wrongPath)); !os.IsNotExist(err) {
			t.Fatalf("unexpected auth path %s: %v", wrongPath, err)
		}
	}
}

func TestWriteRuntimeConfigWithoutAuth(t *testing.T) {
	home := t.TempDir()
	raw := json.RawMessage(`{"permissions":[{"action":"*","resource":"*","effect":"deny"}],"model":"openai/test"}`)
	if err := writeRuntimeConfig(home, raw); err != nil {
		t.Fatal(err)
	}
	var got, want any
	if err := json.Unmarshal(readCredentialFixture(t, filepath.Join(home, "opencode.json")), &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("config without auth changed")
	}
	if _, err := os.Stat(filepath.Join(home, "data", "opencode", "auth.json")); !os.IsNotExist(err) {
		t.Fatalf("unexpected auth file: %v", err)
	}
	assertCredentialPermissions(t, filepath.Join(home, "opencode.json"), 0600)
}

func TestWriteRuntimeConfigRejectsInvalidCredentialsBeforeWriting(t *testing.T) {
	for name, raw := range map[string]string{
		"invalid JSON":              `{`,
		"null config":               `null`,
		"array config":              `[]`,
		"trailing JSON":             `{} {}`,
		"null auth":                 `{"auth":null}`,
		"array auth":                `{"auth":[]}`,
		"string auth":               `{"auth":"private-key"}`,
		"empty provider":            `{"auth":{"":{"type":"api","key":"private-key"}}}`,
		"blank provider":            `{"auth":{" ":{"type":"api","key":"private-key"}}}`,
		"padded provider":           `{"auth":{" openai ":{"type":"api","key":"private-key"}}}`,
		"provider traversal":        `{"auth":{"../openai":{"type":"api","key":"private-key"}}}`,
		"null credential":           `{"auth":{"openai":null}}`,
		"array credential":          `{"auth":{"openai":[]}}`,
		"missing type":              `{"auth":{"openai":{"key":"private-key"}}}`,
		"null type":                 `{"auth":{"openai":{"type":null,"key":"private-key"}}}`,
		"number type":               `{"auth":{"openai":{"type":1,"key":"private-key"}}}`,
		"OAuth type":                `{"auth":{"openai":{"type":"oauth","key":"private-key"}}}`,
		"V2 internal key type":      `{"auth":{"openai":{"type":"key","key":"private-key"}}}`,
		"wellknown type":            `{"auth":{"openai":{"type":"wellknown","key":"private-key"}}}`,
		"missing key":               `{"auth":{"openai":{"type":"api"}}}`,
		"empty key":                 `{"auth":{"openai":{"type":"api","key":""}}}`,
		"whitespace key":            `{"auth":{"openai":{"type":"api","key":" \n\t "}}}`,
		"null key":                  `{"auth":{"openai":{"type":"api","key":null}}}`,
		"number key":                `{"auth":{"openai":{"type":"api","key":123}}}`,
		"unknown field":             `{"auth":{"openai":{"type":"api","key":"private-key","token":"private-key"}}}`,
		"metadata outside contract": `{"auth":{"openai":{"type":"api","key":"private-key","metadata":{}}}}`,
		"duplicate provider":        `{"auth":{"openai":{"type":"api","key":"private-key"},"openai":{"type":"api","key":"other-key"}}}`,
		"duplicate auth":            `{"auth":{"openai":{"type":"api","key":"private-key"}},"auth":{"openai":{"type":"api","key":"other-key"}}}`,
		"escaped duplicate auth":    `{"auth":{"openai":{"type":"api","key":"private-key"}},"\u0061uth":{"openai":{"type":"api","key":"other-key"}}}`,
		"duplicate key":             `{"auth":{"openai":{"type":"api","key":"private-key","key":"other-key"}}}`,
		"duplicate type":            `{"auth":{"openai":{"type":"api","type":"api","key":"private-key"}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			err := writeRuntimeConfig(home, json.RawMessage(raw))
			if err == nil {
				t.Fatal("invalid credentials accepted")
			}
			if strings.Contains(err.Error(), "private-key") {
				t.Fatalf("error leaks credential: %v", err)
			}
			entries, err := os.ReadDir(home)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 0 {
				t.Fatal("invalid credentials caused filesystem writes")
			}
		})
	}
}

func TestWriteRuntimeConfigUsesNoInheritedCredentialsOrXDGPaths(t *testing.T) {
	outside := t.TempDir()
	for _, name := range []string{"HOME", "XDG_DATA_HOME", "XDG_CONFIG_HOME", "OPENCODE_AUTH_CONTENT", "OPENAI_API_KEY", "ANTHROPIC_API_KEY"} {
		t.Setenv(name, outside+"inherited-secret")
	}
	home := t.TempDir()
	if err := writeRuntimeConfig(home, json.RawMessage(`{"auth":{"openai":{"type":"api","key":"provided-key"}}}`)); err != nil {
		t.Fatal(err)
	}
	auth := readCredentialFixture(t, filepath.Join(home, "data", "opencode", "auth.json"))
	config := readCredentialFixture(t, filepath.Join(home, "opencode.json"))
	if strings.Contains(string(auth)+string(config), "inherited-secret") {
		t.Fatal("inherited credentials used")
	}
	entries, err := os.ReadDir(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatal("inherited XDG directory used")
	}
	if os.Getenv("OPENAI_API_KEY") != outside+"inherited-secret" {
		t.Fatal("helper changed process environment")
	}
}

func TestWriteRuntimeConfigTightensExistingPermissionsAndReplacesAuth(t *testing.T) {
	home := t.TempDir()
	parent := filepath.Join(home, "data", "opencode")
	if err := os.MkdirAll(parent, 0755); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{home, filepath.Join(home, "data"), parent} {
		if err := os.Chmod(path, 0777); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{filepath.Join(parent, "auth.json"), filepath.Join(home, "opencode.json")} {
		if err := os.WriteFile(path, []byte(`{"stale":"old-key"}`), 0644); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeRuntimeConfig(home, json.RawMessage(`{"auth":{"openai":{"type":"api","key":"new-key"}}}`)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(readCredentialFixture(t, filepath.Join(parent, "auth.json"))), "old-key") {
		t.Fatal("old credential retained")
	}
	for _, path := range []string{home, filepath.Join(home, "data"), parent} {
		assertCredentialPermissions(t, path, 0700)
	}
	for _, path := range []string{filepath.Join(parent, "auth.json"), filepath.Join(home, "opencode.json")} {
		assertCredentialPermissions(t, path, 0600)
	}
	if err := writeRuntimeConfig(home, json.RawMessage(`{"auth":{}}`)); err != nil {
		t.Fatal(err)
	}
	if string(readCredentialFixture(t, filepath.Join(parent, "auth.json"))) != "{}" {
		t.Fatal("empty auth did not replace prior credentials")
	}
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary files remain: %v %v", entries, err)
	}
}

func TestWriteRuntimeConfigRejectsSymlinkTargets(t *testing.T) {
	for _, relative := range []string{"data", "data/opencode", "data/opencode/auth.json", "opencode.json"} {
		t.Run(relative, func(t *testing.T) {
			home, outside := t.TempDir(), t.TempDir()
			path := filepath.Join(home, relative)
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(outside, path); err != nil {
				t.Fatal(err)
			}
			if err := writeRuntimeConfig(home, json.RawMessage(`{"auth":{"openai":{"type":"api","key":"provided-key"}}}`)); err == nil {
				t.Fatal("symlink accepted")
			}
			entries, err := os.ReadDir(outside)
			if err != nil || len(entries) != 0 {
				t.Fatalf("writes escaped runtime home: %v %v", entries, err)
			}
		})
	}
	t.Run("home", func(t *testing.T) {
		outside := t.TempDir()
		home := filepath.Join(t.TempDir(), "home")
		if err := os.Symlink(outside, home); err != nil {
			t.Fatal(err)
		}
		if err := writeRuntimeConfig(home, json.RawMessage(`{}`)); err == nil {
			t.Fatal("symlink home accepted")
		}
	})
}
