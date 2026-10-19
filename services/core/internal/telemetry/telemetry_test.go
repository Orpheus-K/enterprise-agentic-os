package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/trace"
	"log/slog"
	"strings"
	"testing"
)

func TestTraceLinksMetricsAndRedaction(t *testing.T) {
	t.Setenv("OTEL_RESOURCE_ATTRIBUTES", "secret=environment-secret")
	var logs bytes.Buffer
	tel, err := New(slog.New(slog.NewJSONHandler(&logs, nil)))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := tel.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	ingress, end := tel.Start(t.Context(), "ingress.webhook")
	source := trace.SpanContextFromContext(ingress)
	end(true)
	run, endRun := tel.Start(t.Context(), "task.run", trace.WithNewRoot(), trace.WithLinks(trace.Link{SpanContext: source}), trace.WithAttributes(attribute.String("task_id", "task-test"), attribute.String("prompt", "private-prompt")))
	child, endChild := tel.Start(run, "model.complete")
	trace.SpanFromContext(child).RecordError(context.Canceled)
	endChild(false)
	endRun(false)
	var lines []map[string]any
	decoder := json.NewDecoder(&logs)
	for decoder.More() {
		var line map[string]any
		if err := decoder.Decode(&line); err != nil {
			t.Fatal(err)
		}
		lines = append(lines, line)
	}
	if len(lines) != 3 {
		t.Fatalf("span count=%d", len(lines))
	}
	if lines[1]["trace_id"] != lines[2]["trace_id"] || lines[1]["parent_span_id"] != lines[2]["span_id"] || lines[2]["linked_trace_id"] != source.TraceID().String() || lines[2]["trace_id"] == source.TraceID().String() {
		t.Fatal("broken causal correlation")
	}
	data, err := tel.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	calls := int64(0)
	for _, scope := range data.ScopeMetrics {
		for _, m := range scope.Metrics {
			if sum, ok := m.Data.(metricdata.Sum[int64]); ok {
				for _, point := range sum.DataPoints {
					calls += point.Value
				}
			}
		}
	}
	if calls != 3 {
		t.Fatalf("operation count=%d", calls)
	}
	encoded, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"private-prompt", "environment-secret", "context canceled"} {
		for _, line := range lines {
			b, _ := json.Marshal(line)
			if strings.Contains(string(b), secret) {
				t.Fatal("trace leaked secret")
			}
		}
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("metrics leaked test sentinel %s", secret)
		}
	}
	second, err := tel.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(second.ScopeMetrics) == 0 {
		t.Fatal("snapshot unexpectedly resets metrics")
	}
}
