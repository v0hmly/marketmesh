package redis

import (
	"context"
	"errors"
	"net"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

type operationHook struct {
	role        Role
	tracer      trace.Tracer
	instruments *instruments
}

func (hook *operationHook) DialHook(next goredis.DialHook) goredis.DialHook {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		var connection net.Conn
		err := hook.observe(ctx, "connect", func(operationCtx context.Context) error {
			var dialErr error
			connection, dialErr = next(operationCtx, network, address)

			return dialErr
		})

		return connection, err
	}
}

func (hook *operationHook) ProcessHook(next goredis.ProcessHook) goredis.ProcessHook {
	return func(ctx context.Context, command goredis.Cmder) error {
		return hook.observe(ctx, "command", func(operationCtx context.Context) error {
			return next(operationCtx, command)
		})
	}
}

func (hook *operationHook) ProcessPipelineHook(
	next goredis.ProcessPipelineHook,
) goredis.ProcessPipelineHook {
	return func(ctx context.Context, commands []goredis.Cmder) error {
		return hook.observe(ctx, "pipeline", func(operationCtx context.Context) error {
			return next(operationCtx, commands)
		})
	}
}

func (hook *operationHook) observe(
	ctx context.Context,
	kind string,
	operation func(context.Context) error,
) error {
	started := time.Now()
	ctx, span := hook.tracer.Start(
		ctx,
		"redis."+kind,
		trace.WithAttributes(
			attribute.String("redis.role", string(hook.role)),
			attribute.String("redis.operation.kind", kind),
		),
	)
	err := operation(ctx)
	resultErr := err
	if contextErr := ctx.Err(); contextErr != nil {
		resultErr = contextErr
	}
	result := operationResult(resultErr)
	span.SetAttributes(attribute.String("redis.operation.result", result))
	if resultErr != nil {
		span.SetStatus(codes.Error, operationStatus(result))
	}
	span.End()
	hook.instruments.recordOperation(ctx, kind, result, time.Since(started))

	return err
}

func operationResult(err error) string {
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case errors.Is(err, goredis.ErrPoolTimeout), errors.Is(err, goredis.ErrPoolExhausted):
		return "pool_timeout"
	default:
		return "error"
	}
}

func operationStatus(result string) string {
	switch result {
	case "canceled":
		return "operation canceled"
	case "timeout":
		return "operation timed out"
	case "pool_timeout":
		return "connection pool unavailable"
	default:
		return "operation failed"
	}
}

type instruments struct {
	role Role
	pool clientBackend
	max  int

	operationAttempts metric.Int64Counter
	operationDuration metric.Float64Histogram
	operationRetries  metric.Int64Counter

	connectionsTotal metric.Int64ObservableGauge
	connectionsIdle  metric.Int64ObservableGauge
	connectionsMax   metric.Int64ObservableGauge
	pendingRequests  metric.Int64ObservableGauge
	poolHits         metric.Int64ObservableCounter
	poolMisses       metric.Int64ObservableCounter
	poolTimeouts     metric.Int64ObservableCounter
	poolWaits        metric.Int64ObservableCounter
	poolWaitDuration metric.Float64ObservableCounter
	staleConnections metric.Int64ObservableCounter
	unusableConns    metric.Int64ObservableCounter

	registration metric.Registration
}

func newInstruments(
	meter metric.Meter,
	role Role,
	pool clientBackend,
	maxConnections int,
) (*instruments, error) {
	instruments := &instruments{role: role, pool: pool, max: maxConnections}
	var err error
	instruments.operationAttempts, err = meter.Int64Counter(
		"marketmesh.redis.operation.attempts",
		metric.WithDescription("Number of Redis operation attempts."),
		metric.WithUnit("{attempt}"),
	)
	if err != nil {
		return nil, instrumentError("create operation attempts counter", err)
	}
	instruments.operationDuration, err = meter.Float64Histogram(
		"marketmesh.redis.operation.duration",
		metric.WithDescription("Duration of Redis operation attempts."),
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, instrumentError("create operation duration histogram", err)
	}
	instruments.operationRetries, err = meter.Int64Counter(
		"marketmesh.redis.operation.retries",
		metric.WithDescription("Number of explicit safe Redis operation retries."),
		metric.WithUnit("{retry}"),
	)
	if err != nil {
		return nil, instrumentError("create operation retries counter", err)
	}

	instruments.connectionsTotal, err = meter.Int64ObservableGauge(
		"marketmesh.redis.connections.total",
		metric.WithDescription("Current total Redis pool connections."),
		metric.WithUnit("{connection}"),
	)
	if err != nil {
		return nil, instrumentError("create total connections gauge", err)
	}
	instruments.connectionsIdle, err = meter.Int64ObservableGauge(
		"marketmesh.redis.connections.idle",
		metric.WithDescription("Current idle Redis pool connections."),
		metric.WithUnit("{connection}"),
	)
	if err != nil {
		return nil, instrumentError("create idle connections gauge", err)
	}
	instruments.connectionsMax, err = meter.Int64ObservableGauge(
		"marketmesh.redis.connections.max",
		metric.WithDescription("Configured maximum Redis pool connections."),
		metric.WithUnit("{connection}"),
	)
	if err != nil {
		return nil, instrumentError("create maximum connections gauge", err)
	}
	instruments.pendingRequests, err = meter.Int64ObservableGauge(
		"marketmesh.redis.pool.requests.pending",
		metric.WithDescription("Current Redis pool requests waiting for a connection."),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return nil, instrumentError("create pending pool requests gauge", err)
	}
	instruments.poolHits, err = meter.Int64ObservableCounter(
		"marketmesh.redis.pool.hits",
		metric.WithDescription("Total Redis pool hits."),
		metric.WithUnit("{hit}"),
	)
	if err != nil {
		return nil, instrumentError("create pool hits counter", err)
	}
	instruments.poolMisses, err = meter.Int64ObservableCounter(
		"marketmesh.redis.pool.misses",
		metric.WithDescription("Total Redis pool misses."),
		metric.WithUnit("{miss}"),
	)
	if err != nil {
		return nil, instrumentError("create pool misses counter", err)
	}
	instruments.poolTimeouts, err = meter.Int64ObservableCounter(
		"marketmesh.redis.pool.timeouts",
		metric.WithDescription("Total Redis pool wait timeouts."),
		metric.WithUnit("{timeout}"),
	)
	if err != nil {
		return nil, instrumentError("create pool timeouts counter", err)
	}
	instruments.poolWaits, err = meter.Int64ObservableCounter(
		"marketmesh.redis.pool.waits",
		metric.WithDescription("Total Redis pool waits."),
		metric.WithUnit("{wait}"),
	)
	if err != nil {
		return nil, instrumentError("create pool waits counter", err)
	}
	instruments.poolWaitDuration, err = meter.Float64ObservableCounter(
		"marketmesh.redis.pool.wait.duration",
		metric.WithDescription("Cumulative Redis pool wait duration."),
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, instrumentError("create pool wait duration counter", err)
	}
	instruments.staleConnections, err = meter.Int64ObservableCounter(
		"marketmesh.redis.connections.stale",
		metric.WithDescription("Total stale Redis connections removed."),
		metric.WithUnit("{connection}"),
	)
	if err != nil {
		return nil, instrumentError("create stale connections counter", err)
	}
	instruments.unusableConns, err = meter.Int64ObservableCounter(
		"marketmesh.redis.connections.unusable",
		metric.WithDescription("Total unusable Redis connections."),
		metric.WithUnit("{connection}"),
	)
	if err != nil {
		return nil, instrumentError("create unusable connections counter", err)
	}

	instruments.registration, err = meter.RegisterCallback(
		instruments.observePool,
		instruments.connectionsTotal,
		instruments.connectionsIdle,
		instruments.connectionsMax,
		instruments.pendingRequests,
		instruments.poolHits,
		instruments.poolMisses,
		instruments.poolTimeouts,
		instruments.poolWaits,
		instruments.poolWaitDuration,
		instruments.staleConnections,
		instruments.unusableConns,
	)
	if err != nil {
		return nil, instrumentError("register pool metrics callback", err)
	}

	return instruments, nil
}

func (instruments *instruments) observePool(
	_ context.Context,
	observer metric.Observer,
) error {
	if instruments == nil || instruments.pool == nil {
		return nil
	}
	stats := instruments.pool.PoolStats()
	if stats == nil {
		return nil
	}
	options := metric.WithAttributes(attribute.String("redis.role", string(instruments.role)))
	observer.ObserveInt64(instruments.connectionsTotal, int64(stats.TotalConns), options)
	observer.ObserveInt64(instruments.connectionsIdle, int64(stats.IdleConns), options)
	observer.ObserveInt64(instruments.connectionsMax, int64(instruments.max), options)
	observer.ObserveInt64(instruments.pendingRequests, int64(stats.PendingRequests), options)
	observer.ObserveInt64(instruments.poolHits, int64(stats.Hits), options)
	observer.ObserveInt64(instruments.poolMisses, int64(stats.Misses), options)
	observer.ObserveInt64(instruments.poolTimeouts, int64(stats.Timeouts), options)
	observer.ObserveInt64(instruments.poolWaits, int64(stats.WaitCount), options)
	observer.ObserveFloat64(
		instruments.poolWaitDuration,
		time.Duration(stats.WaitDurationNs).Seconds(),
		options,
	)
	observer.ObserveInt64(instruments.staleConnections, int64(stats.StaleConns), options)
	observer.ObserveInt64(instruments.unusableConns, int64(stats.Unusable), options)

	return nil
}

func (instruments *instruments) recordOperation(
	ctx context.Context,
	kind string,
	result string,
	duration time.Duration,
) {
	if instruments == nil ||
		instruments.operationAttempts == nil ||
		instruments.operationDuration == nil {
		return
	}
	options := metric.WithAttributes(
		attribute.String("redis.role", string(instruments.role)),
		attribute.String("redis.operation.kind", kind),
		attribute.String("redis.operation.result", result),
	)
	instruments.operationAttempts.Add(ctx, 1, options)
	instruments.operationDuration.Record(ctx, duration.Seconds(), options)
}

func (instruments *instruments) recordRetry(ctx context.Context, reason string) {
	if instruments == nil || instruments.operationRetries == nil {
		return
	}
	instruments.operationRetries.Add(
		ctx,
		1,
		metric.WithAttributes(
			attribute.String("redis.role", string(instruments.role)),
			attribute.String("redis.retry.reason", reason),
		),
	)
}

func (instruments *instruments) unregister() error {
	if instruments == nil || instruments.registration == nil {
		return nil
	}

	return instruments.registration.Unregister()
}

var _ goredis.Hook = (*operationHook)(nil)
