// Package telemetry exports lifecycle spans and low-cardinality metrics, never model payloads.
package telemetry

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	prom "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

type Phase string

const Initialize Phase = "initialize"

const (
	Claim     Phase = "claim"
	Secrets   Phase = "secrets"
	Identity  Phase = "identity_issue"
	Runtime   Phase = "runtime_ready"
	Tailnet   Phase = "tailnet_enroll"
	Checkout  Phase = "checkout"
	Harness   Phase = "harness_ready"
	Session   Phase = "session_create"
	Prompt    Phase = "prompt_admit"
	Execution Phase = "execution"
	Cleanup   Phase = "cleanup"
)

var phases = map[Phase]bool{Initialize: true, Claim: true, Secrets: true, Identity: true, Runtime: true, Tailnet: true, Checkout: true, Harness: true, Session: true, Prompt: true, Execution: true, Cleanup: true}

type Options struct {
	Service     string
	OTLPTraces  bool
	OTLPMetrics bool
	Prometheus  bool
	// Additional exporters, including Hatchet, receive the same sanitized spans.
	Exporters []sdktrace.SpanExporter
}
type Telemetry struct {
	Traces  *sdktrace.TracerProvider
	Metrics *sdkmetric.MeterProvider
	Handler http.Handler
	phase   metric.Float64Histogram
	startup metric.Float64Histogram
	runs    metric.Int64Counter
	events  metric.Int64Counter
}

func New(ctx context.Context, o Options) (*Telemetry, error) {
	if o.Service == "" {
		o.Service = "agent-runtime"
	}
	res := resource.NewSchemaless(attribute.String("service.name", o.Service))
	traceOpts := []sdktrace.TracerProviderOption{sdktrace.WithResource(res), sdktrace.WithSpanLimits(sdktrace.SpanLimits{AttributeCountLimit: 32, AttributeValueLengthLimit: 256, EventCountLimit: 0, LinkCountLimit: 8})}
	exporters := append([]sdktrace.SpanExporter{}, o.Exporters...)
	if o.OTLPTraces {
		e, err := otlptracehttp.New(ctx)
		if err != nil {
			return nil, err
		}
		exporters = append(exporters, e)
	}
	for _, e := range exporters {
		traceOpts = append(traceOpts, sdktrace.WithBatcher(SafeExporter(e), sdktrace.WithMaxQueueSize(512), sdktrace.WithMaxExportBatchSize(64), sdktrace.WithExportTimeout(5*time.Second)))
	}
	t := &Telemetry{Traces: sdktrace.NewTracerProvider(traceOpts...)}
	metricOpts := []sdkmetric.Option{sdkmetric.WithResource(res)}
	fail := func(err error) (*Telemetry, error) { _ = t.Traces.Shutdown(ctx); return nil, err }
	if o.OTLPMetrics {
		e, err := otlpmetrichttp.New(ctx)
		if err != nil {
			return fail(err)
		}
		metricOpts = append(metricOpts, sdkmetric.WithReader(sdkmetric.NewPeriodicReader(e, sdkmetric.WithInterval(30*time.Second))))
	}
	if o.Prometheus {
		registry := prometheus.NewRegistry()
		registry.MustRegister(prometheus.NewGoCollector(), prometheus.NewProcessCollector(prometheus.ProcessCollectorOpts{}))
		e, err := prom.New(prom.WithRegisterer(registry))
		if err != nil {
			return fail(err)
		}
		metricOpts = append(metricOpts, sdkmetric.WithReader(e))
		t.Handler = promhttp.HandlerFor(registry, promhttp.HandlerOpts{})
	}
	t.Metrics = sdkmetric.NewMeterProvider(metricOpts...)
	m := t.Metrics.Meter("agent-runtime")
	var err error
	t.phase, err = m.Float64Histogram("agent.phase.duration", metric.WithUnit("s"), metric.WithExplicitBucketBoundaries(.01, .025, .05, .1, .25, .5, 1, 2, 5, 10, 20, 30, 60, 120, 300, 900, 1800))
	if err != nil {
		return nil, err
	}
	t.startup, err = m.Float64Histogram("agent.startup.duration", metric.WithUnit("s"), metric.WithExplicitBucketBoundaries(.1, .25, .5, 1, 2, 5, 10, 20, 30, 60, 120, 300))
	if err != nil {
		return nil, err
	}
	t.runs, err = m.Int64Counter("agent.runs")
	if err != nil {
		return nil, err
	}
	t.events, err = m.Int64Counter("agent.events")
	if err != nil {
		return nil, err
	}
	return t, nil
}
func (t *Telemetry) Shutdown(ctx context.Context) error {
	return errors.Join(t.Traces.Shutdown(ctx), t.Metrics.Shutdown(ctx))
}

type Run struct {
	t          *Telemetry
	started    time.Time
	attrs      []attribute.KeyValue
	mu         sync.Mutex
	milestones map[string]bool
	phaseCount map[Phase]int
}

func (t *Telemetry) Run(started time.Time, pool, launch string) *Run {
	if started.IsZero() {
		started = time.Now()
	}
	if launch != "warm" && launch != "cold" {
		launch = "unknown"
	}
	return &Run{t: t, started: started, attrs: []attribute.KeyValue{attribute.String("sandbox.pool", pool), attribute.String("sandbox.launch", launch)}, milestones: map[string]bool{}, phaseCount: map[Phase]int{}}
}

// Phase admits at most two spans per fixed phase, even after reconnects or retries.
func (r *Run) Phase(ctx context.Context, p Phase, fn func(context.Context) error) error {
	if !phases[p] {
		return errors.New("unknown telemetry phase")
	}
	r.mu.Lock()
	r.phaseCount[p]++
	sample := r.phaseCount[p] <= 2
	r.mu.Unlock()
	var span trace.Span
	if sample {
		ctx, span = r.t.Traces.Tracer("agent-runtime").Start(ctx, "agent."+string(p))
		defer span.End()
	}
	start := time.Now()
	err := fn(ctx)
	outcome := "ok"
	if err != nil {
		outcome = "error"
		if span != nil {
			span.SetStatus(codes.Error, "operation failed")
		}
	}
	attrs := append(r.attributes(), attribute.String("phase", string(p)), attribute.String("outcome", outcome))
	r.t.phase.Record(ctx, time.Since(start).Seconds(), metric.WithAttributes(attrs...))
	return err
}
func (r *Run) Milestone(ctx context.Context, name string) {
	switch name {
	case "harness_ready", "harness_running", "first_response", "first_text":
	default:
		return
	}
	r.mu.Lock()
	if r.milestones[name] {
		r.mu.Unlock()
		return
	}
	r.milestones[name] = true
	r.mu.Unlock()
	attrs := append(r.attributes(), attribute.String("milestone", name))
	r.t.startup.Record(ctx, time.Since(r.started).Seconds(), metric.WithAttributes(attrs...))
}
func (r *Run) Event(ctx context.Context, kind string) {
	switch kind {
	case "tool", "tool_failed", "turn", "reconnect", "dropped":
	default:
		return
	}
	r.t.events.Add(ctx, 1, metric.WithAttributes(attribute.String("kind", kind)))
}
func (r *Run) Finish(ctx context.Context, err error) {
	outcome := "ok"
	if err != nil {
		outcome = "error"
	}
	r.t.runs.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", outcome)))
}

// Duration records an operation measured by the sandbox without inventing cross-host timestamps.
func (r *Run) Duration(ctx context.Context, p Phase, seconds float64) {
	if !phases[p] || seconds < 0 || seconds > 86400 {
		return
	}
	attrs := append(r.attributes(), attribute.String("phase", string(p)), attribute.String("outcome", "ok"), attribute.String("source", "sandbox"))
	r.t.phase.Record(ctx, seconds, metric.WithAttributes(attrs...))
}

func (r *Run) attributes() []attribute.KeyValue {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]attribute.KeyValue{}, r.attrs...)
}
func (r *Run) Launch(launch string) {
	if launch != "warm" && launch != "cold" {
		launch = "unknown"
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.attrs[1] = attribute.String("sandbox.launch", launch)
}
