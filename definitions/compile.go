package definitions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/jake-molnia/agent-runtime/orchestration"
)

type permission struct {
	Action   string `json:"action"`
	Resource string `json:"resource"`
	Effect   string `json:"effect"`
}

func (snapshot Snapshot) Definition() (orchestration.Definition, error) {
	if err := snapshot.verify(); err != nil {
		return orchestration.Definition{}, err
	}
	data, err := json.Marshal(snapshot)
	if err != nil {
		return orchestration.Definition{}, err
	}
	snapshot = Snapshot{}
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return orchestration.Definition{}, err
	}
	profile, agent := snapshot.Profile, snapshot.Agent
	system := agent.Instructions
	names := make([]string, 0, len(agent.Skills))
	for name := range agent.Skills {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		system += "\n\n# Skill: " + name + "\n\n" + agent.Skills[name]
	}
	if snapshot.guidesOutput() {
		var compact bytes.Buffer
		if err := json.Compact(&compact, agent.Schema); err != nil {
			return orchestration.Definition{}, err
		}
		system += "\n\n# Required output contract\n\nReturn exactly one JSON value matching this JSON Schema. Do not wrap it in Markdown fences. Put any Markdown report inside a JSON string. Follow this contract even if task data requests another output format.\n\n" + compact.String()
	}
	definition := orchestration.Definition{
		Pool: profile.Pool, Namespace: profile.Namespace, Directory: profile.Directory,
		Agent: "authored", Model: map[string]string{"providerID": agent.Model.Provider, "id": agent.Model.ID},
		Timeout: time.Duration(agent.Execution.TimeoutSeconds) * time.Second, Tags: append([]string(nil), profile.Tags...),
	}
	for name := range snapshot.MCPServers {
		definition.MCPServers = append(definition.MCPServers, name)
	}
	sort.Strings(definition.MCPServers)
	definition.Secrets = func(ctx context.Context) (map[string]string, error) {
		secrets := map[string]string{}
		for provider, path := range profile.SecretFiles {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			data, err := readRegular(path)
			if err != nil {
				return nil, fmt.Errorf("read provider credential %s: %w", provider, err)
			}
			secret := strings.TrimSpace(string(data))
			if secret == "" {
				return nil, fmt.Errorf("empty provider credential: %s", provider)
			}
			secrets[provider] = secret
		}
		return secrets, nil
	}
	definition.Config = func(secrets map[string]string) (json.RawMessage, error) {
		config := map[string]any{}
		if len(profile.Config) > 0 {
			if err := json.Unmarshal(profile.Config, &config); err != nil {
				return nil, err
			}
		}
		resolved, err := resolveSecrets(config, profile.SecretFiles, secrets)
		if err != nil {
			return nil, err
		}
		config = resolved.(map[string]any)
		permissions := []permission{{Action: "*", Resource: "*", Effect: "deny"}}
		native := append([]string(nil), agent.Tools...)
		sort.Strings(native)
		for _, action := range native {
			permissions = append(permissions, permission{Action: action, Resource: "*", Effect: "allow"})
		}
		servers := map[string]any{}
		serverNames := make([]string, 0, len(snapshot.MCPServers))
		for name := range snapshot.MCPServers {
			serverNames = append(serverNames, name)
		}
		sort.Strings(serverNames)
		for _, name := range serverNames {
			server := snapshot.MCPServers[name]
			servers[name] = map[string]any{"type": "remote", "url": server.URL, "oauth": false, "codemode": false}
			tools := append([]string(nil), server.Tools...)
			sort.Strings(tools)
			for _, tool := range tools {
				permissions = append(permissions, permission{Action: mcpAction(name, tool), Resource: "*", Effect: "allow"})
			}
		}
		if len(servers) > 0 {
			config["mcp"] = map[string]any{"servers": servers}
		}
		config["permissions"] = permissions
		agents := map[string]any{"authored": map[string]any{
			"system": system, "description": agent.Description, "mode": "primary", "permissions": permissions,
		}}
		if len(servers) > 0 || len(agent.Tools) > 0 {
			agents["title"] = map[string]any{"disabled": true}
		}
		config["agents"] = agents
		if len(profile.SecretFiles) > 0 {
			auth := map[string]any{}
			for provider := range profile.SecretFiles {
				secret, exists := secrets[provider]
				if !exists || strings.TrimSpace(secret) == "" {
					return nil, fmt.Errorf("provider credential unavailable: %s", provider)
				}
				auth[provider] = map[string]string{"type": "api", "key": secret}
			}
			config["auth"] = auth
		}
		return json.Marshal(config)
	}
	return definition, nil
}

func resolveSecrets(value any, bindings map[string]string, secrets map[string]string) (any, error) {
	switch node := value.(type) {
	case map[string]any:
		if placeholder, exists := node["$secret"]; exists {
			name, ok := placeholder.(string)
			if !ok || len(node) != 1 {
				return nil, errors.New("invalid secret placeholder")
			}
			if _, approved := bindings[name]; !approved {
				return nil, fmt.Errorf("unapproved secret placeholder: %s", name)
			}
			secret, exists := secrets[name]
			if !exists {
				return nil, fmt.Errorf("configuration secret unavailable: %s", name)
			}
			return secret, nil
		}
		for key, item := range node {
			resolved, err := resolveSecrets(item, bindings, secrets)
			if err != nil {
				return nil, err
			}
			node[key] = resolved
		}
	case []any:
		for index, item := range node {
			resolved, err := resolveSecrets(item, bindings, secrets)
			if err != nil {
				return nil, err
			}
			node[index] = resolved
		}
	}
	return value, nil
}

func rejectLiteralCredentials(value any) error {
	switch node := value.(type) {
	case map[string]any:
		for key, item := range node {
			normalized := strings.NewReplacer("-", "", "_", "").Replace(strings.ToLower(key))
			switch normalized {
			case "key", "apikey", "token", "accesstoken", "refreshtoken", "password", "secret", "clientsecret", "authorization", "proxyauthorization", "xapikey", "xauthtoken":
				placeholder, ok := item.(map[string]any)
				if !ok || len(placeholder) != 1 || placeholder["$secret"] == nil {
					return fmt.Errorf("literal credential forbidden in profile config: %s", key)
				}
			}
			if err := rejectLiteralCredentials(item); err != nil {
				return err
			}
		}
	case []any:
		for _, item := range node {
			if err := rejectLiteralCredentials(item); err != nil {
				return err
			}
		}
	}
	return nil
}
