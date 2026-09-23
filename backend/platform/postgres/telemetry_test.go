package postgres

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/v0hmly/marketmesh/platform/testkit"
)

func TestQueryTracerDoesNotRecordSQLArgumentsOrErrorText(t *testing.T) {
	t.Parallel()

	observability := testkit.NewTelemetry(t)
	tracer := &queryTracer{
		pool:   roleRW,
		tracer: observability.Tracer(instrumentationName),
	}
	sensitiveSQL := "SELECT private_column FROM private_table WHERE secret = $1"
	sensitiveArgument := "private-query-argument"
	sensitiveError := "private-server-error-detail"

	ctx := tracer.TraceQueryStart(
		t.Context(),
		nil,
		pgx.TraceQueryStartData{
			SQL:  sensitiveSQL,
			Args: []any{sensitiveArgument},
		},
	)
	tracer.TraceQueryEnd(
		ctx,
		nil,
		pgx.TraceQueryEndData{
			Err: &pgconn.PgError{
				Code:    "23505",
				Message: sensitiveError,
			},
		},
	)

	spans := observability.Spans(t)
	if len(spans) != 1 {
		t.Fatalf("exported spans = %d, want 1", len(spans))
	}
	representation := fmt.Sprintf("%+v", spans[0])
	for _, sensitive := range []string{sensitiveSQL, sensitiveArgument, sensitiveError} {
		if strings.Contains(representation, sensitive) {
			t.Fatalf("span contains sensitive value %q: %s", sensitive, representation)
		}
	}
	if spans[0].Name != "postgres.query" || spans[0].Status.Description != "query failed" {
		t.Fatalf("span = %+v", spans[0])
	}
}

func TestPoolAndTransactionMetricsUseBoundedAttributes(t *testing.T) {
	t.Parallel()

	observability := testkit.NewTelemetry(t)
	rw := &fakePool{poolStats: poolStats{
		acquireCount:          11,
		acquireDuration:       2 * time.Second,
		acquiredConns:         2,
		canceledAcquireCount:  1,
		constructingConns:     1,
		emptyAcquireCount:     3,
		emptyAcquireWaitTime:  time.Second,
		idleConns:             4,
		maxConns:              8,
		maxIdleDestroyCount:   5,
		maxLifetimeDestroyCnt: 6,
		newConnsCount:         7,
		totalConns:            6,
	}}
	ro := &fakePool{poolStats: rw.poolStats}
	instruments, err := newInstruments(
		observability.Meter(instrumentationName),
		&managedPool{role: roleRW, backend: rw},
		&managedPool{role: roleRO, backend: ro},
	)
	if err != nil {
		t.Fatalf("newInstruments() error = %v", err)
	}
	t.Cleanup(func() {
		if err := instruments.unregister(); err != nil {
			t.Errorf("unregister metrics: %v", err)
		}
	})

	instruments.recordTransactionAttempt(
		t.Context(),
		IsolationSerializable,
		false,
	)
	instruments.recordTransactionRetry(t.Context(), "serialization_failure")
	instruments.recordTransactionDuration(
		t.Context(),
		25*time.Millisecond,
		IsolationSerializable,
		false,
		"committed",
	)

	resourceMetrics := observability.Metrics(t)
	names := map[string]struct{}{}
	for _, scopeMetrics := range resourceMetrics.ScopeMetrics {
		for _, measurement := range scopeMetrics.Metrics {
			names[measurement.Name] = struct{}{}
		}
	}
	for _, expected := range []string{
		"marketmesh.postgres.transaction.attempts",
		"marketmesh.postgres.transaction.retries",
		"marketmesh.postgres.transaction.duration",
		"marketmesh.postgres.connections.total",
		"marketmesh.postgres.connections.idle",
		"marketmesh.postgres.connections.acquired",
		"marketmesh.postgres.connections.constructing",
		"marketmesh.postgres.connections.max",
		"marketmesh.postgres.connection.acquires",
		"marketmesh.postgres.connection.acquires.canceled",
		"marketmesh.postgres.connection.acquire.duration",
		"marketmesh.postgres.connection.acquires.empty",
		"marketmesh.postgres.connection.acquire.empty_wait",
		"marketmesh.postgres.connections.created",
		"marketmesh.postgres.connections.destroyed.idle",
		"marketmesh.postgres.connections.destroyed.lifetime",
	} {
		if _, found := names[expected]; !found {
			t.Errorf("metric %q was not collected; got %v", expected, names)
		}
	}

	representation := fmt.Sprintf("%+v", resourceMetrics)
	for _, sensitive := range []string{
		"postgres://user:private-password@database/internal",
		"private-query-argument",
		"SELECT private_column",
	} {
		if strings.Contains(representation, sensitive) {
			t.Fatalf("metrics contain sensitive value %q", sensitive)
		}
	}
}
