package definitions

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestOutputContractReachesModelAndLegacyReplayStaysStable(t *testing.T) {
	catalog := loaded(t, fixture(t))
	snapshot, err := catalog.Snapshot("reviewer")
	if err != nil {
		t.Fatal(err)
	}
	modelSystem := func(s Snapshot) string {
		t.Helper()
		definition, err := s.Definition()
		if err != nil {
			t.Fatal(err)
		}
		raw, err := definition.Config(nil)
		if err != nil {
			t.Fatal(err)
		}
		var config struct {
			Agents map[string]struct{ System string } `json:"agents"`
		}
		if err := json.Unmarshal(raw, &config); err != nil {
			t.Fatal(err)
		}
		return config.Agents["authored"].System
	}
	current := modelSystem(snapshot)
	if !strings.Contains(current, "# Required output contract") || !strings.Contains(current, string(snapshot.Agent.Schema)) {
		t.Fatalf("schema not supplied to model: %s", current)
	}
	newDigest := snapshot.Agent.Digest
	snapshot.CompiledPolicy = ""
	snapshot.Agent.Digest, err = snapshot.digest()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Agent.Digest == newDigest || strings.Contains(modelSystem(snapshot), "# Required output contract") {
		t.Fatal("new compiler behavior changed a legacy replay")
	}
}
