package command

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jake-molnia/agent-runtime/definitions"
	"github.com/jake-molnia/agent-runtime/githubreview"
	"github.com/jake-molnia/agent-runtime/hatchetbridge"
)

func TestCheckedInDefinitionsAndInvocation(t *testing.T) {
	root, err := filepath.Abs("../examples/definitions")
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := definitions.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_DEFINITIONS_DIR", root)
	t.Setenv("AGENT_DEFINITIONS_FILE", "")
	if err := Run(context.Background(), []string{"agents", "validate"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("../examples/review.json")
	if err != nil {
		t.Fatal(err)
	}
	input, err := invocation(catalog, "github-pr-review", data)
	if err != nil {
		t.Fatal(err)
	}
	if input.Agent != "github-reviewer" || input.GroupKey != "67890/1" || input.Digest != catalog.Agents[input.Agent].Digest || input.Run.Prompt != "" {
		t.Fatalf("invalid invocation: %+v", input)
	}
	for _, bad := range []string{`{"installation_id":1,"repository_id":2,"number":3,"config":{}}`, `null`, `{"repository_id":0,"number":3}`, string(data) + `{}`} {
		if _, err := invocation(catalog, "github-pr-review", []byte(bad)); err == nil {
			t.Fatalf("accepted bad input %s", bad)
		}
	}
	if _, err := invocation(catalog, "not-registered", data); err == nil {
		t.Fatal("accepted unknown workflow")
	}
	manual, err := invocation(catalog, "agent-run", []byte(`{"agent":"github-reviewer","prompt":"Return an empty review JSON object"}`))
	if err != nil || manual.Digest != input.Digest || manual.Run.Prompt == "" {
		t.Fatalf("manual: %+v %v", manual, err)
	}
}

func TestPinnedDefinitionSurvivesAuthoringChanges(t *testing.T) {
	catalog, err := definitions.Load("../examples/definitions")
	if err != nil {
		t.Fatal(err)
	}
	snapshots := t.TempDir()
	if err := catalog.Save(snapshots); err != nil {
		t.Fatal(err)
	}
	agent := catalog.Agents["github-reviewer"]
	spec := hatchetbridge.Spec{Agent: agent.Name, Digest: agent.Digest}
	definition, err := pinnedDefinition(snapshots, spec)
	if err != nil {
		t.Fatal(err)
	}
	config, err := definition.Config(map[string]string{"openai": "test-provider-key"})
	if err != nil {
		t.Fatal(err)
	}
	agent.Instructions = "Changed during deployment"
	catalog.Agents[agent.Name] = agent
	changed, err := catalog.Snapshot(agent.Name)
	if err != nil {
		t.Fatal(err)
	}
	if changed.Agent.Digest == spec.Digest {
		t.Fatal("changed instructions retained old identity")
	}
	recovered, err := pinnedDefinition(snapshots, spec)
	if err != nil {
		t.Fatal(err)
	}
	again, err := recovered.Config(map[string]string{"openai": "test-provider-key"})
	if err != nil || string(config) != string(again) {
		t.Fatal("old run changed behavior")
	}
	spec.Agent = "other"
	if _, err := pinnedDefinition(snapshots, spec); err == nil {
		t.Fatal("accepted mismatched agent")
	}
	spec.Digest = strings.Repeat("0", 64)
	if _, err := pinnedDefinition(snapshots, spec); err == nil {
		t.Fatal("accepted missing snapshot")
	}
}

func TestRemovedConfigurationFailsExplicitly(t *testing.T) {
	t.Setenv("AGENT_DEFINITIONS_FILE", "old.json")
	if _, err := loadCatalog(); err == nil || !strings.Contains(err.Error(), "migrate") {
		t.Fatalf("migration diagnostic: %v", err)
	}
}

func TestStrictJSONBoundsAndFields(t *testing.T) {
	for _, data := range []string{"", strings.Repeat(" ", 1<<20+1), `{"agent":"x","unknown":true}`, `{"agent":"x"} null`} {
		var target struct {
			Agent string `json:"agent"`
		}
		if err := strictJSON([]byte(data), &target); err == nil {
			t.Fatal("accepted invalid input")
		}
	}
	var out json.RawMessage
	if err := strictJSON([]byte(`{"ok":true}`), &out); err != nil {
		t.Fatal(err)
	}
}

func TestResolvedReviewUsesSinglePinnedContext(t *testing.T) {
	metadata := githubreview.Resolved{Digest: "pinned-digest", Key: "review-key"}
	domain, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	spec := hatchetbridge.Spec{Digest: metadata.Digest, Domain: domain}
	spec.Run.Prompt = "pinned diff context"
	resolved, err := resolvedReview(spec)
	if err != nil || resolved.Prompt != spec.Run.Prompt || resolved.Key != metadata.Key {
		t.Fatalf("context reconstruction: %+v %v", resolved, err)
	}
	metadata.Prompt = "a different diff"
	spec.Domain, _ = json.Marshal(metadata)
	if _, err := resolvedReview(spec); err == nil {
		t.Fatal("accepted conflicting contexts")
	}
	metadata.Prompt = ""
	metadata.Digest = "other-digest"
	spec.Domain, _ = json.Marshal(metadata)
	if _, err := resolvedReview(spec); err == nil {
		t.Fatal("accepted mismatched definition identity")
	}
}
