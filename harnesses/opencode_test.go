package harnesses

import (
	"bytes"
	"os"
	"testing"
)

func TestImageAssetAndCompiledDefaultsMatch(t *testing.T) {
	data, err := os.ReadFile("opencode.json")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, openCodeConfig) {
		t.Fatal("image file differs from compiled policy")
	}
	first, err := OpenCode()
	if err != nil {
		t.Fatal(err)
	}
	first["agents"].(map[string]any)["authored"].(map[string]any)["system"] = "run-specific"
	second, err := OpenCode()
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := second["agents"].(map[string]any)["authored"].(map[string]any)["system"]; exists {
		t.Fatal("one run changed the next run's base config")
	}
}
