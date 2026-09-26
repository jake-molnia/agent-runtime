package telemetry

import (
	"context"
	"strings"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

type safeExporter struct{ sdktrace.SpanExporter }

func SafeExporter(e sdktrace.SpanExporter) sdktrace.SpanExporter { return safeExporter{e} }
func (e safeExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	safe := make([]sdktrace.ReadOnlySpan, 0, len(spans))
	for _, s := range spans {
		if !strings.HasPrefix(s.Name(), "agent.") && !strings.HasPrefix(s.Name(), "hatchet.") {
			continue
		}
		attrs := make([]attribute.KeyValue, 0, 16)
		for _, a := range s.Attributes() {
			k := string(a.Key)
			switch k {
			case "hatchet.step_run_id", "hatchet.workflow_run_id", "hatchet.workflow_id", "hatchet.task_run_id", "hatchet.retry_count", "sandbox.claim", "sandbox.name", "opencode.session_id":
				if a.Value.Type() == attribute.STRING && len(a.Value.AsString()) > 256 {
					continue
				}
				attrs = append(attrs, a)
			}
		}
		safe = append(safe, cleanSpan{ReadOnlySpan: s, attrs: attrs})
	}
	if len(safe) == 0 {
		return nil
	}
	return e.SpanExporter.ExportSpans(ctx, safe)
}

type cleanSpan struct {
	sdktrace.ReadOnlySpan
	attrs []attribute.KeyValue
}

func (s cleanSpan) Attributes() []attribute.KeyValue { return s.attrs }
func (s cleanSpan) Events() []sdktrace.Event         { return nil }
func (s cleanSpan) Links() []sdktrace.Link           { return nil }
func (s cleanSpan) Status() sdktrace.Status {
	status := s.ReadOnlySpan.Status()
	if status.Code == codes.Error {
		status.Description = "operation failed"
	} else {
		status.Description = ""
	}
	return status
}
