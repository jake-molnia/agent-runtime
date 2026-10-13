package workflows

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/robfig/cron/v3"
	"gopkg.in/yaml.v3"
)

const singleStep = "steps:\n  task: {agent: verify, input: input}\noutput: task\n"

func TestRecurringYAMLInputAndStrictness(t *testing.T) {
	for _, input := range []string{"null", "true", "42", "hello", "[one, two]", "{brief: test, options: {count: 2}}"} {
		var workflow Workflow
		if err := yaml.Unmarshal([]byte("input: "+input+"\nschedule: {cron: '0 8 * * *'}\nnotebook: true\n"+singleStep), &workflow); err != nil {
			t.Fatal(err)
		}
		workflow.Name = "brief"
		if _, err := workflow.order(); err != nil {
			t.Fatalf("%s: %v", input, err)
		}
		if workflow.DefaultInput == nil || !json.Valid(workflow.DefaultInput) {
			t.Fatalf("lost input: %s", input)
		}
	}
	for _, source := range []string{
		"unknown: true\n" + singleStep,
		"schedule: {cron: '0 8 * * *', typo: UTC}\n" + singleStep,
		"input: {nested: {key: 1, key: 2}}\n" + singleStep,
		"input: true\ninput: false\n" + singleStep,
		"steps:\n  task: {agent: verify, input: input, typo: true}\noutput: task\n",
	} {
		var workflow Workflow
		if err := yaml.Unmarshal([]byte(source), &workflow); err == nil {
			t.Fatalf("accepted malformed YAML %s", source)
		}
	}
	var without, withNull Workflow
	if err := yaml.Unmarshal([]byte(singleStep), &without); err != nil {
		t.Fatal(err)
	}
	if err := yaml.Unmarshal([]byte("input: null\n"+singleStep), &withNull); err != nil {
		t.Fatal(err)
	}
	if without.DefaultInput != nil || string(withNull.DefaultInput) != "null" {
		t.Fatalf("null distinction: %q / %q", without.DefaultInput, withNull.DefaultInput)
	}
}

func TestInvocationDefaultsAndRecurringValidation(t *testing.T) {
	workflow := Workflow{Name: "brief", Version: 1, Steps: map[string]Step{"task": {Agent: "verify", Input: InputRefs{Sources: []string{"input"}}}}, Output: "task", DefaultInput: json.RawMessage(`{"brief":"default"}`)}
	value, err := workflow.InvocationInput(nil)
	if err != nil || string(value) != string(workflow.DefaultInput) {
		t.Fatalf("default: %s %v", value, err)
	}
	value[0] = ' '
	if workflow.DefaultInput[0] != '{' {
		t.Fatal("input aliases manifest")
	}
	value, err = workflow.InvocationInput(json.RawMessage(`null`))
	if err != nil || string(value) != "null" {
		t.Fatalf("override: %s %v", value, err)
	}
	for _, override := range []json.RawMessage{[]byte{}, []byte(`{} {}`), []byte(strings.Repeat(" ", MaxInputBytes+1))} {
		if _, err := workflow.InvocationInput(override); err == nil {
			t.Fatal("accepted invalid override")
		}
	}
	workflow.Schedule = &Schedule{Cron: "0 8 * * *"}
	workflow.DefaultInput = nil
	if _, err := workflow.order(); err == nil {
		t.Fatal("scheduled without default")
	}
	workflow.Schedule = nil
	workflow.Notebook = true
	workflow.Steps["other"] = workflow.Steps["task"]
	if _, err := workflow.order(); err == nil {
		t.Fatal("multi-step notebook accepted")
	}
}

func TestScheduleTimezoneAndDST(t *testing.T) {
	for _, schedule := range []Schedule{{Cron: "@daily"}, {Cron: "0 0 8 * * *"}, {Cron: "60 8 * * *"}, {Cron: "CRON_TZ=UTC 0 8 * * *"}, {Cron: "0 8 * * *", Timezone: "missing/zone"}, {Cron: "0 8 * * *", Timezone: "Local"}} {
		if _, err := schedule.Expression(); err == nil {
			t.Fatalf("accepted %+v", schedule)
		}
	}
	expression, err := (Schedule{Cron: "0 8 * * *"}).Expression()
	if err != nil || expression != "CRON_TZ=UTC 0 8 * * *" {
		t.Fatalf("default zone: %s %v", expression, err)
	}
	expression, err = (Schedule{Cron: "0 8 * * *", Timezone: "Europe/London"}).Expression()
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow).Parse(expression)
	if err != nil {
		t.Fatal(err)
	}
	before := time.Date(2026, 3, 28, 9, 0, 0, 0, time.UTC)
	next := parsed.Next(before)
	if !next.Equal(time.Date(2026, 3, 29, 7, 0, 0, 0, time.UTC)) {
		t.Fatalf("DST next: %v", next)
	}
}

func TestRecurringYAMLRoundTrip(t *testing.T) {
	for _, input := range []string{`null`, `{"brief":"daily","count":9007199254740993}`} {
		var original Workflow
		if err := yaml.Unmarshal([]byte("input: "+input+"\n"+singleStep), &original); err != nil {
			t.Fatal(err)
		}
		data, err := yaml.Marshal(original)
		if err != nil {
			t.Fatal(err)
		}
		var roundTrip Workflow
		if err := yaml.Unmarshal(data, &roundTrip); err != nil {
			t.Fatal(err)
		}
		if string(original.DefaultInput) != string(roundTrip.DefaultInput) {
			t.Fatalf("lost default: %s -> %s", original.DefaultInput, roundTrip.DefaultInput)
		}
	}
}
