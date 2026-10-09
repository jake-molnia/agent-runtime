package workflows

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jake-molnia/agent-runtime/definitions"
)

const notebookOutputSchema = `{"type":"object","required":["status","notebook"],"properties":{"status":{"enum":["completed","no_change","blocked"]},"notebook":{"type":"string"}}}`

func notebookContractFixture(schema string) (Workflow, *definitions.Catalog) {
	catalog := fixtureCatalog()
	agent := catalog.Agents["verify"]
	agent.Schema = json.RawMessage(schema)
	catalog.Agents["verify"] = agent
	return Workflow{Name: "notes", Version: 1, Notebook: true,
		Steps: map[string]Step{"work": {Agent: "verify", Input: InputRefs{Sources: []string{"input"}}}}, Output: "work",
	}, catalog
}

func TestNotebookContractAllowsExplicitEnvelope(t *testing.T) {
	for _, schema := range []string{
		notebookOutputSchema,
		strings.Replace(notebookOutputSchema, `["completed","no_change","blocked"]`, `["completed"]`, 1),
	} {
		workflow, catalog := notebookContractFixture(schema)
		snapshot, err := Capture(workflow, catalog)
		if err != nil {
			t.Fatal(err)
		}
		if err := snapshot.Verify(); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range definitions.BuiltinNames()[3:] {
		agent, _ := definitions.Builtin(name)
		workflow, catalog := notebookContractFixture(string(agent.Schema))
		if _, err := Capture(workflow, catalog); err != nil {
			t.Fatalf("builtin %s contract rejected: %v", name, err)
		}
	}
}

func TestNotebookContractRejectsIncompatibleSchemasOnCaptureAndVerify(t *testing.T) {
	legacy, _ := definitions.Builtin("verify")
	cases := map[string]string{
		"legacy verify":             string(legacy.Schema),
		"wrong root type":           strings.Replace(notebookOutputSchema, `"type":"object"`, `"type":"string"`, 1),
		"union root type":           strings.Replace(notebookOutputSchema, `"type":"object"`, `"type":["object","null"]`, 1),
		"missing required notebook": strings.Replace(notebookOutputSchema, `"required":["status","notebook"]`, `"required":["status"]`, 1),
		"missing required status":   strings.Replace(notebookOutputSchema, `"required":["status","notebook"]`, `"required":["notebook"]`, 1),
		"missing notebook shape":    strings.Replace(notebookOutputSchema, `"notebook":{"type":"string"}`, `"notebook":{}`, 1),
		"nullable notebook":         strings.Replace(notebookOutputSchema, `"type":"string"`, `"type":["string","null"]`, 1),
		"missing status enum":       strings.Replace(notebookOutputSchema, `{"enum":["completed","no_change","blocked"]}`, `{"type":"string"}`, 1),
		"unknown status":            strings.Replace(notebookOutputSchema, `"completed"`, `"success"`, 1),
		"nullable status":           strings.Replace(notebookOutputSchema, `"completed"`, `null`, 1),
		"reference":                 `{"$defs":{"result":` + notebookOutputSchema + `},"$ref":"#/$defs/result"}`,
		"composition":               `{"allOf":[` + notebookOutputSchema + `]}`,
		"composition with siblings": strings.Replace(notebookOutputSchema, `{"type":"object"`, `{"allOf":[{}],"type":"object"`, 1),
		"notebook composition":      strings.Replace(notebookOutputSchema, `"notebook":{"type":"string"}`, `"notebook":{"type":"string","allOf":[{}]}`, 1),
	}
	for name, schema := range cases {
		t.Run(name, func(t *testing.T) {
			workflow, catalog := notebookContractFixture(schema)
			if _, err := Capture(workflow, catalog); err == nil || !strings.Contains(err.Error(), "notebook output contract") {
				t.Fatalf("incompatible notebook capture: %v", err)
			}
			workflow.Notebook = false
			snapshot, err := Capture(workflow, catalog)
			if err != nil {
				t.Fatalf("ordinary workflow should retain schema support: %v", err)
			}
			snapshot.Workflow.Notebook = true
			snapshot.Digest, err = snapshot.digest()
			if err != nil {
				t.Fatal(err)
			}
			if err := snapshot.Verify(); err == nil || !strings.Contains(err.Error(), "notebook output contract") {
				t.Fatalf("incompatible notebook replay: %v", err)
			}
		})
	}
}

func TestBuiltinNotebookCharacterBudget(t *testing.T) {
	agent, _ := definitions.Builtin("daily-brief")
	workflow, catalog := notebookContractFixture(string(agent.Schema))
	snapshot, err := Capture(workflow, catalog)
	if err != nil {
		t.Fatal(err)
	}
	for _, character := range []string{"a", "\U0001F30D"} {
		for _, count := range []int{16384, 16385} {
			output, err := json.Marshal(map[string]any{
				"status": "completed", "report": "A complete report", "sources": []any{},
				"notebook": strings.Repeat(character, count),
			})
			if err != nil {
				t.Fatal(err)
			}
			err = snapshot.Agents["work"].ValidateOutput(output)
			if (err == nil) != (count == 16384) {
				t.Fatalf("character %q count %d: %v", character, count, err)
			}
		}
	}
}
