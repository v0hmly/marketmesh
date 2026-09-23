package tunnel

import (
	"context"
	"fmt"
	"time"

	contractv1 "github.com/v0hmly/marketmesh/api/gen/go/tunnel/v1"
	"github.com/v0hmly/marketmesh/platform/telemetry"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

const instrumentationName = "github.com/v0hmly/marketmesh/services/gateway-out/internal/tunnel"

type observer struct {
	tracer            trace.Tracer
	connections       metric.Int64UpDownCounter
	reconnectAttempts metric.Int64Counter
	frames            metric.Int64Counter
	activeRequests    metric.Int64UpDownCounter
	requestDuration   metric.Float64Histogram
	protocolFailures  metric.Int64Counter
}

func newObserver(pipeline *telemetry.Telemetry) (observer, error) {
	meter := pipeline.Meter(instrumentationName)
	connections, err := meter.Int64UpDownCounter(
		"marketmesh.gateway_out.tunnel.connections",
		metric.WithDescription("Current ready reverse tunnel connections."),
	)
	if err != nil {
		return observer{}, fmt.Errorf("gateway-out tunnel create connections metric: %w", err)
	}
	reconnectAttempts, err := meter.Int64Counter(
		"marketmesh.gateway_out.tunnel.reconnect_attempts",
		metric.WithDescription("Bounded reverse tunnel connection attempts."),
	)
	if err != nil {
		return observer{}, fmt.Errorf("gateway-out tunnel create reconnect metric: %w", err)
	}
	frames, err := meter.Int64Counter(
		"marketmesh.gateway_out.tunnel.frames",
		metric.WithDescription("Reverse tunnel frames by bounded direction and type."),
	)
	if err != nil {
		return observer{}, fmt.Errorf("gateway-out tunnel create frames metric: %w", err)
	}
	activeRequests, err := meter.Int64UpDownCounter(
		"marketmesh.gateway_out.tunnel.active_requests",
		metric.WithDescription("Current logical requests by allowlisted route and traffic class."),
	)
	if err != nil {
		return observer{}, fmt.Errorf("gateway-out tunnel create active requests metric: %w", err)
	}
	requestDuration, err := meter.Float64Histogram(
		"marketmesh.gateway_out.tunnel.request.duration",
		metric.WithDescription("Internal RPC duration by allowlisted route, class, and safe result."),
		metric.WithUnit("s"),
		metric.WithExplicitBucketBoundaries(.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10, 30),
	)
	if err != nil {
		return observer{}, fmt.Errorf("gateway-out tunnel create duration metric: %w", err)
	}
	protocolFailures, err := meter.Int64Counter(
		"marketmesh.gateway_out.tunnel.protocol_failures",
		metric.WithDescription("Fail-closed tunnel protocol failures by bounded category."),
	)
	if err != nil {
		return observer{}, fmt.Errorf("gateway-out tunnel create protocol metric: %w", err)
	}

	return observer{
		tracer:            pipeline.Tracer(instrumentationName),
		connections:       connections,
		reconnectAttempts: reconnectAttempts,
		frames:            frames,
		activeRequests:    activeRequests,
		requestDuration:   requestDuration,
		protocolFailures:  protocolFailures,
	}, nil
}

func (observer observer) connection(ctx context.Context, delta int64) {
	observer.connections.Add(ctx, delta)
}

func (observer observer) reconnect(ctx context.Context, outcome string) {
	observer.reconnectAttempts.Add(
		ctx,
		1,
		metric.WithAttributes(attribute.String("outcome", outcome)),
	)
}

func (observer observer) frame(
	ctx context.Context,
	direction string,
	frameType string,
	class contractv1.TrafficClass,
) {
	observer.frames.Add(
		ctx,
		1,
		metric.WithAttributes(
			attribute.String("direction", direction),
			attribute.String("frame.type", frameType),
			attribute.String("traffic.class", class.String()),
		),
	)
}

func (observer observer) requestStarted(
	ctx context.Context,
	route route,
) (context.Context, trace.Span, time.Time) {
	attributes := routeAttributes(route)
	observer.activeRequests.Add(ctx, 1, metric.WithAttributes(attributes...))
	ctx, span := observer.tracer.Start(
		ctx,
		"gateway-out internal RPC",
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attributes...),
	)

	return ctx, span, time.Now()
}

func (observer observer) requestFinished(
	ctx context.Context,
	route route,
	span trace.Span,
	started time.Time,
	result contractv1.ResultCode,
) {
	attributes := append(
		routeAttributes(route),
		attribute.String("result.code", result.String()),
	)
	observer.activeRequests.Add(ctx, -1, metric.WithAttributes(routeAttributes(route)...))
	observer.requestDuration.Record(
		ctx,
		time.Since(started).Seconds(),
		metric.WithAttributes(attributes...),
	)
	if result != contractv1.ResultCode_RESULT_CODE_OK {
		span.SetStatus(codes.Error, "internal RPC failed")
	}
	span.End()
}

func (observer observer) protocolFailure(ctx context.Context, category string) {
	observer.protocolFailures.Add(
		ctx,
		1,
		metric.WithAttributes(attribute.String("category", category)),
	)
}

func routeAttributes(route route) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String("route.id", route.ID.String()),
		attribute.String("traffic.class", route.TrafficClass.String()),
		attribute.Bool("route.mutating", route.Mutating),
	}
}
