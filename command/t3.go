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
	"net/url"
	"os"
	"regexp"
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
	config.QPS, config.Burst = 20, 40
	control, err := sandbox.NewControl(config)
	if err != nil {
		return err
	}
	controller, err := lifecycle.New(control, profiles, key)
	if err != nil {
		return err
	}
	controller.PoolID = os.Getenv("T3_POOL_ID")
	if controller.PoolID != "" && !regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`).MatchString(controller.PoolID) {
		return errors.New("invalid T3_POOL_ID")
	}
	controller.PublicURL = strings.TrimRight(os.Getenv("T3_PUBLIC_URL"), "/")
	if controller.PublicURL != "" {
		u, err := url.Parse(controller.PublicURL)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("invalid T3_PUBLIC_URL")
		}
	}
	if domain := os.Getenv("T3_IDE_DOMAIN"); domain != "" {
		config := lifecycle.IDEConfig{Domain: domain, ProxyGroup: os.Getenv("T3_IDE_PROXY_GROUP"), BackendService: "t3-runtime", BackendPort: 8084, CallbackURL: os.Getenv("T3_IDE_CALLBACK_URL"), Tags: strings.Split(os.Getenv("T3_IDE_TAGS"), ",")}
		if err := config.Validate(); err != nil {
			return err
		}
		controller.IDE = &config
	}
	controller.Resources = lifecycle.Resources(control.API, os.Getenv("T3_NODE_SELECTOR"), os.Getenv("T3_RESOURCE_NODE"))
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
