package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

type queryTracer struct {
	pool   poolRole
	tracer trace.Tracer
}

func (tracer *queryTracer) TraceQueryStart(
	ctx context.Context,
	_ *pgx.Conn,
	_ pgx.TraceQueryStartData,
) context.Context {
	ctx, _ = tracer.tracer.Start(
		ctx,
		"postgres.query",
		trace.WithAttributes(
			attribute.String("db.system.name", "postgresql"),
			attribute.String("postgres.pool", string(tracer.pool)),
		),
	)

	return ctx
}

func (*queryTracer) TraceQueryEnd(
	ctx context.Context,
	_ *pgx.Conn,
	data pgx.TraceQueryEndData,
) {
	span := trace.SpanFromContext(ctx)
	if data.Err != nil {
		span.SetStatus(codes.Error, "query failed")

		var postgresErr *pgconn.PgError
		if errors.As(data.Err, &postgresErr) {
			span.SetAttributes(attribute.String("db.response.status_code", postgresErr.Code))
		}
	}
	span.End()
}

type instruments struct {
	transactionAttempts metric.Int64Counter
	transactionRetries  metric.Int64Counter
	transactionDuration metric.Float64Histogram

	connectionsTotal        metric.Int64ObservableGauge
	connectionsIdle         metric.Int64ObservableGauge
	connectionsAcquired     metric.Int64ObservableGauge
	connectionsConstructing metric.Int64ObservableGauge
	connectionsMax          metric.Int64ObservableGauge
	acquiresTotal           metric.Int64ObservableCounter
	canceledAcquiresTotal   metric.Int64ObservableCounter
	acquireDurationTotal    metric.Float64ObservableCounter
	emptyAcquiresTotal      metric.Int64ObservableCounter
	emptyAcquireWaitTotal   metric.Float64ObservableCounter
	newConnectionsTotal     metric.Int64ObservableCounter
	idleDestroyedTotal      metric.Int64ObservableCounter
	lifetimeDestroyedTotal  metric.Int64ObservableCounter

	registration metric.Registration
}

func newInstruments(
	meter metric.Meter,
	pools ...*managedPool,
) (*instruments, error) {
	instruments := &instruments{}

	var err error
	instruments.transactionAttempts, err = meter.Int64Counter(
		"marketmesh.postgres.transaction.attempts",
		metric.WithDescription("Number of PostgreSQL transaction attempts."),
		metric.WithUnit("{attempt}"),
	)
	if err != nil {
		return nil, instrumentError("create transaction attempts counter", err)
	}
	instruments.transactionRetries, err = meter.Int64Counter(
		"marketmesh.postgres.transaction.retries",
		metric.WithDescription("Number of safe PostgreSQL transaction retries."),
		metric.WithUnit("{retry}"),
	)
	if err != nil {
		return nil, instrumentError("create transaction retries counter", err)
	}
	instruments.transactionDuration, err = meter.Float64Histogram(
		"marketmesh.postgres.transaction.duration",
		metric.WithDescription("Duration of complete PostgreSQL transactions including retries."),
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, instrumentError("create transaction duration histogram", err)
	}

	instruments.connectionsTotal, err = meter.Int64ObservableGauge(
		"marketmesh.postgres.connections.total",
		metric.WithDescription("Current total PostgreSQL pool connections."),
		metric.WithUnit("{connection}"),
	)
	if err != nil {
		return nil, instrumentError("create total connections gauge", err)
	}
	instruments.connectionsIdle, err = meter.Int64ObservableGauge(
		"marketmesh.postgres.connections.idle",
		metric.WithDescription("Current idle PostgreSQL pool connections."),
		metric.WithUnit("{connection}"),
	)
	if err != nil {
		return nil, instrumentError("create idle connections gauge", err)
	}
	instruments.connectionsAcquired, err = meter.Int64ObservableGauge(
		"marketmesh.postgres.connections.acquired",
		metric.WithDescription("Current acquired PostgreSQL pool connections."),
		metric.WithUnit("{connection}"),
	)
	if err != nil {
		return nil, instrumentError("create acquired connections gauge", err)
	}
	instruments.connectionsConstructing, err = meter.Int64ObservableGauge(
		"marketmesh.postgres.connections.constructing",
		metric.WithDescription("Current PostgreSQL pool connections being constructed."),
		metric.WithUnit("{connection}"),
	)
	if err != nil {
		return nil, instrumentError("create constructing connections gauge", err)
	}
	instruments.connectionsMax, err = meter.Int64ObservableGauge(
		"marketmesh.postgres.connections.max",
		metric.WithDescription("Configured maximum PostgreSQL pool connections."),
		metric.WithUnit("{connection}"),
	)
	if err != nil {
		return nil, instrumentError("create max connections gauge", err)
	}
	instruments.acquiresTotal, err = meter.Int64ObservableCounter(
		"marketmesh.postgres.connection.acquires",
		metric.WithDescription("Total successful PostgreSQL pool acquisitions."),
		metric.WithUnit("{acquire}"),
	)
	if err != nil {
		return nil, instrumentError("create acquires counter", err)
	}
	instruments.canceledAcquiresTotal, err = meter.Int64ObservableCounter(
		"marketmesh.postgres.connection.acquires.canceled",
		metric.WithDescription("Total canceled PostgreSQL pool acquisitions."),
		metric.WithUnit("{acquire}"),
	)
	if err != nil {
		return nil, instrumentError("create canceled acquires counter", err)
	}
	instruments.acquireDurationTotal, err = meter.Float64ObservableCounter(
		"marketmesh.postgres.connection.acquire.duration",
		metric.WithDescription("Cumulative PostgreSQL pool acquisition duration."),
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, instrumentError("create acquire duration counter", err)
	}
	instruments.emptyAcquiresTotal, err = meter.Int64ObservableCounter(
		"marketmesh.postgres.connection.acquires.empty",
		metric.WithDescription("Total PostgreSQL acquisitions that waited for an empty pool."),
		metric.WithUnit("{acquire}"),
	)
	if err != nil {
		return nil, instrumentError("create empty acquires counter", err)
	}
	instruments.emptyAcquireWaitTotal, err = meter.Float64ObservableCounter(
		"marketmesh.postgres.connection.acquire.empty_wait",
		metric.WithDescription("Cumulative wait duration for empty PostgreSQL pools."),
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, instrumentError("create empty acquire wait counter", err)
	}
	instruments.newConnectionsTotal, err = meter.Int64ObservableCounter(
		"marketmesh.postgres.connections.created",
		metric.WithDescription("Total PostgreSQL pool connections created."),
		metric.WithUnit("{connection}"),
	)
	if err != nil {
		return nil, instrumentError("create new connections counter", err)
	}
	instruments.idleDestroyedTotal, err = meter.Int64ObservableCounter(
		"marketmesh.postgres.connections.destroyed.idle",
		metric.WithDescription("Total PostgreSQL connections destroyed by idle limit."),
		metric.WithUnit("{connection}"),
	)
	if err != nil {
		return nil, instrumentError("create idle destroyed counter", err)
	}
	instruments.lifetimeDestroyedTotal, err = meter.Int64ObservableCounter(
		"marketmesh.postgres.connections.destroyed.lifetime",
		metric.WithDescription("Total PostgreSQL connections destroyed by lifetime limit."),
		metric.WithUnit("{connection}"),
	)
	if err != nil {
		return nil, instrumentError("create lifetime destroyed counter", err)
	}

	instruments.registration, err = meter.RegisterCallback(
		func(_ context.Context, observer metric.Observer) error {
			for _, pool := range pools {
				if pool == nil || pool.backend == nil {
					continue
				}
				instruments.observePool(observer, pool.role, pool.backend.Stats())
			}

			return nil
		},
		instruments.connectionsTotal,
		instruments.connectionsIdle,
		instruments.connectionsAcquired,
		instruments.connectionsConstructing,
		instruments.connectionsMax,
		instruments.acquiresTotal,
		instruments.canceledAcquiresTotal,
		instruments.acquireDurationTotal,
		instruments.emptyAcquiresTotal,
		instruments.emptyAcquireWaitTotal,
		instruments.newConnectionsTotal,
		instruments.idleDestroyedTotal,
		instruments.lifetimeDestroyedTotal,
	)
	if err != nil {
		return nil, instrumentError("register pool metrics callback", err)
	}

	return instruments, nil
}

func (instruments *instruments) observePool(
	observer metric.Observer,
	role poolRole,
	stats poolStats,
) {
	options := metric.WithAttributes(attribute.String("postgres.pool", string(role)))
	observer.ObserveInt64(instruments.connectionsTotal, int64(stats.totalConns), options)
	observer.ObserveInt64(instruments.connectionsIdle, int64(stats.idleConns), options)
	observer.ObserveInt64(instruments.connectionsAcquired, int64(stats.acquiredConns), options)
	observer.ObserveInt64(instruments.connectionsConstructing, int64(stats.constructingConns), options)
	observer.ObserveInt64(instruments.connectionsMax, int64(stats.maxConns), options)
	observer.ObserveInt64(instruments.acquiresTotal, stats.acquireCount, options)
	observer.ObserveInt64(instruments.canceledAcquiresTotal, stats.canceledAcquireCount, options)
	observer.ObserveFloat64(
		instruments.acquireDurationTotal,
		stats.acquireDuration.Seconds(),
		options,
	)
	observer.ObserveInt64(instruments.emptyAcquiresTotal, stats.emptyAcquireCount, options)
	observer.ObserveFloat64(
		instruments.emptyAcquireWaitTotal,
		stats.emptyAcquireWaitTime.Seconds(),
		options,
	)
	observer.ObserveInt64(instruments.newConnectionsTotal, stats.newConnsCount, options)
	observer.ObserveInt64(instruments.idleDestroyedTotal, stats.maxIdleDestroyCount, options)
	observer.ObserveInt64(
		instruments.lifetimeDestroyedTotal,
		stats.maxLifetimeDestroyCnt,
		options,
	)
}

func (instruments *instruments) recordTransactionAttempt(
	ctx context.Context,
	isolation IsolationLevel,
	readOnly bool,
) {
	if instruments == nil {
		return
	}
	instruments.transactionAttempts.Add(
		ctx,
		1,
		transactionMetricOptions(isolation, readOnly),
	)
}

func (instruments *instruments) recordTransactionRetry(ctx context.Context, reason string) {
	if instruments == nil {
		return
	}
	instruments.transactionRetries.Add(
		ctx,
		1,
		metric.WithAttributes(attribute.String("postgres.retry.reason", reason)),
	)
}

func (instruments *instruments) recordTransactionDuration(
	ctx context.Context,
	duration time.Duration,
	isolation IsolationLevel,
	readOnly bool,
	result string,
) {
	if instruments == nil {
		return
	}
	instruments.transactionDuration.Record(
		ctx,
		duration.Seconds(),
		transactionMetricOptions(
			isolation,
			readOnly,
			attribute.String("postgres.transaction.result", result),
		),
	)
}

func (instruments *instruments) unregister() error {
	if instruments == nil || instruments.registration == nil {
		return nil
	}

	return instruments.registration.Unregister()
}

func instrumentError(operation string, err error) error {
	if err == nil {
		return nil
	}

	return fmt.Errorf("postgres: %s: %w", operation, err)
}

var _ pgx.QueryTracer = (*queryTracer)(nil)
