package command

import (
	"context"
	"errors"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
	"github.com/jake-molnia/agent-runtime/artifacts"
	"github.com/jake-molnia/agent-runtime/githubreview"
	"github.com/jake-molnia/agent-runtime/hatchetbridge"
	"github.com/jake-molnia/agent-runtime/messages"
	"github.com/jake-molnia/agent-runtime/orchestration"
	"github.com/jake-molnia/agent-runtime/sandbox"
	"github.com/jake-molnia/agent-runtime/tailnet"
	"github.com/jake-molnia/agent-runtime/telemetry"
	"github.com/jake-molnia/agent-runtime/workflows"
	"golang.org/x/sync/errgroup"
)

func worker(ctx context.Context) error {
	catalog, err := loadCatalog()
	if err != nil {
		return err
	}
	snapshots := env("AGENT_SNAPSHOT_DIR", "/state/definitions")
	if err := catalog.Save(snapshots); err != nil {
		return err
	}
	plans, err := workflows.Load(env("AGENT_DEFINITIONS_DIR", "/config"), catalog)
	if err != nil {
		return err
	}
	if len(plans) == 0 {
		return errors.New("no workflows configured in workflows/; the worker has no baked-in workflows")
	}
	for _, plan := range plans {
		if err := workflows.Save(snapshots, plan); err != nil {
			return err
		}
	}
	key, err := os.ReadFile(env("AGENT_SECRET_KEY_FILE", "/secrets/runtime-key"))
	if err != nil || len(key) < 32 {
		return errors.New("runtime secret key must contain at least 32 bytes")
	}
	defs := make(map[string]orchestration.Definition, len(catalog.Agents))
	for name := range catalog.Agents {
		definition, err := catalog.Resolve(name)
		if err != nil {
			return err
		}
		defs[name] = definition
	}
	namespaces := []string{}
	tailnetEnabled := os.Getenv("TAILSCALE_CLIENT_ID") != ""
	for _, d := range defs {
		namespaces = append(namespaces, d.Namespace)
		tailnetEnabled = tailnetEnabled || len(d.Tags) > 0
	}
	slices.Sort(namespaces)
	namespaces = slices.Compact(namespaces)
	var scope string
	if tailnetEnabled {
		scope, err = tailnet.NewScope(os.Getenv("AGENT_DEPLOYMENT_ID"), namespaces)
		if err != nil {
			return errors.New("AGENT_DEPLOYMENT_ID and configured namespaces required for tailnet ownership")
		}
	}
	config, err := sandbox.Config()
	if err != nil {
		return err
	}
	control, err := sandbox.NewControl(config)
	if err != nil {
		return err
	}
	tel, err := telemetry.New(ctx, telemetry.Options{Service: "agent-worker", OTLPTraces: os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "", OTLPMetrics: os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") != "", Prometheus: true})
	if err != nil {
		return err
	}
	defer func() {
		flush, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = tel.Shutdown(flush)
	}()
	engine := &orchestration.Engine{Control: control, Telemetry: tel, SecretKey: key, Tailnet: &tailnet.Client{Scope: scope, ClientID: os.Getenv("TAILSCALE_CLIENT_ID"), ClientSecret: func(context.Context) (string, error) {
		b, err := os.ReadFile(env("TAILSCALE_CLIENT_SECRET_FILE", "/tailscale/client-secret"))
		return strings.TrimSpace(string(b)), err
	}}}
	if dir := os.Getenv("AGENT_ARTIFACT_DIR"); dir != "" {
		engine.Artifacts = artifacts.Directory{Root: dir}
	}
	for _, agent := range catalog.Agents {
		if len(agent.Schema) > 0 && engine.Artifacts == nil {
			return errors.New("structured agents require AGENT_ARTIFACT_DIR")
		}
	}
	client, err := hatchet.NewClient()
	if err != nil {
		return err
	}
	defer client.Close(context.Background())
	reviewConfig, reviewsEnabled, err := optionalReviewConfig()
	if err != nil {
		return err
	}
	var handler *githubreview.Handler
	reloadReview := func() (githubreview.Integration, error) { return githubreview.LoadIntegration(reviewConfigPath()) }
	if reviewsEnabled {
		var closeReview func()
		handler, closeReview, err = reviewHandler(ctx, reviewConfig)
		if err != nil {
			return err
		}
		defer closeReview()
	}
	var reviewChild *hatchet.Workflow
	registered := make([]hatchet.WorkflowBase, 0, len(plans))
	for _, plan := range plans {
		hooks := []hatchetbridge.ConfiguredHooks{}
		if reviewsEnabled && plan.Workflow.Name == reviewConfig.Workflow {
			hooks = append(hooks, hatchetbridge.ReviewBeforeStep(reloadReview, handler))
		}
		workflow, err := hatchetbridge.RegisterConfiguredWorkflow(client, engine, messages.Directory{Root: env("AGENT_MESSAGE_DIR", "/state/messages")}, snapshots, plan, hooks...)
		if err != nil {
			return err
		}
		registered = append(registered, workflow)
		if reviewsEnabled && plan.Workflow.Name == reviewConfig.Workflow {
			reviewChild = workflow
		}
	}
	var webhook http.Handler
	labels := map[string]any{}
	if reviewsEnabled {
		if reviewChild == nil {
			return errors.New("configured review workflow missing")
		}
		adapter, err := hatchetbridge.RegisterReview(client, reviewConfig, reloadReview, handler, reviewChild, plans[reviewConfig.Workflow], engine, messages.Directory{Root: env("AGENT_MESSAGE_DIR", "/state/messages")})
		if err != nil {
			return err
		}
		ingress, err := hatchetbridge.RegisterReviewIngress(client, reviewConfig, adapter, plans[reviewConfig.Workflow].Digest, reloadReview, handler)
		if err != nil {
			return err
		}
		registered = append(registered, adapter, ingress)
		webhook, err = reviewWebhook(reviewConfig, ingress)
		if err != nil {
			return err
		}
		for k, v := range reviewConfig.WorkerLabels {
			labels[k] = v
		}
	}
	slots, err := strconv.Atoi(env("AGENT_WORKER_SLOTS", "4"))
	if err != nil || slots < 1 {
		return errors.New("invalid worker slots")
	}
	workerName := env("AGENT_WORKER_NAME", "agent-runtime") + "-" + uuid.NewString()
	worker, err := client.NewWorker(workerName, hatchet.WithWorkflows(registered...), hatchet.WithSlots(slots), hatchet.WithDurableSlots(slots), hatchet.WithLabels(labels))
	if err != nil {
		return err
	}
	instrument, err := hatchetbridge.Instrument(ctx, tel)
	if err != nil {
		return err
	}
	worker.Use(instrument.Middleware())
	health := &workerHealth{}
	mux := http.NewServeMux()
	for _, path := range []string{"/healthz", "/startupz", "/readyz"} {
		mux.Handle(path, health)
	}
	mux.Handle("/metrics", tel.Handler)
	if webhook != nil {
		mux.Handle("/webhooks/github", webhook)
	}
	workerCtx, stopWorker := context.WithCancel(ctx)
	defer stopWorker()
	g, ctx := errgroup.WithContext(workerCtx)
	if os.Getenv("TAILSCALE_CLIENT_ID") != "" {
		g.Go(func() error {
			tick := time.NewTicker(5 * time.Minute)
			defer tick.Stop()
			for {
				select {
				case <-ctx.Done():
					return nil
				case <-tick.C:
					cleanup, cancel := context.WithTimeout(ctx, 30*time.Second)
					_ = reapTailnet(cleanup, control, engine.Tailnet, namespaces)
					cancel()
				}
			}
		})
	}
	g.Go(func() error { return health.monitor(ctx, workerName, client.Workers().List) })
	g.Go(func() error { return httpServer(ctx, ":9091", mux) })
	g.Go(func() error { defer stopWorker(); return worker.StartBlocking(ctx) })
	return g.Wait()
}

// Never pass a partial inventory to the destructive device reaper.
func reapTailnet(ctx context.Context, control *sandbox.Control, client *tailnet.Client, namespaces []string) error {
	active, err := control.ActiveClaims(ctx, namespaces)
	if err != nil {
		return err
	}
	return client.Reap(ctx, active)
}
