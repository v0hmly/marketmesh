package redis

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestOperationHookDoesNotRecordCommandsKeysValuesAddressesOrErrors(t *testing.T) {
	t.Parallel()

	spanExporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(spanExporter))
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := provider.Shutdown(shutdownCtx); err != nil {
			t.Errorf("trace provider shutdown: %v", err)
		}
	})
	hook := &operationHook{
		role:        RoleAuth,
		tracer:      provider.Tracer(instrumentationName),
		instruments: &instruments{},
	}
	sensitiveKey := "private:auth:session:key"
	sensitiveValue := "private-session-value"
	sensitiveAddress := "auth-state.internal:6379"
	sensitiveError := errors.New("server error includes " + sensitiveValue + " at " + sensitiveAddress)
	command := goredis.NewStatusCmd(t.Context(), "SET", sensitiveKey, sensitiveValue)
	process := hook.ProcessHook(func(context.Context, goredis.Cmder) error {
		return sensitiveError
	})
	if err := process(t.Context(), command); !errors.Is(err, sensitiveError) {
		t.Fatalf("ProcessHook() error = %v", err)
	}

	spans := spanExporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("exported spans = %d, want 1", len(spans))
	}
	representation := fmt.Sprintf("%+v", spans[0])
	for _, sensitive := range []string{
		"SET", sensitiveKey, sensitiveValue, sensitiveAddress, sensitiveError.Error(),
	} {
		if strings.Contains(representation, sensitive) {
			t.Fatalf("span contains sensitive value %q: %s", sensitive, representation)
		}
	}
	if spans[0].Name != "redis.command" || spans[0].Status.Description != "operation failed" {
		t.Fatalf("span = %+v", spans[0])
	}
}

func TestMetricsUseOnlyBoundedAttributes(t *testing.T) {
	t.Parallel()

	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := provider.Shutdown(shutdownCtx); err != nil {
			t.Errorf("meter provider shutdown: %v", err)
		}
	})
	backend := &fakeBackend{stats: &goredis.PoolStats{
		Hits:            11,
		Misses:          7,
		Timeouts:        2,
		WaitCount:       3,
		Unusable:        1,
		WaitDurationNs:  int64(2 * time.Second),
		TotalConns:      5,
		IdleConns:       4,
		StaleConns:      6,
		PendingRequests: 1,
	}}
	instruments, err := newInstruments(
		provider.Meter(instrumentationName),
		RoleAuth,
		backend,
		8,
	)
	if err != nil {
		t.Fatalf("newInstruments() error = %v", err)
	}
	t.Cleanup(func() {
		if err := instruments.unregister(); err != nil {
			t.Errorf("unregister metrics: %v", err)
		}
	})
	instruments.recordOperation(t.Context(), "command", "ok", 25*time.Millisecond)
	instruments.recordRetry(t.Context(), "pool")

	var resourceMetrics metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &resourceMetrics); err != nil {
		t.Fatalf("collect metrics: %v", err)
	}
	names := map[string]struct{}{}
	for _, scopeMetrics := range resourceMetrics.ScopeMetrics {
		for _, measurement := range scopeMetrics.Metrics {
			names[measurement.Name] = struct{}{}
		}
	}
	for _, expected := range []string{
		"marketmesh.redis.operation.attempts",
		"marketmesh.redis.operation.duration",
		"marketmesh.redis.operation.retries",
		"marketmesh.redis.connections.total",
		"marketmesh.redis.connections.idle",
		"marketmesh.redis.connections.max",
		"marketmesh.redis.pool.requests.pending",
		"marketmesh.redis.pool.hits",
		"marketmesh.redis.pool.misses",
		"marketmesh.redis.pool.timeouts",
		"marketmesh.redis.pool.waits",
		"marketmesh.redis.pool.wait.duration",
		"marketmesh.redis.connections.stale",
		"marketmesh.redis.connections.unusable",
	} {
		if _, found := names[expected]; !found {
			t.Errorf("metric %q was not collected; got %v", expected, names)
		}
	}

	representation := fmt.Sprintf("%+v", resourceMetrics)
	for _, sensitive := range []string{
		"private:auth:session:key",
		"private-session-value",
		"auth-state.internal:6379",
	} {
		if strings.Contains(representation, sensitive) {
			t.Fatalf("metrics contain sensitive value %q", sensitive)
		}
	}
}
