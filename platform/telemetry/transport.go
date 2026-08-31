package telemetry

import (
	"connectrpc.com/connect"
	"connectrpc.com/otelconnect"
	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"google.golang.org/grpc/stats"
)

// GRPCServerStatsHandler создаёт обработчик серверной gRPC-телеметрии.
func (telemetry *Telemetry) GRPCServerStatsHandler() stats.Handler {
	return otelgrpc.NewServerHandler(
		otelgrpc.WithTracerProvider(telemetry.tracerProvider),
		otelgrpc.WithMeterProvider(telemetry.meterProvider),
		otelgrpc.WithPropagators(telemetry.propagator),
	)
}

// GRPCClientStatsHandler создаёт обработчик клиентской gRPC-телеметрии.
func (telemetry *Telemetry) GRPCClientStatsHandler() stats.Handler {
	return otelgrpc.NewClientHandler(
		otelgrpc.WithTracerProvider(telemetry.tracerProvider),
		otelgrpc.WithMeterProvider(telemetry.meterProvider),
		otelgrpc.WithPropagators(telemetry.propagator),
	)
}

// PublicConnectInterceptor создаёт Connect interceptor для недоверенной
// публичной границы. Удалённый span становится link, а не родителем нового span.
func (telemetry *Telemetry) PublicConnectInterceptor() (connect.Interceptor, error) {
	return otelconnect.NewInterceptor(
		otelconnect.WithTracerProvider(telemetry.tracerProvider),
		otelconnect.WithMeterProvider(telemetry.meterProvider),
		otelconnect.WithPropagator(telemetry.propagator),
		otelconnect.WithoutServerPeerAttributes(),
		otelconnect.WithoutTraceEvents(),
	)
}

// TrustedConnectInterceptor создаёт Connect interceptor для доверенной
// внутренней границы, где решение удалённого sampler можно сохранить.
func (telemetry *Telemetry) TrustedConnectInterceptor() (connect.Interceptor, error) {
	return otelconnect.NewInterceptor(
		otelconnect.WithTracerProvider(telemetry.tracerProvider),
		otelconnect.WithMeterProvider(telemetry.meterProvider),
		otelconnect.WithPropagator(telemetry.propagator),
		otelconnect.WithoutServerPeerAttributes(),
		otelconnect.WithoutTraceEvents(),
		otelconnect.WithTrustRemote(),
	)
}
