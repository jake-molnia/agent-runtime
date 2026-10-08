package vault

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
	"unicode/utf8"
)

const maxResponseBytes = 1 << 20

type Config struct {
	Address   string `yaml:"address"`
	Namespace string `yaml:"namespace,omitempty"`
	Auth      string `yaml:"auth"`
	TokenFile string `yaml:"token_file,omitempty"`
	CAFile    string `yaml:"ca_file,omitempty"`
}

type Reference struct {
	Mount string `yaml:"mount"`
	Path  string `yaml:"path"`
	Field string `yaml:"field"`
}

func validPath(value string) bool {
	if value == "" || len(value) > 1024 {
		return false
	}
	for _, segment := range strings.Split(value, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return false
		}
		for _, character := range segment {
			if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || strings.ContainsRune("_.-", character)) {
				return false
			}
		}
	}
	return true
}

func (reference Reference) Validate() error {
	if !validPath(reference.Mount) || !validPath(reference.Path) || !validPath(reference.Field) || strings.Contains(reference.Field, "/") {
		return errors.New("invalid Vault KV v2 reference")
	}
	return nil
}

type Reader struct {
	address   string
	namespace string
	tokenFile string
	http      *http.Client
}

func New(config Config) (*Reader, error) {
	address, err := url.Parse(config.Address)
	if err != nil || address.Hostname() == "" || address.User != nil || address.RawQuery != "" || address.Fragment != "" || (address.Path != "" && address.Path != "/") {
		return nil, errors.New("invalid Vault address")
	}
	loopback := address.Hostname() == "localhost"
	if ip := net.ParseIP(address.Hostname()); ip != nil {
		loopback = ip.IsLoopback()
	}
	if address.Scheme != "https" && !(address.Scheme == "http" && loopback) {
		return nil, errors.New("Vault requires HTTPS except on loopback")
	}
	if config.Namespace != "" && !validPath(config.Namespace) {
		return nil, errors.New("invalid Vault namespace")
	}
	switch config.Auth {
	case "token-file":
		if config.TokenFile == "" {
			return nil, errors.New("Vault token-file authentication requires a token file")
		}
	case "proxy":
		if config.TokenFile != "" || !loopback {
			return nil, errors.New("Vault proxy authentication requires a loopback agent and no token file")
		}
	default:
		return nil, errors.New("unsupported Vault authentication mode")
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	if config.CAFile != "" {
		data, err := readFile(config.CAFile, maxResponseBytes)
		if err != nil {
			return nil, errors.New("Vault CA file unavailable")
		}
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			return nil, errors.New("Vault system CA pool unavailable")
		}
		if !pool.AppendCertsFromPEM(data) {
			return nil, errors.New("invalid Vault CA file")
		}
		transport.TLSClientConfig.RootCAs = pool
	}
	return &Reader{
		address: strings.TrimRight(address.String(), "/"), namespace: config.Namespace, tokenFile: config.TokenFile,
		http: &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }},
	}, nil
}

func readFile(path string, limit int64) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err == nil && int64(len(data)) > limit {
		err = errors.New("file exceeds limit")
	}
	return data, err
}

func (reader *Reader) Read(ctx context.Context, reference Reference) ([]byte, error) {
	if err := reference.Validate(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	endpoint := reader.address + "/v1/" + reference.Mount + "/data/" + reference.Path
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, errors.New("invalid Vault read request")
	}
	request.Header.Set("X-Vault-Request", "true")
	if reader.namespace != "" {
		request.Header.Set("X-Vault-Namespace", reader.namespace)
	}
	if reader.tokenFile != "" {
		data, err := readFile(reader.tokenFile, 16<<10)
		if err != nil {
			return nil, errors.New("Vault authentication token unavailable")
		}
		token := strings.TrimSpace(string(data))
		if token == "" {
			return nil, errors.New("invalid Vault authentication token")
		}
		for _, character := range token {
			if character < 33 || character > 126 {
				return nil, errors.New("invalid Vault authentication token")
			}
		}
		request.Header.Set("X-Vault-Token", token)
	}
	response, err := reader.http.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("Vault read transport failed")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return nil, errors.New("Vault secret read rejected")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil || len(data) > maxResponseBytes || !utf8.Valid(data) {
		return nil, errors.New("invalid or oversized Vault response")
	}
	var envelope struct {
		Data struct {
			Data     map[string]json.RawMessage `json:"data"`
			Metadata struct {
				Version      int    `json:"version"`
				Destroyed    bool   `json:"destroyed"`
				DeletionTime string `json:"deletion_time"`
			} `json:"metadata"`
		} `json:"data"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	if decoder.Decode(&envelope) != nil || decoder.Decode(new(any)) != io.EOF || envelope.Data.Metadata.Version < 1 || envelope.Data.Metadata.Destroyed || envelope.Data.Metadata.DeletionTime != "" {
		return nil, errors.New("invalid or inactive Vault KV v2 secret")
	}
	var value string
	if json.Unmarshal(envelope.Data.Data[reference.Field], &value) != nil || value == "" {
		return nil, errors.New("Vault secret string field unavailable")
	}
	return []byte(value), nil
}
