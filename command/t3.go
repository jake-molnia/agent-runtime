package command

import (
	"context"
	"encoding/json"
	"errors"
	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
	"github.com/jake-molnia/agent-runtime/hatchetbridge"
	"github.com/jake-molnia/agent-runtime/lifecycle"
	"github.com/jake-molnia/agent-runtime/sandbox"
	"golang.org/x/sync/errgroup"
	"os"
	"strings"
	"time"
)

func t3Worker(ctx context.Context) error {
	data, err := os.ReadFile(env("T3_PROFILES_FILE", "/config/t3-profiles.json"))
	if err != nil {
		return errors.New("T3 profiles unavailable")
	}
	var profiles map[string]lifecycle.Profile
	if err = json.Unmarshal(data, &profiles); err != nil {
		return errors.New("invalid T3 profiles")
	}
	key, err := os.ReadFile(env("AGENT_SECRET_KEY_FILE", "/secrets/runtime-key"))
	if err != nil {
		return errors.New("runtime key unavailable")
	}
	apiToken, err := os.ReadFile(env("T3_CONTROL_TOKEN_FILE", "/secrets/t3-control-token"))
	apiToken = []byte(strings.TrimSpace(string(apiToken)))
	if err != nil || len(apiToken) < 32 {
		return errors.New("T3 control token requires at least 32 bytes")
	}
	config, err := sandbox.Config()
	if err != nil {
		return err
	}
	control, err := sandbox.NewControl(config)
	if err != nil {
		return err
	}
	controller, err := lifecycle.New(control, profiles, key)
	if err != nil {
		return err
	}
	client, err := hatchet.NewClient()
	if err != nil {
		return err
	}
	defer func() {
		bounded, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = client.Close(bounded)
	}()
	workflow := hatchetbridge.RegisterT3(client, controller)
	worker, err := client.NewWorker(env("T3_WORKER_NAME", "t3-agent-runtime"), hatchet.WithWorkflows(workflow), hatchet.WithSlots(8))
	if err != nil {
		return err
	}
	workerContext, stop := context.WithCancel(ctx)
	defer stop()
	g, ctx := errgroup.WithContext(workerContext)
	g.Go(func() error { defer stop(); return worker.StartBlocking(ctx) })
	g.Go(func() error {
		return httpServer(ctx, env("T3_CONTROL_ADDR", ":8084"), lifecycle.Handler(controller, hatchetbridge.T3Tasks{Client: client, Workflow: workflow}, strings.TrimSpace(string(apiToken))))
	})
	return g.Wait()
}
