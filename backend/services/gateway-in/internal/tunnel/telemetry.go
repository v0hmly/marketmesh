package tunnel

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"time"

	contractv1 "github.com/v0hmly/marketmesh/api/gen/go/tunnel/v1"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

const instrumentationName = "github.com/v0hmly/marketmesh/services/gateway-in/internal/tunnel"

type instrumentation struct {
	tracer trace.Tracer

	activeTunnels  metric.Int64UpDownCounter
	activeRequests metric.Int64UpDownCounter
	queuedFrames   metric.Int64UpDownCounter
	frames         metric.Int64Counter
	policyRefusals metric.Int64Counter
	tunnelFailures metric.Int64Counter
	selections     metric.Int64Counter
	requestLatency metric.Float64Histogram
}

type requestMetric struct {
	dataCenter string
	route      contractv1.RouteId
	class      contractv1.TrafficClass
}

type requestResultMetric struct {
	requestMetric
	started time.Time
	err     error
}

func newInstrumentation(
	meterProvider metric.MeterProvider,
	tracerProvider trace.TracerProvider,
) (*instrumentation, error) {
	if isNilProvider(meterProvider) {
		meterProvider = metricnoop.NewMeterProvider()
	}
	if isNilProvider(tracerProvider) {
		tracerProvider = tracenoop.NewTracerProvider()
	}

	meter := meterProvider.Meter(instrumentationName)
	activeTunnels, err := meter.Int64UpDownCounter(
		"marketmesh.gateway_in.tunnel.active",
		metric.WithDescription("Current number of authenticated reverse tunnels."),
		metric.WithUnit("{tunnel}"),
	)
	if err != nil {
		return nil, fmt.Errorf("tunnel: create active tunnel metric: %w", err)
	}
	activeRequests, err := meter.Int64UpDownCounter(
		"marketmesh.gateway_in.tunnel.requests.active",
		metric.WithDescription("Current logical requests by bounded data center, route, and class."),
		metric.WithUnit("{request}"),
	)
	if err != nil {
		return nil, fmt.Errorf("tunnel: create active request metric: %w", err)
	}
	queuedFrames, err := meter.Int64UpDownCounter(
		"marketmesh.gateway_in.tunnel.frames.queued",
		metric.WithDescription("Current outbound frames in bounded queues."),
		metric.WithUnit("{frame}"),
	)
	if err != nil {
		return nil, fmt.Errorf("tunnel: create queued frame metric: %w", err)
	}
	frames, err := meter.Int64Counter(
		"marketmesh.gateway_in.tunnel.frames",
		metric.WithDescription("Total validated reverse tunnel frames."),
		metric.WithUnit("{frame}"),
	)
	if err != nil {
		return nil, fmt.Errorf("tunnel: create frame metric: %w", err)
	}
	policyRefusals, err := meter.Int64Counter(
		"marketmesh.gateway_in.tunnel.policy.refusals",
		metric.WithDescription("Total finite deny-by-default policy refusals."),
		metric.WithUnit("{refusal}"),
	)
	if err != nil {
		return nil, fmt.Errorf("tunnel: create policy refusal metric: %w", err)
	}
	tunnelFailures, err := meter.Int64Counter(
		"marketmesh.gateway_in.tunnel.failures",
		metric.WithDescription("Total reverse tunnel terminations by finite reason."),
		metric.WithUnit("{failure}"),
	)
	if err != nil {
		return nil, fmt.Errorf("tunnel: create failure metric: %w", err)
	}
	selections, err := meter.Int64Counter(
		"marketmesh.gateway_in.tunnel.selections",
		metric.WithDescription("Total route selections by bounded data center and status."),
		metric.WithUnit("{selection}"),
	)
	if err != nil {
		return nil, fmt.Errorf("tunnel: create selection metric: %w", err)
	}
	requestLatency, err := meter.Float64Histogram(
		"marketmesh.gateway_in.tunnel.request.duration",
		metric.WithDescription("Logical request duration through the reverse tunnel."),
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, fmt.Errorf("tunnel: create request duration metric: %w", err)
	}

	return &instrumentation{
		tracer:         tracerProvider.Tracer(instrumentationName),
		activeTunnels:  activeTunnels,
		activeRequests: activeRequests,
		queuedFrames:   queuedFrames,
		frames:         frames,
		policyRefusals: policyRefusals,
		tunnelFailures: tunnelFailures,
		selections:     selections,
		requestLatency: requestLatency,
	}, nil
}

func isNilProvider(provider any) bool {
	if provider == nil {
		return true
	}
	value := reflect.ValueOf(provider)
	return value.Kind() == reflect.Pointer && value.IsNil()
}

func (i *instrumentation) tunnelDelta(ctx context.Context, dataCenter string, delta int64) {
	i.activeTunnels.Add(
		ctx,
		delta,
		metric.WithAttributes(attribute.String("tunnel.data_center", dataCenter)),
	)
}

func (i *instrumentation) requestDelta(
	ctx context.Context,
	request requestMetric,
	delta int64,
) {
	i.activeRequests.Add(
		ctx,
		delta,
		metric.WithAttributes(
			attribute.String("tunnel.data_center", request.dataCenter),
			attribute.String("tunnel.route", routeLabel(request.route)),
			attribute.String("tunnel.traffic_class", classLabel(request.class)),
		),
	)
}

func (i *instrumentation) queueDelta(
	ctx context.Context,
	class contractv1.TrafficClass,
	delta int64,
) {
	i.queuedFrames.Add(
		ctx,
		delta,
		metric.WithAttributes(attribute.String("tunnel.traffic_class", classLabel(class))),
	)
}

func (i *instrumentation) recordFrame(
	ctx context.Context,
	direction string,
	class contractv1.TrafficClass,
	frameType string,
) {
	i.frames.Add(
		ctx,
		1,
		metric.WithAttributes(
			attribute.String("tunnel.direction", direction),
			attribute.String("tunnel.traffic_class", classLabel(class)),
			attribute.String("tunnel.frame_type", frameType),
		),
	)
}

func (i *instrumentation) refusal(ctx context.Context, reason string) {
	i.policyRefusals.Add(
		ctx,
		1,
		metric.WithAttributes(attribute.String("tunnel.refusal.reason", reason)),
	)
}

func (i *instrumentation) failure(ctx context.Context, dataCenter string, reason string) {
	i.tunnelFailures.Add(
		ctx,
		1,
		metric.WithAttributes(
			attribute.String("tunnel.data_center", dataCenter),
			attribute.String("tunnel.failure.reason", reason),
		),
	)
}

func (i *instrumentation) selection(
	ctx context.Context,
	dataCenter string,
	route contractv1.RouteId,
	status string,
) {
	i.selections.Add(
		ctx,
		1,
		metric.WithAttributes(
			attribute.String("tunnel.data_center", dataCenter),
			attribute.String("tunnel.route", routeLabel(route)),
			attribute.String("tunnel.selection.status", status),
		),
	)
}

func (i *instrumentation) finishRequest(
	ctx context.Context,
	request requestResultMetric,
) {
	result := "ok"
	var resultErr *ResultError
	switch {
	case request.err == nil:
	case errors.As(request.err, &resultErr):
		result = resultLabel(resultErr.Code())
	case errors.Is(request.err, context.Canceled):
		result = "canceled"
	case errors.Is(request.err, context.DeadlineExceeded):
		result = "deadline_exceeded"
	case errors.Is(request.err, ErrQueueFull):
		result = "resource_exhausted"
	case errors.Is(request.err, ErrNoTunnel), errors.Is(request.err, ErrTunnelClosed),
		errors.Is(request.err, ErrDraining):
		result = "unavailable"
	default:
		result = "internal"
	}

	i.requestLatency.Record(
		ctx,
		time.Since(request.started).Seconds(),
		metric.WithAttributes(
			attribute.String("tunnel.data_center", request.dataCenter),
			attribute.String("tunnel.route", routeLabel(request.route)),
			attribute.String("tunnel.traffic_class", classLabel(request.class)),
			attribute.String("tunnel.result", result),
		),
	)
}

func outboundFrameType(frame *contractv1.ConnectResponse) string {
	switch frame.GetPayload().(type) {
	case *contractv1.ConnectResponse_Hello:
		return "hello"
	case *contractv1.ConnectResponse_Open:
		return "open"
	case *contractv1.ConnectResponse_Data:
		return "data"
	case *contractv1.ConnectResponse_HalfClose:
		return "half_close"
	case *contractv1.ConnectResponse_Cancel:
		return "cancel"
	case *contractv1.ConnectResponse_Credit:
		return "credit"
	case *contractv1.ConnectResponse_Ping:
		return "ping"
	case *contractv1.ConnectResponse_Pong:
		return "pong"
	case *contractv1.ConnectResponse_Drain:
		return "drain"
	default:
		return "unknown"
	}
}

func inboundFrameType(frame *contractv1.ConnectRequest) string {
	switch frame.GetPayload().(type) {
	case *contractv1.ConnectRequest_Hello:
		return "hello"
	case *contractv1.ConnectRequest_Data:
		return "data"
	case *contractv1.ConnectRequest_HalfClose:
		return "half_close"
	case *contractv1.ConnectRequest_Cancel:
		return "cancel"
	case *contractv1.ConnectRequest_Result:
		return "result"
	case *contractv1.ConnectRequest_Credit:
		return "credit"
	case *contractv1.ConnectRequest_Ping:
		return "ping"
	case *contractv1.ConnectRequest_Pong:
		return "pong"
	case *contractv1.ConnectRequest_Drain:
		return "drain"
	case *contractv1.ConnectRequest_RevokeSession:
		return "revoke_session"
	default:
		return "unknown"
	}
}
