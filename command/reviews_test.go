package command

import (
	"path/filepath"
	"testing"
)

func TestReviewIntegrationIsOptional(t *testing.T) {
	t.Setenv("AGENT_DEFINITIONS_DIR", t.TempDir())
	t.Setenv("AGENT_GITHUB_REVIEW_CONFIG", "")
	_, enabled, err := optionalReviewConfig()
	if err != nil || enabled {
		t.Fatalf("unconfigured worker unexpectedly needs GitHub: %v %v", enabled, err)
	}
	t.Setenv("AGENT_GITHUB_REVIEW_CONFIG", filepath.Join(t.TempDir(), "missing.yaml"))
	if _, _, err := optionalReviewConfig(); err == nil {
		t.Fatal("explicit missing policy silently disabled")
	}
}
