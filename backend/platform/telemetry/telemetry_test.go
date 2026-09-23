package telemetry

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

func TestTelemetryExportsResourceSpansAndMetrics(t *testing.T) {
	t.Parallel()

	spanExporter := tracetest.NewInMemoryExporter()
	metricReader := sdkmetric.NewManualReader()
	pipeline, err := New(
		context.Background(),
		validConfigWithoutEndpoint(),
		WithSpanExporter(spanExporter),
		WithMetricReader(metricReader),
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx, span := pipeline.Tracer("test").Start(context.Background(), "operation")
	counter, err := pipeline.Meter("test").Int64Counter("test.requests")
	if err != nil {
		t.Fatalf("Int64Counter() error = %v", err)
	}
	counter.Add(
		ctx,
		1,
		metric.WithAttributes(attribute.String("rpc.method", "marketmesh.auth.v1.AuthService/Login")),
	)
	span.End()

	if err := pipeline.ForceFlush(context.Background()); err != nil {
		t.Fatalf("ForceFlush() error = %v", err)
	}

	spans := spanExporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("exported spans = %d, want 1", len(spans))
	}
	assertResource(t, spans[0].Resource)

	var resourceMetrics metricdata.ResourceMetrics
	if err := metricReader.Collect(context.Background(), &resourceMetrics); err != nil {
		t.Fatalf("Collect() error = %v", err)
	}
	assertResource(t, resourceMetrics.Resource)
	if len(resourceMetrics.ScopeMetrics) != 1 || len(resourceMetrics.ScopeMetrics[0].Metrics) != 1 {
		t.Fatalf("unexpected metric data: %+v", resourceMetrics.ScopeMetrics)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := pipeline.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}

func TestTraceSampleRatioZeroDropsNewRoot(t *testing.T) {
	t.Parallel()

	ratio := 0.0
	config := validConfigWithoutEndpoint()
	config.TraceSampleRatio = &ratio
	pipeline, err := New(
		context.Background(),
		config,
		WithSpanExporter(tracetest.NewNoopExporter()),
		WithMetricReader(sdkmetric.NewManualReader()),
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	_, span := pipeline.Tracer("test").Start(context.Background(), "not-sampled")
	if span.IsRecording() {
		t.Fatal("span is recording with zero sample ratio")
	}
	span.End()

	if err := pipeline.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}

func TestParentBasedSamplerKeepsSampledParent(t *testing.T) {
	t.Parallel()

	ratio := 0.0
	config := validConfigWithoutEndpoint()
	config.TraceSampleRatio = &ratio
	pipeline, err := New(
		context.Background(),
		config,
		WithSpanExporter(tracetest.NewNoopExporter()),
		WithMetricReader(sdkmetric.NewManualReader()),
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	parent := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
		SpanID:     trace.SpanID{1, 2, 3, 4, 5, 6, 7, 8},
		TraceFlags: trace.FlagsSampled,
	})
	ctx := trace.ContextWithSpanContext(context.Background(), parent)
	_, span := pipeline.Tracer("test").Start(ctx, "sampled-child")
	if !span.IsRecording() {
		t.Fatal("span with sampled parent is not recording")
	}
	span.End()

	if err := pipeline.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}

func TestTraceContextPropagation(t *testing.T) {
	t.Parallel()

	traceID := trace.TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	spanID := trace.SpanID{1, 2, 3, 4, 5, 6, 7, 8}
	spanContext := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     spanID,
		TraceFlags: trace.FlagsSampled,
		Remote:     true,
	})
	ctx := trace.ContextWithRemoteSpanContext(context.Background(), spanContext)
	carrier := propagation.MapCarrier{}
	propagator := NewNoop().Propagator()

	propagator.Inject(ctx, carrier)
	extracted := trace.SpanContextFromContext(propagator.Extract(context.Background(), carrier))

	if !extracted.Equal(spanContext) {
		t.Fatalf("extracted span context = %v, want %v", extracted, spanContext)
	}
	if !extracted.IsRemote() {
		t.Fatal("extracted span context is not remote")
	}
	if len(carrier) != 1 {
		t.Fatalf("carrier fields = %v, want only traceparent", carrier)
	}
}

func TestShutdownPreservesAllErrorsAndRunsOnce(t *testing.T) {
	t.Parallel()

	traceErr := errors.New("trace shutdown")
	metricErr := errors.New("metric shutdown")
	spanExporter := &failingSpanExporter{shutdownErr: traceErr}
	metricExporter := &failingMetricExporter{shutdownErr: metricErr}
	metricReader := sdkmetric.NewPeriodicReader(metricExporter, sdkmetric.WithInterval(time.Hour))
	pipeline, err := New(
		context.Background(),
		validConfigWithoutEndpoint(),
		WithSpanExporter(spanExporter),
		WithMetricReader(metricReader),
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	const callers = 8
	errorsByCaller := make(chan error, callers)
	var group sync.WaitGroup
	for range callers {
		group.Add(1)
		go func() {
			defer group.Done()
			errorsByCaller <- pipeline.Shutdown(context.Background())
		}()
	}
	group.Wait()
	close(errorsByCaller)

	for err := range errorsByCaller {
		if !errors.Is(err, traceErr) {
			t.Errorf("Shutdown() error %v does not preserve trace error", err)
		}
		if !errors.Is(err, metricErr) {
			t.Errorf("Shutdown() error %v does not preserve metric error", err)
		}
	}
	if spanExporter.shutdownCalls != 1 {
		t.Fatalf("span exporter shutdown calls = %d, want 1", spanExporter.shutdownCalls)
	}
	if metricExporter.shutdownCalls != 1 {
		t.Fatalf("metric exporter shutdown calls = %d, want 1", metricExporter.shutdownCalls)
	}
}

func TestForceFlushPreservesTraceAndMetricErrors(t *testing.T) {
	t.Parallel()

	traceErr := errors.New("trace export")
	metricErr := errors.New("metric export")
	spanExporter := &failingSpanExporter{exportErr: traceErr}
	metricExporter := &failingMetricExporter{exportErr: metricErr}
	metricReader := sdkmetric.NewPeriodicReader(metricExporter, sdkmetric.WithInterval(time.Hour))
	pipeline, err := New(
		context.Background(),
		validConfigWithoutEndpoint(),
		WithSpanExporter(spanExporter),
		WithMetricReader(metricReader),
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	_, span := pipeline.Tracer("test").Start(context.Background(), "operation")
	span.End()
	counter, err := pipeline.Meter("test").Int64Counter("test.requests")
	if err != nil {
		t.Fatalf("Int64Counter() error = %v", err)
	}
	counter.Add(context.Background(), 1)

	err = pipeline.ForceFlush(context.Background())
	if !errors.Is(err, traceErr) {
		t.Errorf("ForceFlush() error %v does not preserve trace error", err)
	}
	if !errors.Is(err, metricErr) {
		t.Errorf("ForceFlush() error %v does not preserve metric error", err)
	}

	if err := pipeline.Shutdown(context.Background()); err != nil && !errors.Is(err, metricErr) {
		t.Fatalf("Shutdown() error = %v", err)
	}
}

func TestNoopPipeline(t *testing.T) {
	t.Parallel()

	pipeline := NewNoop()
	_, span := pipeline.Tracer("test").Start(context.Background(), "operation")
	if span.IsRecording() {
		t.Fatal("no-op span is recording")
	}
	span.End()

	if err := pipeline.ForceFlush(context.Background()); err != nil {
		t.Fatalf("ForceFlush() error = %v", err)
	}
	if err := pipeline.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}

func assertResource(t *testing.T, resource interface {
	Set() *attribute.Set
}) {
	t.Helper()

	attributes := resource.Set()
	want := map[attribute.Key]string{
		semconv.ServiceNameKey:               "auth",
		semconv.ServiceVersionKey:            "1.2.3",
		semconv.DeploymentEnvironmentNameKey: "test",
		semconv.ServiceInstanceIDKey:         "auth-1",
	}
	for key, value := range want {
		actual, found := attributes.Value(key)
		if !found || actual.AsString() != value {
			t.Errorf("resource attribute %q = %q, %v; want %q", key, actual.AsString(), found, value)
		}
	}
}

func validConfigWithoutEndpoint() Config {
	config := validConfig()
	config.Endpoint = ""

	return config
}

type failingSpanExporter struct {
	mu            sync.Mutex
	exportErr     error
	shutdownErr   error
	shutdownCalls int
}

func (exporter *failingSpanExporter) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error {
	return exporter.exportErr
}

func (exporter *failingSpanExporter) Shutdown(context.Context) error {
	exporter.mu.Lock()
	defer exporter.mu.Unlock()
	exporter.shutdownCalls++

	return exporter.shutdownErr
}

type failingMetricExporter struct {
	mu            sync.Mutex
	exportErr     error
	shutdownErr   error
	shutdownCalls int
}

func (*failingMetricExporter) Temporality(kind sdkmetric.InstrumentKind) metricdata.Temporality {
	return sdkmetric.DefaultTemporalitySelector(kind)
}

func (*failingMetricExporter) Aggregation(kind sdkmetric.InstrumentKind) sdkmetric.Aggregation {
	return sdkmetric.DefaultAggregationSelector(kind)
}

func (exporter *failingMetricExporter) Export(context.Context, *metricdata.ResourceMetrics) error {
	return exporter.exportErr
}

func (*failingMetricExporter) ForceFlush(context.Context) error {
	return nil
}

func (exporter *failingMetricExporter) Shutdown(context.Context) error {
	exporter.mu.Lock()
	defer exporter.mu.Unlock()
	exporter.shutdownCalls++

	return exporter.shutdownErr
}
