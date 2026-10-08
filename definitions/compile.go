package definitions

import (
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
	definition := orchestration.Definition{
		Pool: profile.Pool, Namespace: profile.Namespace, Directory: profile.Directory,
		Agent: "authored", Model: map[string]string{"providerID": agent.Model.Provider, "id": agent.Model.ID},
		Timeout: time.Duration(agent.Execution.TimeoutSeconds) * time.Second, Tags: append([]string(nil), profile.Tags...),
	}
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
		deny := []permission{{Action: "*", Resource: "*", Effect: "deny"}}
		config["permissions"] = deny
		config["agents"] = map[string]any{"authored": map[string]any{
			"system": system, "description": agent.Description, "mode": "primary", "permissions": deny,
		}}
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
