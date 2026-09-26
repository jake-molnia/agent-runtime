package command

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
	"github.com/jake-molnia/agent-runtime/artifacts"
	"github.com/jake-molnia/agent-runtime/hatchetbridge"
	"github.com/jake-molnia/agent-runtime/orchestration"
	"github.com/jake-molnia/agent-runtime/sandbox"
	"github.com/jake-molnia/agent-runtime/tailnet"
	"github.com/jake-molnia/agent-runtime/telemetry"
	"golang.org/x/sync/errgroup"
)

type definitionConfig struct {
	AllowProjectConfig bool              `json:"allow_project_config"`
	Pool               string            `json:"pool"`
	Namespace          string            `json:"namespace"`
	Agent              string            `json:"agent"`
	Model              map[string]string `json:"model"`
	Directory          string            `json:"directory"`
	TimeoutSeconds     int               `json:"timeout_seconds"`
	Tags               []string          `json:"tags"`
	Config             json.RawMessage   `json:"config"`
	SecretFiles        map[string]string `json:"secret_files"`
	Prepare            []string          `json:"prepare"`
}

func worker(ctx context.Context) error {
	var configured map[string]definitionConfig
	data, err := os.ReadFile(env("AGENT_DEFINITIONS_FILE", "/config/agents.json"))
	if err != nil {
		return errors.New("agent definitions unavailable")
	}
	if err = json.Unmarshal(data, &configured); err != nil {
		return errors.New("invalid agent definitions")
	}
	key, err := os.ReadFile(env("AGENT_SECRET_KEY_FILE", "/secrets/runtime-key"))
	if err != nil || len(key) < 32 {
		return errors.New("runtime secret key must contain at least 32 bytes")
	}
	defs := make(map[string]orchestration.Definition, len(configured))
	for name, cfg := range configured {
		if cfg.TimeoutSeconds < 1 || cfg.TimeoutSeconds > 86400 || cfg.Pool == "" || cfg.Namespace == "" || cfg.Directory == "" || !json.Valid(cfg.Config) {
			return errors.New("invalid agent definition")
		}
		defs[name] = orchestration.Definition{AllowProjectConfig: cfg.AllowProjectConfig, Pool: cfg.Pool, Namespace: cfg.Namespace, Agent: cfg.Agent, Model: cfg.Model, Directory: cfg.Directory, Timeout: time.Duration(cfg.TimeoutSeconds) * time.Second, Tags: cfg.Tags,
			Config: func(secrets map[string]string) (json.RawMessage, error) { return runtimeConfig(cfg.Config, secrets) },
			Secrets: func(ctx context.Context) (map[string]string, error) {
				out := map[string]string{}
				for k, path := range cfg.SecretFiles {
					b, err := os.ReadFile(path)
					if err != nil {
						return nil, errors.New("agent secret unavailable")
					}
					out[k] = strings.TrimSpace(string(b))
				}
				return out, nil
			},
		}
		if len(cfg.Prepare) > 0 {
			d := defs[name]
			d.Prepare = orchestration.Command(cfg.Prepare, "/workspace")
			defs[name] = d
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
	engine := &orchestration.Engine{Control: control, Telemetry: tel, SecretKey: key, Tailnet: &tailnet.Client{ClientID: os.Getenv("TAILSCALE_CLIENT_ID"), ClientSecret: func(context.Context) (string, error) {
		b, err := os.ReadFile(env("TAILSCALE_CLIENT_SECRET_FILE", "/tailscale/client-secret"))
		return strings.TrimSpace(string(b)), err
	}}}
	if dir := os.Getenv("AGENT_ARTIFACT_DIR"); dir != "" {
		engine.Artifacts = artifacts.Directory{Root: dir}
	}
	client, err := hatchet.NewClient()
	if err != nil {
		return err
	}
	defer client.Close(context.Background())
	workflow := hatchetbridge.Register(client, engine, defs)
	slots, err := strconv.Atoi(env("AGENT_WORKER_SLOTS", "4"))
	if err != nil || slots < 1 {
		return errors.New("invalid worker slots")
	}
	worker, err := client.NewWorker(env("AGENT_WORKER_NAME", "agent-runtime"), hatchet.WithWorkflows(workflow), hatchet.WithSlots(slots), hatchet.WithDurableSlots(slots))
	if err != nil {
		return err
	}
	instrument, err := hatchetbridge.Instrument(ctx, tel)
	if err != nil {
		return err
	}
	worker.Use(instrument.Middleware())
	mux := http.NewServeMux()
	mux.Handle("/metrics", tel.Handler)
	workerCtx, stopWorker := context.WithCancel(ctx)
	defer stopWorker()
	g, ctx := errgroup.WithContext(workerCtx)
	if os.Getenv("TAILSCALE_CLIENT_ID") != "" {
		namespaces := []string{}
		seen := map[string]bool{}
		for _, d := range defs {
			if !seen[d.Namespace] {
				seen[d.Namespace] = true
				namespaces = append(namespaces, d.Namespace)
			}
		}
		g.Go(func() error {
			tick := time.NewTicker(5 * time.Minute)
			defer tick.Stop()
			for {
				select {
				case <-ctx.Done():
					return nil
				case <-tick.C:
					cleanup, cancel := context.WithTimeout(ctx, 30*time.Second)
					active, err := control.ActiveClaims(cleanup, namespaces)
					if err == nil {
						_ = engine.Tailnet.Reap(cleanup, active)
					}
					cancel()
				}
			}
		})
	}
	g.Go(func() error { return httpServer(ctx, ":9091", mux) })
	g.Go(func() error { defer stopWorker(); return worker.StartBlocking(ctx) })
	return g.Wait()
}

// Secret placeholders occupy a whole JSON value: {"$secret":"APERTURE_API_KEY"}.
func runtimeConfig(raw json.RawMessage, secrets map[string]string) (json.RawMessage, error) {
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, err
	}
	var resolve func(any) (any, error)
	resolve = func(v any) (any, error) {
		switch node := v.(type) {
		case map[string]any:
			if name, ok := node["$secret"].(string); ok && len(node) == 1 {
				secret, found := secrets[name]
				if !found {
					return nil, errors.New("configuration secret unavailable")
				}
				return secret, nil
			}
			for key, item := range node {
				next, err := resolve(item)
				if err != nil {
					return nil, err
				}
				node[key] = next
			}
		case []any:
			for i, item := range node {
				next, err := resolve(item)
				if err != nil {
					return nil, err
				}
				node[i] = next
			}
		}
		return v, nil
	}
	out, err := resolve(value)
	if err != nil {
		return nil, err
	}
	return json.Marshal(out)
}
