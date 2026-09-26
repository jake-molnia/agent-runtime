package telemetry

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"google.golang.org/grpc"
	"google.golang.org/grpc/status"
)

type RuntimeMetrics struct {
	duration metric.Float64Histogram
	calls    metric.Int64Counter
}

func (t *Telemetry) RuntimeMetrics() (*RuntimeMetrics, error) {
	meter := t.Metrics.Meter("sandbox-runtime")
	duration, err := meter.Float64Histogram("sandbox.operation.duration", metric.WithUnit("s"), metric.WithExplicitBucketBoundaries(.001, .01, .1, 1, 10, 60, 300, 1800))
	if err != nil {
		return nil, err
	}
	calls, err := meter.Int64Counter("sandbox.operations")
	if err != nil {
		return nil, err
	}
	return &RuntimeMetrics{duration, calls}, nil
}
func (m *RuntimeMetrics) record(ctx context.Context, operation, outcome string, start time.Time) {
	attrs := metric.WithAttributes(attribute.String("operation", operation), attribute.String("outcome", outcome))
	m.duration.Record(ctx, time.Since(start).Seconds(), attrs)
	m.calls.Add(ctx, 1, attrs)
}
func rpcName(method string) string {
	name := method[strings.LastIndex(method, "/")+1:]
	switch name {
	case "Start", "Execute", "WriteStdin", "SendSignal", "ResizeTTY":
		return name
	default:
		return "other"
	}
}
func (m *RuntimeMetrics) Unary() grpc.UnaryClientInterceptor {
	return func(ctx context.Context, method string, req, reply any, cc *grpc.ClientConn, invoke grpc.UnaryInvoker, opts ...grpc.CallOption) error {
		start := time.Now()
		err := invoke(ctx, method, req, reply, cc, opts...)
		m.record(ctx, rpcName(method), status.Code(err).String(), start)
		return err
	}
}
func (m *RuntimeMetrics) Stream() grpc.StreamClientInterceptor {
	return func(ctx context.Context, desc *grpc.StreamDesc, cc *grpc.ClientConn, method string, streamer grpc.Streamer, opts ...grpc.CallOption) (grpc.ClientStream, error) {
		start := time.Now()
		s, err := streamer(ctx, desc, cc, method, opts...)
		if err != nil {
			m.record(ctx, rpcName(method), status.Code(err).String(), start)
			return nil, err
		}
		return &measuredStream{ClientStream: s, finish: func(err error) {
			if err == io.EOF {
				err = nil
			}
			m.record(ctx, rpcName(method), status.Code(err).String(), start)
		}}, nil
	}
}

type measuredStream struct {
	grpc.ClientStream
	once   sync.Once
	finish func(error)
}

func (s *measuredStream) RecvMsg(msg any) error {
	err := s.ClientStream.RecvMsg(msg)
	if err != nil {
		s.once.Do(func() { s.finish(err) })
	}
	return err
}

type measuredTransport struct {
	base    http.RoundTripper
	metrics *RuntimeMetrics
}

func (m *RuntimeMetrics) Transport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return measuredTransport{base, m}
}
func (t measuredTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	name := "other"
	switch {
	case strings.HasPrefix(req.URL.Path, "/v1/files/"):
		switch req.Method {
		case "GET":
			name = "file_read"
		case "PUT":
			name = "file_write"
		case "HEAD":
			name = "file_stat"
		case "DELETE":
			name = "file_delete"
		}
	case req.URL.Path == "/v1/health":
		name = "health"
	case req.URL.Path == "/v1/metadata":
		name = "metadata"
	}
	start := time.Now()
	res, err := t.base.RoundTrip(req)
	if err != nil {
		t.metrics.record(req.Context(), name, "error", start)
		return nil, err
	}
	outcome := "ok"
	if res.StatusCode >= 400 {
		outcome = "error"
	}
	res.Body = &measuredBody{ReadCloser: res.Body, finish: func() { t.metrics.record(req.Context(), name, outcome, start) }}
	return res, nil
}

type measuredBody struct {
	io.ReadCloser
	once   sync.Once
	finish func()
}

func (b *measuredBody) Close() error { err := b.ReadCloser.Close(); b.once.Do(b.finish); return err }
