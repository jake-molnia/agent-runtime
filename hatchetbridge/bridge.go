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
	Agent string                `json:"agent"`
	Run   orchestration.Request `json:"run"`
}
type Output struct {
	Prepared orchestration.Prepared `json:"prepared"`
	Result   orchestration.Result   `json:"result"`
	Artifact string                 `json:"artifact,omitempty"`
}

// Register retains completed sandboxes until their lease expiry so the app can retrieve results.
// Applications may call Engine.Cleanup after persisting the native session export/artifacts.
func Register(client *hatchet.Client, engine *orchestration.Engine, definitions map[string]orchestration.Definition) *hatchet.Workflow {
	workflow := client.NewWorkflow("agent-run")
	provision := workflow.NewTask("provision", func(ctx hatchet.Context, input Input) (orchestration.Prepared, error) {
		d, ok := definitions[input.Agent]
		if !ok {
			return orchestration.Prepared{}, errors.New("unknown agent definition")
		}
		input.Run.Key = ctx.WorkflowRunId()
		if input.Run.SubmittedAt.IsZero() {
			input.Run.SubmittedAt = time.Now()
		}
		ctx.Log("Provisioning sandbox, credentials and identity")
		bounded, cancel := context.WithTimeout(ctx.GetContext(), 3*time.Minute)
		defer cancel()
		out, err := engine.Provision(bounded, d, input.Run)
		if err != nil {
			return out, errors.New("agent provisioning failed")
		}
		ctx.Log("Sandbox and OpenCode ready")
		return out, nil
	}, hatchet.WithRetries(0), hatchet.WithExecutionTimeout(4*time.Minute), hatchet.WithScheduleTimeout(time.Hour))
	execute := workflow.NewDurableTask("execute", func(ctx hatchet.DurableContext, input Input) (Output, error) {
		d, ok := definitions[input.Agent]
		if !ok {
			return Output{}, errors.New("unknown agent definition")
		}
		var prepared orchestration.Prepared
		if err := ctx.StepOutput("provision", &prepared); err != nil {
			return Output{}, err
		}
		input.Run.Key = ctx.WorkflowRunId()
		input.Run.SubmittedAt = prepared.Started
		ctx.Log("Running agent session")
		result, err := engine.Execute(ctx.GetContext(), d, input.Run, prepared, func(waitCtx context.Context, kind string) error {
			// Recheck on a bounded durable timeout even if an app notification was lost.
			now, err := ctx.Now()
			if err != nil {
				return err
			}
			_, err = ctx.WaitFor(hatchet.OrCondition(hatchet.UserEventCondition("agent:interaction", "", hatchet.WithEventScope(prepared.SessionID), hatchet.WithConsiderEventsSince(now.Add(-time.Minute))), hatchet.SleepCondition(30*time.Second)))
			return err
		})
		if err != nil {
			return Output{Prepared: prepared, Result: result}, errors.New("agent execution did not complete")
		}
		summary, _ := json.Marshal(map[string]string{"event": "agent.completed", "session_id": prepared.SessionID, "status": result.Status})
		ctx.Log(string(summary))
		return Output{Prepared: prepared, Result: result}, nil
	}, hatchet.WithParents(provision), hatchet.WithRetries(0), hatchet.WithExecutionTimeout(24*time.Hour), hatchet.WithScheduleTimeout(time.Hour))
	if engine.Artifacts != nil {
		collect := workflow.NewTask("collect", func(ctx hatchet.Context, input Input) (Output, error) {
			var out Output
			if err := ctx.StepOutput("execute", &out); err != nil {
				return out, err
			}
			input.Run.Key = ctx.WorkflowRunId()
			ref, err := engine.Collect(ctx.GetContext(), input.Run, out.Prepared)
			out.Artifact = ref
			return out, err
		}, hatchet.WithParents(execute), hatchet.WithRetries(2), hatchet.WithExecutionTimeout(2*time.Minute))
		workflow.NewTask("cleanup", func(ctx hatchet.Context, input Input) (Output, error) {
			var out Output
			if err := ctx.StepOutput("collect", &out); err != nil {
				return out, err
			}
			input.Run.Key = ctx.WorkflowRunId()
			return out, engine.Cleanup(ctx.GetContext(), input.Run, out.Prepared)
		}, hatchet.WithParents(collect), hatchet.WithRetries(2), hatchet.WithExecutionTimeout(time.Minute))
	}
	workflow.OnFailure(func(ctx hatchet.Context, input Input) (map[string]string, error) {
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
		if err := engine.Cancel(cleanup, d, input.Run); err != nil {
			return nil, errors.New("agent cleanup pending lease expiry")
		}
		return map[string]string{"status": "cleaned"}, nil
	})
	return workflow
}

// NotifyInteraction wakes the owning workflow after an authorized native permission/form reply.
func NotifyInteraction(ctx context.Context, client *hatchet.Client, sessionID string) error {
	return client.Events().Push(ctx, "agent:interaction", map[string]string{"session_id": sessionID}, hatchet.WithFilterScope(&sessionID))
}
