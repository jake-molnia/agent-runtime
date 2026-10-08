package command

import (
	"bytes"
	"context"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"io"
	"os"

	"github.com/jake-molnia/agent-runtime/githubreview"
	"github.com/jake-molnia/agent-runtime/vault"
	"gopkg.in/yaml.v3"
)

type githubConnectionConfig struct {
	GitHub struct {
		AppID       int64 `yaml:"app_id"`
		Credentials struct {
			Provider      string          `yaml:"provider"`
			PrivateKey    vault.Reference `yaml:"private_key_ref"`
			WebhookSecret vault.Reference `yaml:"webhook_secret_ref"`
		} `yaml:"credentials"`
	} `yaml:"github"`
	Vault vault.Config `yaml:"vault"`
}

type githubAccess struct {
	Client        *githubreview.Client
	Allowed       map[int64]int64
	WebhookSecret func(context.Context) ([]byte, error)
}

func githubConnection(ctx context.Context) (githubAccess, error) {
	var access githubAccess
	file, err := os.Open(env("GITHUB_CONNECTION_FILE", "/config/github.yaml"))
	if err != nil {
		return access, errors.New("GitHub connection configuration unavailable")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil || len(data) > 64<<10 {
		return access, errors.New("GitHub connection configuration exceeds limit or is unreadable")
	}
	var config githubConnectionConfig
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	if decoder.Decode(&config) != nil || decoder.Decode(new(any)) != io.EOF || config.GitHub.AppID <= 0 || config.GitHub.Credentials.Provider != "vault" {
		return access, errors.New("invalid GitHub connection configuration")
	}
	if config.GitHub.Credentials.PrivateKey.Validate() != nil || config.GitHub.Credentials.WebhookSecret.Validate() != nil {
		return access, errors.New("invalid GitHub Vault credential reference")
	}
	data, err = os.ReadFile(env("GITHUB_REPOSITORIES_FILE", "/config/github-repositories.json"))
	if err != nil {
		return access, errors.New("GitHub repository allowlist unavailable")
	}
	if err := strictJSON(data, &access.Allowed); err != nil {
		return access, errors.New("invalid GitHub repository allowlist")
	}
	reader, err := vault.New(config.Vault)
	if err != nil {
		return access, err
	}
	privateKey := func(ctx context.Context) (*rsa.PrivateKey, error) {
		data, err := reader.Read(ctx, config.GitHub.Credentials.PrivateKey)
		if err != nil {
			return nil, errors.New("GitHub App Vault private key unavailable")
		}
		return parseGitHubKey(data)
	}
	access.WebhookSecret = func(ctx context.Context) ([]byte, error) {
		data, err := reader.Read(ctx, config.GitHub.Credentials.WebhookSecret)
		if err != nil || len(data) == 0 || len(data) > 1024 {
			return nil, errors.New("GitHub App Vault webhook secret unavailable")
		}
		return data, nil
	}
	access.Client, err = githubreview.NewClient(githubreview.ClientConfig{AppID: config.GitHub.AppID, PrivateKey: privateKey, Allowed: access.Allowed})
	if err != nil {
		return access, err
	}
	if _, err = privateKey(ctx); err != nil {
		return access, err
	}
	if _, err = access.WebhookSecret(ctx); err != nil {
		return access, err
	}
	return access, nil
}

func parseGitHubKey(data []byte) (*rsa.PrivateKey, error) {
	block, rest := pem.Decode(data)
	if block == nil || len(rest) != 0 {
		return nil, errors.New("invalid GitHub App PEM private key")
	}
	var key *rsa.PrivateKey
	switch block.Type {
	case "RSA PRIVATE KEY":
		parsed, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, errors.New("invalid GitHub App RSA private key")
		}
		key = parsed
	case "PRIVATE KEY":
		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, errors.New("invalid GitHub App private key")
		}
		var ok bool
		key, ok = parsed.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("GitHub App private key must be RSA")
		}
	default:
		return nil, errors.New("unsupported GitHub App private key encoding")
	}
	if key.N.BitLen() < 2048 || key.Validate() != nil {
		return nil, errors.New("invalid GitHub App private key")
	}
	return key, nil
}
