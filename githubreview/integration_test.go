package githubreview

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func integrationFixture() Integration {
	return Integration{Version: 1, Name: "test-review", Workflow: "review-graph", CandidateSteps: []string{"verify", "adversarial"}, WriteupStep: "writeup", Actions: []string{"opened", "synchronize"}, Repositories: map[int64]RepositoryPolicy{2: {Name: "owner/repo", InstallationID: 1}}}
}
func TestIntegrationStrictConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "review.yaml")
	base := "version: 1\nname: test-review\nworkflow: graph\ncandidate_steps: [verify]\nwriteup_step: writeup\nactions: [opened]\nrepositories:\n  2:\n    name: owner/repo\n    installation_id: 1\n"
	for _, suffix := range []string{"", "typo: true\n", "---\nversion: 1\n", "name: duplicate\n", "native_events: true\nworker_labels: {cluster: staging}\n"} {
		if err := os.WriteFile(path, []byte(base+suffix), 0600); err != nil {
			t.Fatal(err)
		}
		_, err := LoadIntegration(path)
		if (err == nil) != (suffix == "" || strings.HasPrefix(suffix, "native_events:")) {
			t.Fatalf("suffix %q: %v", suffix, err)
		}
	}
}
func TestIntegrationNormalize(t *testing.T) {
	config := integrationFixture()
	input := Input{RepositoryID: 2, InstallationID: 1, Repository: "owner/repo", Number: 7, BaseSHA: strings.Repeat("a", 40), HeadSHA: strings.Repeat("b", 40), DeliveryID: "manual-1"}
	raw, _ := json.Marshal(Request{Review: input})
	request, skip, err := config.Normalize(raw)
	if err != nil || skip || request.Publish {
		t.Fatalf("manual: %+v %v %v", request, skip, err)
	}
	input.InstallationID = 9
	raw, _ = json.Marshal(Request{Review: input})
	if _, _, err = config.Normalize(raw); err == nil {
		t.Fatal("installation spoof accepted")
	}
	native := []byte(`{"action":"opened","number":7,"installation":{"id":1},"repository":{"id":2,"full_name":"owner/repo"},"pull_request":{"number":7,"draft":false,"state":"open","base":{"sha":"` + strings.Repeat("a", 40) + `","repo":{"id":2,"full_name":"owner/repo"}},"head":{"sha":"` + strings.Repeat("b", 40) + `"}}}`)
	if _, _, err = config.Normalize(native); err == nil {
		t.Fatal("disabled native ingress accepted")
	}
	config.NativeEvents = true
	request, skip, err = config.Normalize(native)
	if err != nil || skip || !request.Publish {
		t.Fatalf("native: %+v %v %v", request, skip, err)
	}
	for _, replacement := range []struct{ from, to string }{{`"draft":false`, `"draft":true`}, {`"state":"open"`, `"state":"closed"`}, {`"action":"opened"`, `"action":"edited"`}} {
		raw := strings.Replace(string(native), replacement.from, replacement.to, 1)
		_, skip, err := config.Normalize([]byte(raw))
		if err != nil || !skip {
			t.Fatalf("filter %s: %v %v", raw, skip, err)
		}
	}
}
func TestReduceSourceIntegrity(t *testing.T) {
	report := json.RawMessage(`{"summary":"Private","limitations":"","findings":[{"priority":"P2","path":"a.go","line":3,"title":"A","explanation":"A detail"},{"priority":"P1","path":"b.go","line":8,"title":"B","explanation":"B detail"}]}`)
	wording := `{"assessment":"Fix this","findings":[{"sources":[0,1],"title":"Merged","explanation":"Merged detail"}]}`
	output, err := Reduce([]json.RawMessage{report}, []byte(wording))
	if err != nil {
		t.Fatal(err)
	}
	var got struct{ Findings []Finding }
	if err = json.Unmarshal(output, &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Findings) != 1 || got.Findings[0].Path != "a.go" || got.Findings[0].Line != 3 || !strings.Contains(got.Findings[0].Body, "[P1]") {
		t.Fatalf("wrong merge %s", output)
	}
	for _, sources := range []string{"[]", "[0]", "[0,0]", "[0,2]", "[-1,1]"} {
		if _, err = Reduce([]json.RawMessage{report}, []byte(strings.Replace(wording, "[0,1]", sources, 1))); err == nil {
			t.Fatalf("accepted sources %s", sources)
		}
	}
	if _, err = Reduce([]json.RawMessage{report}, []byte(strings.Replace(wording, `"title":"Merged"`, `"path":"injected.go","title":"Merged"`, 1))); err == nil {
		t.Fatal("writer location injection accepted")
	}
	if _, err = Reduce(nil, []byte(`{"assessment":"No issues","findings":[]}`)); err != nil {
		t.Fatal(err)
	}
}
