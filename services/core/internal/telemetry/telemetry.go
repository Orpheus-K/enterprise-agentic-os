// Package telemetry emits bounded operation metrics and content-free OTel spans locally.
package telemetry

import (
	"context"
	"errors"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
	"log/slog"
	"time"
)

type Telemetry struct {
	traces  *sdktrace.TracerProvider
	meters  *sdkmetric.MeterProvider
	reader  *sdkmetric.ManualReader
	tracer  trace.Tracer
	calls   metric.Int64Counter
	latency metric.Float64Histogram
}

func New(logger *slog.Logger) (*Telemetry, error) {
	res := resource.NewSchemaless(attribute.String("service.name", "agentos.core"))
	tp := sdktrace.NewTracerProvider(sdktrace.WithResource(res), sdktrace.WithSyncer(logExporter{logger}))
	reader := sdkmetric.NewManualReader()
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithResource(res), sdkmetric.WithReader(reader))
	meter := mp.Meter("agentos.core")
	calls, err := meter.Int64Counter("agentos.operations")
	if err != nil {
		return nil, err
	}
	latency, err := meter.Float64Histogram("agentos.operation.duration", metric.WithUnit("s"), metric.WithExplicitBucketBoundaries(.005, .01, .025, .05, .1, .25, .5, 1, 2, 5, 10, 30, 60))
	if err != nil {
		return nil, err
	}
	return &Telemetry{tp, mp, reader, tp.Tracer("agentos.core"), calls, latency}, nil
}

// Start accepts fixed operation names only; caller input is never a metric label or span name.
func (t *Telemetry) Start(ctx context.Context, operation string, opts ...trace.SpanStartOption) (context.Context, func(bool)) {
	started := time.Now()
	ctx, span := t.tracer.Start(ctx, operation, opts...)
	return ctx, func(success bool) {
		outcome := "ok"
		if !success {
			outcome = "error"
			span.SetStatus(codes.Error, "operation_failed")
		}
		attrs := metric.WithAttributes(attribute.String("operation", operation), attribute.String("outcome", outcome))
		t.calls.Add(ctx, 1, attrs)
		t.latency.Record(ctx, time.Since(started).Seconds(), attrs)
		span.End()
	}
}
func (t *Telemetry) Snapshot(ctx context.Context) (metricdata.ResourceMetrics, error) {
	var data metricdata.ResourceMetrics
	err := t.reader.Collect(ctx, &data)
	// The SDK merges OTEL_RESOURCE_ATTRIBUTES even with WithResource; export only our approved resource.
	data.Resource = resource.NewSchemaless(attribute.String("service.name", "agentos.core"))
	return data, err
}
func (t *Telemetry) Close(ctx context.Context) error {
	return errors.Join(t.traces.Shutdown(ctx), t.meters.Shutdown(ctx))
}

// Explicit projection prevents future instrumentation attributes from silently leaking content.
type logExporter struct{ logger *slog.Logger }

func (e logExporter) ExportSpans(ctx context.Context, spans []sdktrace.ReadOnlySpan) error {
	for _, span := range spans {
		args := []any{"operation", span.Name(), "trace_id", span.SpanContext().TraceID().String(), "span_id", span.SpanContext().SpanID().String(), "parent_span_id", span.Parent().SpanID().String(), "duration_ms", span.EndTime().Sub(span.StartTime()).Milliseconds(), "status", span.Status().Code.String()}
		for _, a := range span.Attributes() {
			if a.Key == "task_id" {
				args = append(args, "task_id", a.Value.AsString())
			}
		}
		for _, link := range span.Links() {
			args = append(args, "linked_trace_id", link.SpanContext.TraceID().String())
		}
		e.logger.InfoContext(ctx, "operation finished", args...)
	}
	return nil
}
func (logExporter) Shutdown(context.Context) error { return nil }
