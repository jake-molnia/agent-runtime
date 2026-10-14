package githubreview

import (
	"strings"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
)

// Escape raw HTML before embedding Markdown inside a renderer-owned disclosure.
// Code spans and fenced code retain their literal contents.
func disclosureMarkdown(markdown string) string {
	source := []byte(markdown)
	document := goldmark.DefaultParser().Parse(text.NewReader(source))
	escape := make([]bool, len(source))
	mark := func(segment text.Segment) {
		for i := segment.Start; i < segment.Stop; i++ {
			if source[i] == '<' {
				escape[i] = true
			}
		}
	}
	ast.Walk(document, func(node ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		switch node := node.(type) {
		case *ast.RawHTML:
			for i := 0; i < node.Segments.Len(); i++ {
				mark(node.Segments.At(i))
			}
		case *ast.HTMLBlock:
			for i := 0; i < node.Lines().Len(); i++ {
				mark(node.Lines().At(i))
			}
			if node.HasClosure() {
				mark(node.ClosureLine)
			}
		}
		return ast.WalkContinue, nil
	})
	var result strings.Builder
	for i, b := range source {
		if escape[i] {
			result.WriteString("\\<")
		} else {
			result.WriteByte(b)
		}
	}
	return result.String()
}
