package testkit_test

import (
	"testing"

	"github.com/v0hmly/marketmesh/platform/testkit"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

func TestTelemetryCapturesSpansAndMetrics(t *testing.T) {
	t.Parallel()

	telemetry := testkit.NewTelemetry(t)
	ctx, span := telemetry.Tracer("test").Start(t.Context(), "operation")
	counter, err := telemetry.Meter("test").Int64Counter("test.operations")
	if err != nil {
		t.Fatalf("create counter: %v", err)
	}
	counter.Add(ctx, 1, metric.WithAttributes(attribute.String("result", "ok")))
	span.End()

	spans := telemetry.Spans(t)
	if len(spans) != 1 || spans[0].Name != "operation" {
		t.Fatalf("spans = %+v, want operation", spans)
	}
	metrics := telemetry.Metrics(t)
	if len(metrics.ScopeMetrics) != 1 || len(metrics.ScopeMetrics[0].Metrics) != 1 {
		t.Fatalf("metrics = %+v, want one metric", metrics.ScopeMetrics)
	}
	if metrics.ScopeMetrics[0].Metrics[0].Name != "test.operations" {
		t.Fatalf("metric name = %q, want test.operations", metrics.ScopeMetrics[0].Metrics[0].Name)
	}
}

func TestNoopTelemetryIsIsolated(t *testing.T) {
	t.Parallel()

	telemetry := testkit.NoopTelemetry(t)
	_, span := telemetry.Tracer("test").Start(t.Context(), "ignored")
	if span.IsRecording() {
		t.Fatal("no-op span is recording")
	}
	span.End()
}
