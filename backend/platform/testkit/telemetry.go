package testkit

import (
	"context"
	"testing"
	"time"

	platformtelemetry "github.com/v0hmly/marketmesh/platform/telemetry"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

const cleanupTimeout = time.Second

// Telemetry объединяет изолированный MarketMesh telemetry pipeline и
// in-memory хранилища spans и metrics для проверок.
type Telemetry struct {
	*platformtelemetry.Telemetry

	spanExporter *tracetest.InMemoryExporter
	metricReader *sdkmetric.ManualReader
}

// NewTelemetry создаёт изолированный in-memory telemetry pipeline и
// регистрирует его bounded shutdown в Cleanup.
func NewTelemetry(t testing.TB) *Telemetry {
	t.Helper()

	spanExporter := tracetest.NewInMemoryExporter()
	metricReader := sdkmetric.NewManualReader()
	pipeline, err := platformtelemetry.New(
		t.Context(),
		platformtelemetry.Config{
			ServiceName:    "test",
			ServiceVersion: "test",
			Environment:    "test",
			InstanceID:     "test",
		},
		platformtelemetry.WithSpanExporter(spanExporter),
		platformtelemetry.WithMetricReader(metricReader),
	)
	if err != nil {
		t.Fatalf("testkit: create telemetry: %v", err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()
		if err := pipeline.Shutdown(shutdownCtx); err != nil {
			t.Errorf("testkit: shutdown telemetry: %v", err)
		}
	})

	return &Telemetry{
		Telemetry:    pipeline,
		spanExporter: spanExporter,
		metricReader: metricReader,
	}
}

// NoopTelemetry создаёт изолированный no-op pipeline и регистрирует его
// lifecycle в Cleanup так же, как ресурсные helpers.
func NoopTelemetry(t testing.TB) *platformtelemetry.Telemetry {
	t.Helper()

	pipeline := platformtelemetry.NewNoop()
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()
		if err := pipeline.Shutdown(shutdownCtx); err != nil {
			t.Errorf("testkit: shutdown no-op telemetry: %v", err)
		}
	})

	return pipeline
}

// Spans принудительно выгружает и возвращает снимок накопленных spans.
func (telemetry *Telemetry) Spans(t testing.TB) tracetest.SpanStubs {
	t.Helper()

	if err := telemetry.ForceFlush(t.Context()); err != nil {
		t.Fatalf("testkit: flush telemetry spans: %v", err)
	}

	return telemetry.spanExporter.GetSpans()
}

// Metrics собирает согласованный снимок накопленных metrics.
func (telemetry *Telemetry) Metrics(t testing.TB) metricdata.ResourceMetrics {
	t.Helper()

	var result metricdata.ResourceMetrics
	if err := telemetry.metricReader.Collect(t.Context(), &result); err != nil {
		t.Fatalf("testkit: collect telemetry metrics: %v", err)
	}

	return result
}
