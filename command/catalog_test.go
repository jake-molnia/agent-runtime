package command

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jake-molnia/agent-runtime/definitions"
	"github.com/jake-molnia/agent-runtime/workflows"
)

func TestConfiguredDefinitionsAndInvocation(t *testing.T) {
	root, err := filepath.Abs("../examples/definitions")
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := definitions.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	plans, err := workflows.Load(root, catalog)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("AGENT_DEFINITIONS_DIR", root)
	t.Setenv("AGENT_DEFINITIONS_FILE", "")
	if err := Run(context.Background(), []string{"agents", "validate"}); err != nil {
		t.Fatal(err)
	}
	for _, data := range []string{`"plain text"`, `{"anything":[1,2]}`, `true`, `null`} {
		input, err := invocation(plans, "review-change", []byte(data))
		if err != nil {
			t.Fatal(err)
		}
		if string(input.Input) != data || input.Digest != plans["review-change"].Digest {
			t.Fatal("rewrote generic input or failed to pin workflow")
		}
	}
	for _, data := range []string{"", "{} {}", strings.Repeat(" ", 1<<20+1)} {
		if _, err := invocation(plans, "review-change", []byte(data)); err == nil {
			t.Fatal("accepted invalid input")
		}
	}
	for _, name := range []string{"github-pr-review", "agent-run", "unknown"} {
		if _, err := invocation(plans, name, []byte(`{}`)); err == nil {
			t.Fatal("accepted unconfigured workflow")
		}
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
