package tailnet

import (
	"strings"
	"testing"
)

func TestScopeBindsDeploymentAndNamespaceInventory(t *testing.T) {
	scope := testScope(t, "deployment-a", "agents", "other")
	if scope != testScope(t, "deployment-a", "other", "agents", "agents") {
		t.Fatal("scope depends on namespace order or duplicates")
	}
	for _, foreign := range []string{
		testScope(t, "deployment-b", "agents", "other"),
		testScope(t, "deployment-a", "agents"),
		testScope(t, "deployment-a", "agents", "other", "added"),
	} {
		if scope == foreign {
			t.Fatal("different ownership inventory shares a scope")
		}
	}
	namespaces := []string{"other", "agents"}
	_ = testScope(t, "deployment-a", namespaces...)
	if namespaces[0] != "other" {
		t.Fatal("scope mutated caller namespaces")
	}
	c := &Client{Scope: scope}
	hostname := c.Hostname("ar-" + strings.Repeat("a", 32))
	if len(hostname) > 63 || !c.ownsHostname(hostname) {
		t.Fatalf("invalid scoped hostname %q", hostname)
	}
}

func TestScopeRequiresExplicitOwnership(t *testing.T) {
	for _, tt := range []struct {
		id         string
		namespaces []string
	}{
		{"", []string{"agents"}}, {" ", []string{"agents"}}, {"deployment", nil}, {"deployment", []string{""}}, {"deployment", []string{"agents", " "}},
	} {
		if _, err := NewScope(tt.id, tt.namespaces); err == nil {
			t.Fatalf("accepted ambiguous scope: %+v", tt)
		}
	}
}
