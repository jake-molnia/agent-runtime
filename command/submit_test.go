package command

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSubmissionData(t *testing.T) {
	for _, test := range []struct {
		name, data string
		valid      bool
	}{
		{"message", `{"parts":[{"name":"request","kind":"text","text":"test"}]}`, true},
		{"domain", `{"repository":"owner/repo","number":1}`, true},
		{"null", "null", false}, {"array", "[]", false}, {"trailing", "{} {}", false}, {"oversized", strings.Repeat(" ", 1<<20+1), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "input.json")
			if err := os.WriteFile(path, []byte(test.data), 0600); err != nil {
				t.Fatal(err)
			}
			data, err := submissionData(path)
			if (err == nil) != test.valid {
				t.Fatalf("submission: %s %v", data, err)
			}
			if test.valid && string(data) != test.data {
				t.Fatal("rewrote caller input")
			}
		})
	}
	if _, err := submissionData(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("accepted missing input")
	}
}

func TestSubmitRejectsInvalidArgumentsBeforeClientSetup(t *testing.T) {
	t.Setenv("AGENT_DEFINITIONS_FILE", "not-used")
	for _, args := range [][]string{nil, {"bad/workflow"}, {"workflow"}, {"workflow", "--input", "missing.json", "extra"}, {"workflow", "--input", "missing.json"}} {
		if err := submitCommand(context.Background(), args); err == nil {
			t.Fatalf("accepted args %v", args)
		}
	}
}
