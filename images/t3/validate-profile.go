// Validate the example against the runtime's actual profile and workspace types.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	k8syaml "k8s.io/apimachinery/pkg/util/yaml"
	"os"
	"strings"

	"github.com/jake-molnia/agent-runtime/lifecycle"
	"github.com/jake-molnia/agent-runtime/sandbox"
)

func main() {
	if len(os.Args) < 2 {
		panic("usage: go run ./images/t3/validate-profile.go deploy/t3/t3-profiles.json")
	}
	data, err := os.ReadFile(os.Args[1])
	if err != nil {
		panic(err)
	}
	decoder := json.NewDecoder(strings.NewReader(strings.ReplaceAll(string(data), "REPLACE_STORAGE_CLASS", "example-storage")))
	decoder.DisallowUnknownFields()
	var profiles map[string]lifecycle.Profile
	if err := decoder.Decode(&profiles); err != nil {
		panic(err)
	}
	for _, file := range os.Args[2:] {
		content, err := os.ReadFile(file)
		if err != nil {
			panic(err)
		}
		reader := k8syaml.NewYAMLOrJSONDecoder(strings.NewReader(string(content)), 4096)
		for {
			var doc map[string]any
			err := reader.Decode(&doc)
			if err == io.EOF {
				break
			}
			if err != nil {
				panic(fmt.Errorf("%s: %w", file, err))
			}
		}
		fmt.Printf("Parsed YAML %s.\n", file)
	}
	for name, profile := range profiles {
		spec := sandbox.WorkspaceSpec{Namespace: profile.Namespace, WorkspaceID: "example", AllocationID: "example", Storage: profile.Storage, PodTemplate: profile.PodTemplate}
		if err := spec.Validate(); err != nil {
			panic(fmt.Errorf("profile %s: %w", name, err))
		}
		if profile.Port != 8083 || profile.WorkerContainer != "worker" {
			panic("example must point to the T3 execution worker")
		}
		fmt.Printf("Validated profile %s; deployment placeholders still require replacement.\n", name)
	}
}
