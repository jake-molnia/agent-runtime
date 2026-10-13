package command

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"

	"github.com/jake-molnia/agent-runtime/packs"
	"github.com/jake-molnia/agent-runtime/workflows"
)

func workflowsCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: agent-runtime workflows list|validate|inspect NAME|templates|init PRESET NAME")
	}
	if args[0] == "templates" {
		if len(args) != 1 {
			return errors.New("templates takes no arguments")
		}
		for _, name := range packs.Names() {
			fmt.Println(name)
		}
		return nil
	}
	if args[0] == "init" {
		if len(args) != 3 {
			return errors.New("init requires a preset and workflow name")
		}
		path, err := packs.Write(env("AGENT_DEFINITIONS_DIR", "/config"), args[1], args[2])
		if err != nil {
			return err
		}
		fmt.Println(path)
		return nil
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
