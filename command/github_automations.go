package command

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"

	"github.com/jake-molnia/agent-runtime/definitions"
	"github.com/jake-molnia/agent-runtime/githubreview"
	"github.com/jake-molnia/agent-runtime/hatchetbridge"
	"gopkg.in/yaml.v3"
)

type githubAutomation struct {
	Name    string `yaml:"-" json:"name"`
	Version int    `yaml:"version" json:"version"`
	Agent   string `yaml:"agent" json:"agent"`
	Handler string `yaml:"handler" json:"handler"`
	Trigger struct {
		Adapter string   `yaml:"adapter" json:"adapter"`
		Actions []string `yaml:"actions" json:"actions"`
	} `yaml:"trigger" json:"trigger"`
	Policy    githubAutomationPolicy `yaml:"policy" json:"policy"`
	Selection githubreview.Selection `yaml:"selection,omitempty" json:"selection,omitempty"`
}

type githubAutomationPolicy struct {
	Concurrency   string `yaml:"concurrency" json:"concurrency"`
	Limit         int    `yaml:"limit" json:"limit"`
	Deduplication string `yaml:"deduplication" json:"deduplication"`
}

func loadGitHubAutomations(catalog *definitions.Catalog) (map[string]githubAutomation, error) {
	directory := env("GITHUB_AUTOMATIONS_DIR", filepath.Join(env("AGENT_DEFINITIONS_DIR", "/config"), "github-automations"))
	entries, err := os.ReadDir(directory)
	result := map[string]githubAutomation{}
	if errors.Is(err, os.ErrNotExist) {
		return result, nil
	}
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		info, err := entry.Info()
		if err != nil {
			return nil, err
		}
		name := entry.Name()
		if !info.Mode().IsRegular() || entry.Type()&os.ModeSymlink != 0 || filepath.Ext(name) != ".yaml" {
			return nil, errors.New("GitHub adapters must be regular YAML files")
		}
		name = name[:len(name)-5]
		if !workflowName.MatchString(name) {
			return nil, errors.New("invalid GitHub automation name")
		}
		file, err := os.Open(filepath.Join(directory, entry.Name()))
		if err != nil {
			return nil, err
		}
		data, readErr := io.ReadAll(io.LimitReader(file, 1<<20+1))
		file.Close()
		if readErr != nil || len(data) > 1<<20 {
			return nil, errors.New("GitHub adapter manifest exceeds limit")
		}
		decoder := yaml.NewDecoder(bytes.NewReader(data))
		decoder.KnownFields(true)
		var automation githubAutomation
		if err := decoder.Decode(&automation); err != nil {
			return nil, err
		}
		if decoder.Decode(new(any)) != io.EOF {
			return nil, errors.New("expected one GitHub adapter YAML document")
		}
		automation.Name = name
		if automation.Policy == (githubAutomationPolicy{}) {
			automation.Policy = githubAutomationPolicy{Concurrency: "pull-request", Limit: 1, Deduplication: "reviewed-revision"}
		}
		agent, exists := catalog.Agents[automation.Agent]
		if !exists || len(agent.Schema) == 0 || automation.Version != 1 || automation.Handler != "github.pr-review" || automation.Trigger.Adapter != "github.pull_request" {
			return nil, errors.New("GitHub adapter requires a known structured agent and supported handler")
		}
		if automation.Policy != (githubAutomationPolicy{Concurrency: "pull-request", Limit: 1, Deduplication: "reviewed-revision"}) {
			return nil, errors.New("unsupported GitHub automation policy")
		}
		if err := automation.Selection.Validate(); err != nil {
			return nil, err
		}
		if len(automation.Trigger.Actions) == 0 {
			return nil, errors.New("GitHub trigger actions required")
		}
		seen := map[string]bool{}
		for _, action := range automation.Trigger.Actions {
			if seen[action] || !slices.Contains([]string{"opened", "reopened", "synchronize", "ready_for_review", "converted_to_draft", "labeled", "unlabeled", "edited"}, action) {
				return nil, errors.New("unsupported or repeated GitHub trigger action")
			}
			seen[action] = true
		}
		result[name] = automation
	}
	return result, nil
}

func githubInvocation(catalog *definitions.Catalog, automations map[string]githubAutomation, name string, data []byte) (hatchetbridge.Input, error) {
	automation, exists := automations[name]
	if !exists {
		return hatchetbridge.Input{}, errors.New("unknown GitHub automation")
	}
	var review githubreview.Input
	if err := strictJSON(data, &review); err != nil {
		return hatchetbridge.Input{}, err
	}
	if review.RepositoryID <= 0 || review.InstallationID <= 0 || review.Number <= 0 {
		return hatchetbridge.Input{}, errors.New("invalid review identity")
	}
	snapshot, err := catalog.Snapshot(automation.Agent)
	if err != nil {
		return hatchetbridge.Input{}, err
	}
	encoded, err := json.Marshal(review)
	if err != nil {
		return hatchetbridge.Input{}, err
	}
	return hatchetbridge.Input{Agent: automation.Agent, Digest: snapshot.Agent.Digest, Review: encoded, GroupKey: fmt.Sprintf("%d/%d", review.RepositoryID, review.Number)}, nil
}
