// Command generate emits operation wrappers from the pinned OpenCode contract.
package main

import (
	"encoding/json"
	"fmt"
	"go/format"
	"os"
	"sort"
	"strings"
)

func main() {
	b, err := os.ReadFile("openapi.json")
	must(err)
	var spec struct {
		Paths map[string]map[string]struct {
			ID string `json:"operationId"`
		} `json:"paths"`
	}
	must(json.Unmarshal(b, &spec))
	var entries []string
	for path, methods := range spec.Paths {
		for method, op := range methods {
			if op.ID == "" {
				continue
			}
			parts := strings.FieldsFunc(op.ID, func(r rune) bool { return r == '.' || r == '-' || r == '_' })
			for i, s := range parts {
				parts[i] = strings.ToUpper(s[:1]) + s[1:]
			}
			name := strings.Join(parts, "")
			entries = append(entries, fmt.Sprintf("// %s invokes %s %s. The caller owns the response body.\nfunc (c *Client) %s(ctx context.Context, args Arguments) (*http.Response, error) { return c.Do(ctx, %q, %q, args) }\n", name, strings.ToUpper(method), path, name, strings.ToUpper(method), path))
		}
	}
	sort.Strings(entries)
	out, err := format.Source([]byte("// Code generated from openapi.json; DO NOT EDIT.\npackage opencode\nimport (\"context\"; \"net/http\")\n" + strings.Join(entries, "\n")))
	must(err)
	must(os.WriteFile("operations.gen.go", out, 0644))
}
func must(err error) {
	if err != nil {
		panic(err)
	}
}
