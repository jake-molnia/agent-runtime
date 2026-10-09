package command

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/hatchet-dev/hatchet/pkg/client"
	"github.com/hatchet-dev/hatchet/pkg/client/rest"
	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
	"github.com/jake-molnia/agent-runtime/hatchetbridge"
)

type runClient interface {
	GetDetails(context.Context, uuid.UUID) (*client.RunDetails, error)
	Cancel(context.Context, rest.V1CancelTaskRequest) (*rest.V1CancelledTasks, error)
}

func runsCommand(ctx context.Context, args []string) error {
	if len(args) < 2 {
		return errors.New("usage: agent-runtime runs inspect|result|cancel RUN_ID [--wait] [--text]")
	}
	if args[0] != "inspect" && args[0] != "result" && args[0] != "cancel" {
		return errors.New("unknown runs command")
	}
	id, err := uuid.Parse(args[1])
	if err != nil || id == uuid.Nil {
		return errors.New("run ID must be a nonzero UUID")
	}
	flags := flag.NewFlagSet("runs "+args[0], flag.ContinueOnError)
	wait := flags.Bool("wait", false, "wait for the result")
	text := flags.Bool("text", false, "print result report as text")
	if err := flags.Parse(args[2:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || (args[0] != "result" && flags.NFlag() > 0) {
		return errors.New("--wait and --text are supported only by runs result")
	}
	connection, err := hatchet.NewClient()
	if err != nil {
		return err
	}
	defer connection.Close(context.Background())
	return executeRunCommand(ctx, connection.Runs(), args[0], id, *wait, *text, os.Stdout)
}

func executeRunCommand(ctx context.Context, runs runClient, operation string, id uuid.UUID, wait, text bool, output io.Writer) error {
	switch operation {
	case "result":
		return printRunResult(ctx, runs, id, wait, text, output)
	case "inspect":
		details, err := runs.GetDetails(ctx, id)
		if err != nil {
			return err
		}
		if details == nil {
			return errors.New("run details unavailable")
		}
		return json.NewEncoder(output).Encode(details)
	case "cancel":
		ids := []uuid.UUID{id}
		result, err := runs.Cancel(ctx, rest.V1CancelTaskRequest{ExternalIds: &ids})
		if err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(result)
	default:
		return errors.New("unknown runs command")
	}
}

func waitRunResult(ctx context.Context, runs runClient, id uuid.UUID, wait bool, interval time.Duration) (json.RawMessage, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		details, err := runs.GetDetails(ctx, id)
		if err != nil {
			return nil, err
		}
		if details == nil {
			return nil, errors.New("run details unavailable")
		}
		switch details.Status {
		case rest.V1TaskStatusFAILED:
			return nil, errors.New("workflow run failed; use runs inspect for details")
		case rest.V1TaskStatusCANCELLED:
			return nil, errors.New("workflow run was canceled")
		case rest.V1TaskStatusCOMPLETED:
			task := details.TaskRuns["result"]
			if task == nil || task.Status != rest.V1TaskStatusCOMPLETED {
				return nil, errors.New("completed workflow has no completed result task")
			}
			var result hatchetbridge.ConfiguredResult
			if err := json.Unmarshal(task.Output, &result); err != nil || len(result.Value) == 0 || !json.Valid(result.Value) {
				return nil, errors.New("workflow result has no valid JSON value")
			}
			return result.Value, nil
		case rest.V1TaskStatusQUEUED, rest.V1TaskStatusRUNNING:
			if !wait {
				return nil, fmt.Errorf("workflow run is %s; use --wait to wait for its result", details.Status)
			}
		default:
			return nil, fmt.Errorf("unknown workflow status: %s", details.Status)
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func printRunResult(ctx context.Context, runs runClient, id uuid.UUID, wait, text bool, output io.Writer) error {
	value, err := waitRunResult(ctx, runs, id, wait, time.Second)
	if err != nil {
		return err
	}
	if !text {
		return json.NewEncoder(output).Encode(value)
	}
	var plain string
	if err := json.Unmarshal(value, &plain); err == nil {
		_, err = fmt.Fprintln(output, plain)
		return err
	}
	var envelope struct {
		Report *string `json:"report"`
	}
	if err := json.Unmarshal(value, &envelope); err != nil || envelope.Report == nil {
		return errors.New("text output requires a JSON string or an object with a report string")
	}
	_, err = fmt.Fprintln(output, *envelope.Report)
	return err
}
