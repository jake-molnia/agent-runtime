package command

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
	"github.com/jake-molnia/agent-runtime/definitions"
	"github.com/jake-molnia/agent-runtime/hatchetbridge"
	"github.com/jake-molnia/agent-runtime/workflows"
)

func loadCatalog() (*definitions.Catalog, error) {
	if os.Getenv("AGENT_DEFINITIONS_FILE") != "" {
		return nil, errors.New("AGENT_DEFINITIONS_FILE is removed; migrate to AGENT_DEFINITIONS_DIR and deployment.yaml")
	}
	return definitions.Load(env("AGENT_DEFINITIONS_DIR", "/config"))
}

func agentsCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: agent-runtime agents defaults|list|validate|inspect NAME")
	}
	if args[0] == "defaults" {
		if len(args) != 1 {
			return errors.New("defaults takes no arguments")
		}
		for _, name := range definitions.BuiltinNames() {
			agent, _ := definitions.Builtin(name)
			if err := json.NewEncoder(os.Stdout).Encode(agent); err != nil {
				return err
			}
		}
		return nil
	}
	catalog, err := loadCatalog()
	if err != nil {
		return err
	}
	switch args[0] {
	case "validate":
		if len(args) != 1 {
			return errors.New("validate takes no arguments")
		}
		plans, err := workflows.Load(env("AGENT_DEFINITIONS_DIR", "/config"), catalog)
		if err != nil {
			return err
		}
		fmt.Printf("valid: %d agents, %d workflows, %d profiles\n", len(catalog.Agents), len(plans), len(catalog.Profiles))
	case "list":
		if len(args) != 1 {
			return errors.New("list takes no arguments")
		}
		names := make([]string, 0, len(catalog.Agents))
		for name := range catalog.Agents {
			names = append(names, name)
		}
		slices.Sort(names)
		for _, name := range names {
			agent := catalog.Agents[name]
			fmt.Printf("%s\t%s\t%s\n", name, agent.Digest, agent.Description)
		}
	case "inspect":
		if len(args) != 2 {
			return errors.New("inspect requires an agent name")
		}
		snapshot, err := catalog.Snapshot(args[1])
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(snapshot)
	default:
		return errors.New("unknown agents command")
	}
	return nil
}

func strictJSON(data []byte, target any) error {
	if len(data) == 0 || len(data) > 1<<20 {
		return errors.New("invalid input size")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return errors.New("invalid input JSON")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("trailing input JSON")
	}
	return nil
}

func runCommand(ctx context.Context, args []string) error {
	if len(args) < 1 {
		return errors.New("usage: agent-runtime run WORKFLOW --input FILE")
	}
	flags := flag.NewFlagSet("run", flag.ContinueOnError)
	path := flags.String("input", "", "JSON input file")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if *path == "" || flags.NArg() != 0 {
		return errors.New("run requires --input FILE")
	}
	file, err := os.Open(*path)
	if err != nil {
		return errors.New("input file unavailable")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 1<<20+1))
	if err != nil {
		return errors.New("input file unavailable")
	}
	catalog, err := loadCatalog()
	if err != nil {
		return err
	}
	plans, err := workflows.Load(env("AGENT_DEFINITIONS_DIR", "/config"), catalog)
	if err != nil {
		return err
	}
	input, err := invocation(plans, args[0], data)
	if err != nil {
		return err
	}
	client, err := hatchet.NewClient()
	if err != nil {
		return err
	}
	defer client.Close(context.Background())
	ref, err := client.RunNoWait(ctx, hatchetbridge.ConfiguredWorkflowName(args[0], input.Digest), input)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]string{"run_id": ref.RunId, "workflow": args[0]})
}

func invocation(plans map[string]workflows.Snapshot, workflow string, data []byte) (hatchetbridge.ConfiguredInput, error) {
	plan, exists := plans[workflow]
	if !exists {
		return hatchetbridge.ConfiguredInput{}, errors.New("unknown configured workflow")
	}
	if len(data) > 1<<20 || !json.Valid(data) {
		return hatchetbridge.ConfiguredInput{}, errors.New("bounded JSON input required")
	}
	return hatchetbridge.ConfiguredInput{Digest: plan.Digest, Input: append(json.RawMessage(nil), data...)}, nil
}
