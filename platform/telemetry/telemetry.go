package telemetry

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetricgrpc"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/metric"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
	"google.golang.org/grpc/credentials"
)

// Telemetry объединяет изолированные providers и propagation для сервиса.
// Конструктор не изменяет глобальное состояние OpenTelemetry.
type Telemetry struct {
	tracerProvider trace.TracerProvider
	meterProvider  metric.MeterProvider
	propagator     propagation.TextMapPropagator

	sdkTracerProvider *sdktrace.TracerProvider
	sdkMeterProvider  *sdkmetric.MeterProvider

	shutdownOnce sync.Once
	shutdownErr  error
}

// New создаёт OpenTelemetry pipeline с OTLP/gRPC exporters либо переданными
// тестовыми зависимостями.
func New(ctx context.Context, config Config, configOptions ...Option) (*Telemetry, error) {
	if ctx == nil {
		return nil, errors.New("telemetry: context must not be nil")
	}

	options, err := applyOptions(configOptions)
	if err != nil {
		return nil, err
	}
	settings, err := normalizeConfig(config, !options.hasSpanExporter || !options.hasMetricReader)
	if err != nil {
		return nil, err
	}

	resource, err := newResource(ctx, settings)
	if err != nil {
		return nil, err
	}

	spanExporter := options.spanExporter
	if !options.hasSpanExporter {
		spanExporter, err = newOTLPSpanExporter(ctx, settings)
		if err != nil {
			return nil, err
		}
	}

	tracerProvider := sdktrace.NewTracerProvider(
		sdktrace.WithResource(resource),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(settings.traceSampleRatio))),
		sdktrace.WithBatcher(
			spanExporter,
			sdktrace.WithBatchTimeout(settings.traceBatchTimeout),
			sdktrace.WithExportTimeout(settings.exportTimeout),
		),
	)

	metricReader := options.metricReader
	if !options.hasMetricReader {
		metricReader, err = newOTLPMetricReader(ctx, settings)
		if err != nil {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), settings.exportTimeout)
			defer cancel()
			cleanupErr := tracerProvider.Shutdown(cleanupCtx)
			return nil, errors.Join(err, wrapCleanupError(cleanupErr))
		}
	}

	meterProvider := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(resource),
		sdkmetric.WithReader(metricReader),
		sdkmetric.WithCardinalityLimit(settings.metricCardinalityLimit),
	)

	return &Telemetry{
		tracerProvider:    tracerProvider,
		meterProvider:     meterProvider,
		propagator:        propagation.TraceContext{},
		sdkTracerProvider: tracerProvider,
		sdkMeterProvider:  meterProvider,
	}, nil
}

// NewNoop создаёт полностью изолированный no-op pipeline без exporters.
func NewNoop() *Telemetry {
	return &Telemetry{
		tracerProvider: tracenoop.NewTracerProvider(),
		meterProvider:  metricnoop.NewMeterProvider(),
		propagator:     propagation.TraceContext{},
	}
}

// TracerProvider возвращает provider этого экземпляра.
func (telemetry *Telemetry) TracerProvider() trace.TracerProvider {
	return telemetry.tracerProvider
}

// MeterProvider возвращает provider этого экземпляра.
func (telemetry *Telemetry) MeterProvider() metric.MeterProvider {
	return telemetry.meterProvider
}

// Propagator возвращает W3C Trace Context propagator без Baggage.
func (telemetry *Telemetry) Propagator() propagation.TextMapPropagator {
	return telemetry.propagator
}

// Tracer создаёт tracer из provider этого экземпляра.
func (telemetry *Telemetry) Tracer(name string, options ...trace.TracerOption) trace.Tracer {
	return telemetry.tracerProvider.Tracer(name, options...)
}

// Meter создаёт meter из provider этого экземпляра.
func (telemetry *Telemetry) Meter(name string, options ...metric.MeterOption) metric.Meter {
	return telemetry.meterProvider.Meter(name, options...)
}

// ForceFlush синхронно отправляет накопленные spans и metrics.
func (telemetry *Telemetry) ForceFlush(ctx context.Context) error {
	if ctx == nil {
		return errors.New("telemetry: context must not be nil")
	}

	var traceErr error
	if telemetry.sdkTracerProvider != nil {
		traceErr = telemetry.sdkTracerProvider.ForceFlush(ctx)
	}
	var metricErr error
	if telemetry.sdkMeterProvider != nil {
		metricErr = telemetry.sdkMeterProvider.ForceFlush(ctx)
	}

	return errors.Join(
		wrapOperationError("force flush traces", traceErr),
		wrapOperationError("force flush metrics", metricErr),
	)
}

// Shutdown останавливает metrics и traces не более одного раза. Вызывающая
// сторона должна передать context с deadline.
func (telemetry *Telemetry) Shutdown(ctx context.Context) error {
	if ctx == nil {
		return errors.New("telemetry: context must not be nil")
	}

	telemetry.shutdownOnce.Do(func() {
		var metricErr error
		if telemetry.sdkMeterProvider != nil {
			metricErr = telemetry.sdkMeterProvider.Shutdown(ctx)
		}
		var traceErr error
		if telemetry.sdkTracerProvider != nil {
			traceErr = telemetry.sdkTracerProvider.Shutdown(ctx)
		}

		telemetry.shutdownErr = errors.Join(
			wrapOperationError("shutdown metrics", metricErr),
			wrapOperationError("shutdown traces", traceErr),
		)
	})

	return telemetry.shutdownErr
}

func newResource(ctx context.Context, settings settings) (*resource.Resource, error) {
	result, err := resource.New(
		ctx,
		resource.WithFromEnv(),
		resource.WithTelemetrySDK(),
		resource.WithSchemaURL(semconv.SchemaURL),
		resource.WithAttributes(
			semconv.ServiceName(settings.serviceName),
			semconv.ServiceVersion(settings.serviceVersion),
			semconv.DeploymentEnvironmentNameKey.String(settings.environment),
			semconv.ServiceInstanceID(settings.instanceID),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("telemetry: create resource: %w", err)
	}

	return result, nil
}

func newOTLPSpanExporter(ctx context.Context, settings settings) (sdktrace.SpanExporter, error) {
	options := []otlptracegrpc.Option{
		otlptracegrpc.WithEndpoint(settings.endpoint),
		otlptracegrpc.WithTimeout(settings.exportTimeout),
	}
	if len(settings.headers) > 0 {
		options = append(options, otlptracegrpc.WithHeaders(settings.headers))
	}
	if settings.insecure {
		options = append(options, otlptracegrpc.WithInsecure())
	} else {
		options = append(options, otlptracegrpc.WithTLSCredentials(credentials.NewTLS(settings.tlsConfig)))
	}

	exporter, err := otlptracegrpc.New(ctx, options...)
	if err != nil {
		return nil, fmt.Errorf("telemetry: create OTLP trace exporter: %w", err)
	}

	return exporter, nil
}

func newOTLPMetricReader(ctx context.Context, settings settings) (sdkmetric.Reader, error) {
	options := []otlpmetricgrpc.Option{
		otlpmetricgrpc.WithEndpoint(settings.endpoint),
		otlpmetricgrpc.WithTimeout(settings.exportTimeout),
	}
	if len(settings.headers) > 0 {
		options = append(options, otlpmetricgrpc.WithHeaders(settings.headers))
	}
	if settings.insecure {
		options = append(options, otlpmetricgrpc.WithInsecure())
	} else {
		options = append(options, otlpmetricgrpc.WithTLSCredentials(credentials.NewTLS(settings.tlsConfig)))
	}

	exporter, err := otlpmetricgrpc.New(ctx, options...)
	if err != nil {
		return nil, fmt.Errorf("telemetry: create OTLP metric exporter: %w", err)
	}

	return sdkmetric.NewPeriodicReader(
		exporter,
		sdkmetric.WithInterval(settings.metricExportInterval),
		sdkmetric.WithTimeout(settings.exportTimeout),
	), nil
}

func wrapOperationError(operation string, err error) error {
	if err == nil {
		return nil
	}

	return fmt.Errorf("telemetry: %s: %w", operation, err)
}

func wrapCleanupError(err error) error {
	return wrapOperationError("cleanup traces after initialization failure", err)
}
