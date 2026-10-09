package command

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"os"
	"regexp"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
	"github.com/jake-molnia/agent-runtime/hatchetbridge"
)

var workflowName = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,62}$`)

func submitCommand(ctx context.Context, args []string) error {
	if len(args) == 0 || !workflowName.MatchString(args[0]) {
		return errors.New("usage: agent-runtime submit WORKFLOW --input FILE")
	}
	flags := flag.NewFlagSet("submit", flag.ContinueOnError)
	path := flags.String("input", "", "JSON workflow input file")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if *path == "" || flags.NArg() != 0 {
		return errors.New("submit requires --input FILE")
	}
	data, err := submissionData(*path)
	if err != nil {
		return err
	}
	client, err := hatchet.NewClient()
	if err != nil {
		return err
	}
	defer client.Close(context.Background())
	var input hatchetbridge.ConfiguredInput
	if err := json.Unmarshal(data, &input); err != nil || len(input.Digest) != 64 {
		return errors.New("configured workflow input requires snapshot digest")
	}
	run, err := client.RunNoWait(ctx, hatchetbridge.ConfiguredWorkflowName(args[0], input.Digest), data)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]string{"run_id": run.RunId, "workflow": args[0]})
}

func submissionData(path string) (json.RawMessage, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("workflow input file unavailable")
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 1<<20+1))
	if err != nil || len(data) > 1<<20 {
		return nil, errors.New("workflow input exceeds 1 MiB or cannot be read")
	}
	var object map[string]json.RawMessage
	if err := strictJSON(data, &object); err != nil || object == nil {
		return nil, errors.New("workflow input must be one JSON object")
	}
	return json.RawMessage(data), nil
}
