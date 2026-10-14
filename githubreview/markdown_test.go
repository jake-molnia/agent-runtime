package githubreview

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/renderer/html"
	htmlparser "golang.org/x/net/html"
)

func TestDetailsCannotCloseDisclosure(t *testing.T) {
	for _, details := range []string{
		"Before\n\n</details>\n\nsupporting-tail",
		"Before </DeTaIlS > supporting-tail",
		"<!--" + strings.Repeat("<", 1700) + "supporting-tail",
		"<!-- unfinished comment\nsupporting-tail",
		"Literal `</details>` and supporting-tail",
		"```html\n</details>\n```\n\nsupporting-tail",
	} {
		t.Run(details, func(t *testing.T) {
			report, _ := json.Marshal(CandidateReview{Summary: "Verified", Findings: []Candidate{{Priority: "P2", Path: "a.go", Line: 1, Title: "Issue", Explanation: "Evidence"}}})
			wording, _ := json.Marshal(Writeup{Assessment: "Review", Findings: []FindingWording{{Sources: []int{0}, Title: "Issue", Explanation: "Impact", Details: details}}})
			output, err := Reduce([]json.RawMessage{report}, wording)
			if err != nil {
				t.Fatal(err)
			}
			_, findings, err := validateOutput(output)
			if err != nil {
				t.Fatal(err)
			}
			var rendered bytes.Buffer
			markdown := goldmark.New(goldmark.WithRendererOptions(html.WithUnsafe()))
			if err := markdown.Convert([]byte(findings[0].Body), &rendered); err != nil {
				t.Fatal(err)
			}
			doc, err := htmlparser.Parse(&rendered)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			var inspect func(*htmlparser.Node, bool)
			inspect = func(node *htmlparser.Node, collapsed bool) {
				collapsed = collapsed || (node.Type == htmlparser.ElementNode && node.Data == "details")
				if node.Type == htmlparser.TextNode && strings.Contains(node.Data, "supporting-tail") {
					found = true
					if !collapsed {
						t.Error("supporting text escaped the details disclosure")
					}
				}
				for child := node.FirstChild; child != nil; child = child.NextSibling {
					inspect(child, collapsed)
				}
			}
			inspect(doc, false)
			if !found {
				t.Fatal("supporting text disappeared during rendering")
			}
			if strings.Contains(details, "`") && !strings.Contains(findings[0].Body, details) {
				t.Fatal("literal code was modified")
			}
		})
	}
}
