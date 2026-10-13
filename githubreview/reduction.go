package githubreview

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"unicode/utf8"
)

type Candidate struct {
	Priority    string `json:"priority"`
	Path        string `json:"path"`
	Line        int    `json:"line"`
	Title       string `json:"title"`
	Explanation string `json:"explanation"`
}
type CandidateReview struct {
	Summary     string      `json:"summary"`
	Findings    []Candidate `json:"findings"`
	Limitations string      `json:"limitations"`
}
type FindingWording struct {
	Sources     []int  `json:"sources"`
	Title       string `json:"title"`
	Explanation string `json:"explanation"`
}
type Writeup struct {
	Assessment string           `json:"assessment"`
	Findings   []FindingWording `json:"findings"`
}

func boundedText(s string, max int) bool {
	return strings.TrimSpace(s) != "" && utf8.ValidString(s) && utf8.RuneCountInString(s) <= max
}
func Reduce(reports []json.RawMessage, raw json.RawMessage) (json.RawMessage, error) {
	candidates := []Candidate{}
	for _, data := range reports {
		var report CandidateReview
		if len(data) > MaxOutputBytes {
			return nil, errors.New("candidate output exceeds bound")
		}
		if err := requiredKeys(data, "summary", "findings", "limitations"); err != nil {
			return nil, err
		}
		if err := decodeExact(data, &report); err != nil {
			return nil, err
		}
		if len(report.Findings) > 15 {
			return nil, errors.New("too many candidates")
		}
		for _, f := range report.Findings {
			if (f.Priority != "P1" && f.Priority != "P2" && f.Priority != "P3") || !safePath(f.Path) || len(f.Path) > 500 || f.Line < 1 || !boundedText(f.Title, 200) || !boundedText(f.Explanation, 1800) {
				return nil, errors.New("invalid candidate")
			}
		}
		candidates = append(candidates, report.Findings...)
	}
	if len(candidates) > 30 || len(raw) > MaxOutputBytes {
		return nil, errors.New("review exceeds bounds")
	}
	if err := requiredKeys(raw, "assessment", "findings"); err != nil {
		return nil, err
	}
	var wording Writeup
	if err := decodeExact(raw, &wording); err != nil {
		return nil, err
	}
	if !boundedText(wording.Assessment, 700) || len(wording.Findings) > 30 {
		return nil, errors.New("invalid writeup")
	}
	seen := make([]bool, len(candidates))
	merged := []Candidate{}
	for _, f := range wording.Findings {
		if len(f.Sources) == 0 || !boundedText(f.Title, 200) || !boundedText(f.Explanation, 1800) {
			return nil, errors.New("invalid finding wording")
		}
		var selected Candidate
		for i, source := range f.Sources {
			if source < 0 || source >= len(candidates) || seen[source] {
				return nil, errors.New("invalid or duplicate finding source")
			}
			seen[source] = true
			if i == 0 {
				selected = candidates[source]
			} else if candidates[source].Priority < selected.Priority {
				selected.Priority = candidates[source].Priority
			}
		}
		selected.Title = f.Title
		selected.Explanation = f.Explanation
		merged = append(merged, selected)
	}
	for _, covered := range seen {
		if !covered {
			return nil, errors.New("writeup omitted a candidate")
		}
	}
	sort.SliceStable(merged, func(i, j int) bool {
		a, b := merged[i], merged[j]
		if a.Priority != b.Priority {
			return a.Priority < b.Priority
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.Line < b.Line
	})
	output := struct {
		Summary  string    `json:"summary"`
		Findings []Finding `json:"findings"`
	}{Summary: wording.Assessment, Findings: []Finding{}}
	for _, f := range merged {
		output.Findings = append(output.Findings, Finding{Path: f.Path, Line: f.Line, Body: fmt.Sprintf("[%s] %s\n\n%s", f.Priority, f.Title, f.Explanation)})
	}
	return json.Marshal(output)
}

func requiredKeys(data []byte, keys ...string) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		return err
	}
	for _, key := range keys {
		value, ok := fields[key]
		if !ok || string(value) == "null" {
			return errors.New("required review field missing or null")
		}
	}
	return nil
}
