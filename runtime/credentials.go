package runtime

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type runtimeCredential struct {
	Type string `json:"type"`
	Key  string `json:"key"`
}

var runtimeProviderID = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)

func writeRuntimeConfig(home string, raw json.RawMessage) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return errors.New("runtime configuration must be an object")
	}
	config := map[string]json.RawMessage{}
	for decoder.More() {
		fieldToken, err := decoder.Token()
		if err != nil {
			return errors.New("invalid runtime configuration field")
		}
		field := fieldToken.(string)
		if _, exists := config[field]; exists {
			return errors.New("duplicate runtime configuration field")
		}
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return fmt.Errorf("invalid runtime configuration: %w", err)
		}
		config[field] = value
	}
	if closing, err := decoder.Token(); err != nil || closing != json.Delim('}') {
		return errors.New("invalid runtime configuration object")
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return errors.New("expected one runtime configuration object")
	}
	auth, hasAuth := config["auth"]
	var credentials map[string]runtimeCredential
	if hasAuth {
		var err error
		credentials, err = decodeRuntimeAuth(auth)
		if err != nil {
			return err
		}
		delete(config, "auth")
	}
	data, err := json.Marshal(config)
	if err != nil {
		return err
	}
	info, err := os.Lstat(home)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("runtime home must be a directory, not a symlink")
	}
	root, err := os.OpenRoot(home)
	if err != nil {
		return err
	}
	defer root.Close()
	if err := root.Chmod(".", 0700); err != nil {
		return err
	}
	if hasAuth {
		for _, directory := range []string{"data", filepath.Join("data", "opencode")} {
			if err := root.Mkdir(directory, 0700); err != nil && !errors.Is(err, fs.ErrExist) {
				return err
			}
			info, err := root.Lstat(directory)
			if err != nil {
				return err
			}
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
				return errors.New("runtime credential parents must be directories, not symlinks")
			}
			if err := root.Chmod(directory, 0700); err != nil {
				return err
			}
		}
		authData, err := json.Marshal(credentials)
		if err != nil {
			return err
		}
		if err := writePrivateRuntimeFile(root, filepath.Join("data", "opencode", "auth.json"), authData); err != nil {
			return err
		}
	}
	return writePrivateRuntimeFile(root, "opencode.json", data)
}

func decodeRuntimeAuth(raw json.RawMessage) (map[string]runtimeCredential, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	opening, err := decoder.Token()
	if err != nil || opening != json.Delim('{') {
		return nil, errors.New("runtime auth must be a provider object")
	}
	credentials := map[string]runtimeCredential{}
	for decoder.More() {
		providerToken, err := decoder.Token()
		if err != nil {
			return nil, errors.New("invalid runtime auth provider")
		}
		provider, ok := providerToken.(string)
		if !ok || !runtimeProviderID.MatchString(provider) {
			return nil, errors.New("runtime auth provider ID is invalid")
		}
		if _, exists := credentials[provider]; exists {
			return nil, errors.New("duplicate runtime auth provider")
		}
		opening, err := decoder.Token()
		if err != nil || opening != json.Delim('{') {
			return nil, errors.New("runtime provider credential must be an object")
		}
		var credential runtimeCredential
		seen := map[string]bool{}
		for decoder.More() {
			fieldToken, err := decoder.Token()
			if err != nil {
				return nil, errors.New("invalid runtime provider credential")
			}
			field, ok := fieldToken.(string)
			if !ok || (field != "type" && field != "key") || seen[field] {
				return nil, errors.New("unsupported or duplicate runtime credential field")
			}
			seen[field] = true
			var value string
			if err := decoder.Decode(&value); err != nil {
				return nil, errors.New("runtime credential fields must be strings")
			}
			if field == "type" {
				credential.Type = value
			} else {
				credential.Key = value
			}
		}
		if closing, err := decoder.Token(); err != nil || closing != json.Delim('}') {
			return nil, errors.New("invalid runtime provider credential object")
		}
		if credential.Type != "api" || strings.TrimSpace(credential.Key) == "" {
			return nil, errors.New("runtime credentials require type api and a non-empty key")
		}
		credentials[provider] = credential
	}
	if closing, err := decoder.Token(); err != nil || closing != json.Delim('}') {
		return nil, errors.New("invalid runtime auth object")
	}
	return credentials, nil
}

func writePrivateRuntimeFile(root *os.Root, name string, data []byte) error {
	info, err := root.Lstat(name)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if err == nil && !info.Mode().IsRegular() {
		return errors.New("runtime configuration files must be regular files, not symlinks")
	}
	temporary := filepath.Join(filepath.Dir(name), ".runtime-config-"+rand.Text())
	file, err := root.OpenFile(temporary, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer root.Remove(temporary)
	defer file.Close()
	if err := file.Chmod(0600); err != nil {
		return err
	}
	if _, err := file.Write(data); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return root.Rename(temporary, name)
}
