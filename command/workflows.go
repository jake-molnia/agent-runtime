package command

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"

	"github.com/jake-molnia/agent-runtime/workflows"
)

func workflowsCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: agent-runtime workflows list|validate|inspect NAME")
	}
	catalog, err := loadCatalog()
	if err != nil {
		return err
	}
	plans, err := workflows.Load(env("AGENT_DEFINITIONS_DIR", "/config"), catalog)
	if err != nil {
		return err
	}
	switch args[0] {
	case "validate":
		if len(args) != 1 {
			return errors.New("validate takes no arguments")
		}
		fmt.Printf("valid: %d configured workflows\n", len(plans))
	case "list":
		if len(args) != 1 {
			return errors.New("list takes no arguments")
		}
		names := make([]string, 0, len(plans))
		for name := range plans {
			names = append(names, name)
		}
		slices.Sort(names)
		for _, name := range names {
			fmt.Printf("%s\t%s\n", name, plans[name].Digest)
		}
	case "inspect":
		if len(args) != 2 {
			return errors.New("inspect requires a workflow name")
		}
		plan, exists := plans[args[1]]
		if !exists {
			return errors.New("unknown configured workflow")
		}
		return json.NewEncoder(os.Stdout).Encode(plan)
	default:
		return errors.New("unknown workflows command")
	}
	return nil
}
