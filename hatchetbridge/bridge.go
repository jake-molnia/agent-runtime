// Package hatchetbridge registers agent lifecycles with durable interaction waits.
package hatchetbridge

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/hatchet-dev/hatchet/pkg/client/loader"
	hatchet "github.com/hatchet-dev/hatchet/sdks/go"
	hotel "github.com/hatchet-dev/hatchet/sdks/go/opentelemetry"
	"github.com/jake-molnia/agent-runtime/orchestration"
	"github.com/jake-molnia/agent-runtime/telemetry"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"google.golang.org/grpc/credentials"
)

// Instrument exports sanitized lifecycle spans to Hatchet's collector using its existing connection settings.
func Instrument(ctx context.Context, t *telemetry.Telemetry) (*hotel.Instrumentor, error) {
	cfg, err := loader.LoadClientConfigFile()
	if err != nil {
		return nil, err
	}
	client, err := loader.GetClientConfigFromConfigFile(nil, cfg)
	if err != nil {
		return nil, err
	}
	opts := []otlptracegrpc.Option{otlptracegrpc.WithEndpoint(client.GRPCBroadcastAddress), otlptracegrpc.WithHeaders(map[string]string{"authorization": "Bearer " + client.Token}), otlptracegrpc.WithTimeout(5 * time.Second)}
	if client.TLSConfig == nil {
		opts = append(opts, otlptracegrpc.WithInsecure())
	} else {
		opts = append(opts, otlptracegrpc.WithTLSCredentials(credentials.NewTLS(client.TLSConfig)))
	}
	exporter, err := otlptracegrpc.New(ctx, opts...)
	if err != nil {
		return nil, err
	}
	processor := sdktrace.NewBatchSpanProcessor(telemetry.SafeExporter(exporter), sdktrace.WithMaxQueueSize(512), sdktrace.WithMaxExportBatchSize(64), sdktrace.WithExportTimeout(5*time.Second))
	t.Traces.RegisterSpanProcessor(hotel.NewHatchetAttributeSpanProcessor(processor))
	return hotel.NewInstrumentor(hotel.WithTracerProvider(t.Traces), hotel.DisableHatchetCollector())
}

type Input struct {
	Agent    string                `json:"agent"`
	Run      orchestration.Request `json:"run"`
	Digest   string                `json:"digest,omitempty"`
	Review   json.RawMessage       `json:"review,omitempty"`
	GroupKey string                `json:"group_key,omitempty"`
}
type Output struct {
	Prepared orchestration.Prepared `json:"prepared"`
	Result   orchestration.Result   `json:"result"`
	Artifact string                 `json:"artifact,omitempty"`
	Data     json.RawMessage        `json:"data,omitempty"`
}

func registerLifecycle(client *hatchet.Client, engine *orchestration.Engine, lifecycle Lifecycle) *hatchet.Workflow {
	options := []hatchet.WorkflowOption{}
	if lifecycle.Automation {
		limit, strategy := int32(1), hatchet.GroupRoundRobin
		options = append(options, hatchet.WithWorkflowConcurrency(hatchet.Concurrency{Expression: "input.group_key", MaxRuns: &limit, LimitStrategy: &strategy}))
	}
	workflow := client.NewWorkflow(lifecycle.Name, options...)
	resolve := workflow.NewTask("resolve", func(ctx hatchet.Context, input Input) (Spec, error) {
		return lifecycle.resolve(ctx.GetContext(), input, ctx.WorkflowRunId())
	}, hatchet.WithRetries(0), hatchet.WithExecutionTimeout(time.Minute))
	provision := workflow.NewTask("provision", func(ctx hatchet.Context, input Input) (orchestration.Prepared, error) {
		var spec Spec
		if err := ctx.StepOutput("resolve", &spec); err != nil {
			return orchestration.Prepared{}, err
		}
		if spec.Skip {
			return orchestration.Prepared{}, nil
		}
		d, err := lifecycle.Definition(spec)
		if err != nil {
			return orchestration.Prepared{}, err
		}
		ctx.Log("Provisioning sandbox, credentials and identity")
		bounded, cancel := context.WithTimeout(ctx.GetContext(), 3*time.Minute)
		defer cancel()
		out, err := engine.Provision(bounded, d, spec.Run)
		if err != nil {
			return out, errors.New("agent provisioning failed")
		}
		ctx.Log("Sandbox and OpenCode ready")
		return out, nil
	}, hatchet.WithParents(resolve), hatchet.WithRetries(0), hatchet.WithExecutionTimeout(4*time.Minute), hatchet.WithScheduleTimeout(time.Hour))
	execute := workflow.NewDurableTask("execute", func(ctx hatchet.DurableContext, input Input) (Output, error) {
		var spec Spec
		if err := ctx.StepOutput("resolve", &spec); err != nil {
			return Output{}, err
		}
		if spec.Skip {
			return Output{}, nil
		}
		d, err := lifecycle.Definition(spec)
		if err != nil {
			return Output{}, err
		}
		var prepared orchestration.Prepared
		if err := ctx.StepOutput("provision", &prepared); err != nil {
			return Output{}, err
		}
		spec.Run.SubmittedAt = prepared.Started
		ctx.Log("Running agent session")
		result, err := engine.Execute(ctx.GetContext(), d, spec.Run, prepared, interactionWait(ctx, prepared.SessionID))
		if err != nil {
			return Output{Prepared: prepared, Result: result}, errors.New("agent execution did not complete")
		}
		summary, _ := json.Marshal(map[string]string{"event": "agent.completed", "session_id": prepared.SessionID, "status": result.Status})
		ctx.Log(string(summary))
		return Output{Prepared: prepared, Result: result}, nil
	}, hatchet.WithParents(resolve, provision), hatchet.WithRetries(0), hatchet.WithExecutionTimeout(24*time.Hour), hatchet.WithScheduleTimeout(time.Hour))
	if engine.Artifacts != nil {
		collect := workflow.NewTask("collect", func(ctx hatchet.Context, input Input) (Output, error) {
			var spec Spec
			if err := ctx.StepOutput("resolve", &spec); err != nil {
				return Output{}, err
			}
			if spec.Skip {
				return Output{}, nil
			}
			var out Output
			if err := ctx.StepOutput("execute", &out); err != nil {
				return out, err
			}
			ref, err := engine.Collect(ctx.GetContext(), spec.Run, out.Prepared)
			out.Artifact = ref
			return out, err
		}, hatchet.WithParents(resolve, execute), hatchet.WithRetries(2), hatchet.WithExecutionTimeout(2*time.Minute))
		parent := collect
		cleanupParents := []*hatchet.Task{resolve, collect}
		if lifecycle.Validate != nil {
			parent = workflow.NewTask("validate", func(ctx hatchet.Context, input Input) (Output, error) {
				var spec Spec
				if err := ctx.StepOutput("resolve", &spec); err != nil {
					return Output{}, err
				}
				if spec.Skip {
					return Output{}, nil
				}
				var out Output
				if err := ctx.StepOutput("collect", &out); err != nil {
					return out, err
				}
				var err error
				structured := lifecycle.Validate != nil
				if lifecycle.Structured != nil {
					structured, err = lifecycle.Structured(spec)
					if err != nil {
						return out, err
					}
				}
				if structured {
					out.Data, err = engine.ReadOutput(ctx.GetContext(), spec.Run, out.Prepared)
					if err != nil {
						return out, err
					}
					if err = lifecycle.Validate(spec, out.Data); err != nil {
						return out, err
					}
				}
				return out, nil
			}, hatchet.WithParents(resolve, collect), hatchet.WithRetries(0), hatchet.WithExecutionTimeout(2*time.Minute))
			cleanupParents = append(cleanupParents, parent)
		}
		if lifecycle.Publish != nil {
			parent = workflow.NewTask("publish", func(ctx hatchet.Context, input Input) (json.RawMessage, error) {
				var spec Spec
				if err := ctx.StepOutput("resolve", &spec); err != nil {
					return nil, err
				}
				if spec.Skip {
					return json.RawMessage(`{"status":"skipped"}`), nil
				}
				var out Output
				if err := ctx.StepOutput("validate", &out); err != nil {
					return nil, err
				}
				return lifecycle.Publish(ctx.GetContext(), spec, out.Data)
			}, hatchet.WithParents(resolve, parent), hatchet.WithRetries(2), hatchet.WithExecutionTimeout(2*time.Minute))
			cleanupParents = append(cleanupParents, parent)
		}
		workflow.NewTask("cleanup", func(ctx hatchet.Context, input Input) (Output, error) {
			var spec Spec
			if err := ctx.StepOutput("resolve", &spec); err != nil {
				return Output{}, err
			}
			if spec.Skip {
				return Output{}, nil
			}
			var out Output
			if err := ctx.StepOutput("collect", &out); err != nil {
				return out, err
			}
			return out, engine.Cleanup(ctx.GetContext(), spec.Run, out.Prepared)
		}, hatchet.WithParents(cleanupParents...), hatchet.WithRetries(2), hatchet.WithExecutionTimeout(time.Minute))
	}
	workflow.OnFailure(func(ctx hatchet.Context, input Input) (map[string]string, error) {
		var spec Spec
		if err := ctx.StepOutput("resolve", &spec); err != nil || spec.Skip {
			return map[string]string{"status": "not_provisioned"}, nil
		}
		d, err := lifecycle.Definition(spec)
		if err != nil {
			return map[string]string{"status": "retained_until_expiry", "reason": "snapshot_unavailable"}, nil
		}
		input.Agent, input.Run = spec.Agent, spec.Run
		return failureHandler(engine, map[string]orchestration.Definition{spec.Agent: d})(ctx, input)
	})
	return workflow
}

func failureHandler(engine *orchestration.Engine, definitions map[string]orchestration.Definition) func(hatchet.Context, Input) (map[string]string, error) {
	return func(ctx hatchet.Context, input Input) (map[string]string, error) {
		if ctx.StepRunErrors()["collect"] != "" {
			return map[string]string{"status": "retained_until_expiry", "reason": "artifact_export_failed"}, nil
		}
		d, ok := definitions[input.Agent]
		if !ok {
			return nil, nil
		}
		input.Run.Key = ctx.WorkflowRunId()
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx.GetContext()), 45*time.Second)
		defer cancel()
		if engine.Artifacts != nil && ctx.StepRunErrors()["provision"] == "" && ctx.StepRunErrors()["cleanup"] == "" {
			// The DAG operator supplies successful outputs but may omit step errors.
			var collected Output
			var prepared orchestration.Prepared
			var ref string
			if err := ctx.StepOutput("collect", &collected); err == nil && collected.Artifact != "" && collected.Prepared.SessionID != "" && collected.Prepared.Lease.UID != "" {
				prepared, ref = collected.Prepared, collected.Artifact
			} else {
				var executed Output
				if ctx.StepOutput("execute", &executed) == nil && executed.Prepared.SessionID != "" {
					return map[string]string{"status": "retained_until_expiry", "reason": "artifact_export_failed"}, nil
				}
				// Failed task outputs are unavailable; provision is the successful parent.
				if err := ctx.StepOutput("provision", &prepared); err != nil || prepared.SessionID == "" || prepared.Lease.Host == "" || prepared.Lease.UID == "" {
					return map[string]string{"status": "retained_until_expiry", "reason": "artifact_export_failed"}, nil
				}
				var err error
				ref, err = engine.Collect(cleanup, input.Run, prepared)
				if err != nil {
					return map[string]string{"status": "retained_until_expiry", "reason": "artifact_export_failed"}, nil
				}
			}
			if err := engine.Cleanup(cleanup, input.Run, prepared); err != nil {
				return map[string]string{"status": "cleanup_pending", "reason": "cleanup_failed", "artifact": ref}, nil
			}
			return map[string]string{"status": "cleaned", "artifact": ref}, nil
		}
		if err := engine.Cancel(cleanup, d, input.Run); err != nil {
			return nil, errors.New("agent cleanup pending lease expiry")
		}
		return map[string]string{"status": "cleaned"}, nil
	}
}

// NotifyInteraction wakes the owning workflow after an authorized native permission/form reply.
func NotifyInteraction(ctx context.Context, client *hatchet.Client, sessionID string) error {
	return client.Events().Push(ctx, "agent:interaction", map[string]string{"session_id": sessionID}, hatchet.WithFilterScope(&sessionID))
}

// Submit stamps the enqueue time so startup metrics include Hatchet's scheduling delay.
func Submit(ctx context.Context, client *hatchet.Client, agent, digest, prompt string) (*hatchet.WorkflowRunRef, error) {
	return SubmitInput(ctx, client, "agent-run", Input{Agent: agent, Digest: digest, Run: orchestration.Request{Prompt: prompt}})
}
