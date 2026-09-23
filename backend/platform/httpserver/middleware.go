package httpserver

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/v0hmly/marketmesh/platform/logger"
	"github.com/v0hmly/marketmesh/platform/telemetry"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

const instrumentationName = "github.com/v0hmly/marketmesh/platform/httpserver"

func bodyLimitMiddleware(limit int64, next http.Handler) http.Handler {
	limited := http.MaxBytesHandler(next, limit)

	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.ContentLength > limit {
			http.Error(response, "request body too large", http.StatusRequestEntityTooLarge)
			return
		}

		limited.ServeHTTP(response, request)
	})
}

func requestDeadlineMiddleware(timeout time.Duration, next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		ctx, cancel := context.WithTimeout(request.Context(), timeout)
		defer cancel()

		next.ServeHTTP(response, request.WithContext(ctx))
	})
}

func recoveryMiddleware(log *logger.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				log.ErrorContext(
					request.Context(),
					"HTTP panic перехвачен",
					logger.String("panic_type", fmt.Sprintf("%T", recovered)),
				)
				http.Error(response, "internal server error", http.StatusInternalServerError)
			}
		}()

		next.ServeHTTP(response, request)
	})
}

func observedLoggingMiddleware(log *logger.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		started := time.Now()
		observed := newObservedResponseWriter(response)
		next.ServeHTTP(observed, request)

		fields := requestFields(request, observed.Status())
		fields = append(fields, logger.Duration("duration", time.Since(started)))
		log.InfoContext(request.Context(), "HTTP запрос завершён", fields...)
	})
}

func telemetryMiddleware(
	pipeline *telemetry.Telemetry,
	next http.Handler,
) (http.Handler, error) {
	duration, err := pipeline.Meter(instrumentationName).Float64Histogram(
		"http.server.request.duration",
		metric.WithDescription("Время обработки входящего HTTP-запроса."),
		metric.WithUnit("s"),
	)
	if err != nil {
		return nil, fmt.Errorf("httpserver: create request duration metric: %w", err)
	}

	tracer := pipeline.Tracer(instrumentationName)
	propagator := pipeline.Propagator()

	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		started := time.Now()
		method := normalizedMethod(request.Method)
		ctx := propagator.Extract(
			request.Context(),
			propagation.HeaderCarrier(request.Header),
		)
		ctx, span := tracer.Start(
			ctx,
			"HTTP "+method,
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(semconv.HTTPRequestMethodKey.String(method)),
		)
		defer span.End()

		observed := newObservedResponseWriter(response)
		tracedRequest := request.WithContext(ctx)
		next.ServeHTTP(observed, tracedRequest)

		status := observed.Status()
		attributes := requestAttributes(tracedRequest, status)
		span.SetAttributes(attributes...)
		if route := routePattern(tracedRequest); route != "" {
			span.SetName(method + " " + route)
		}
		if status >= http.StatusInternalServerError {
			span.SetStatus(codes.Error, "internal server error")
		}

		duration.Record(
			ctx,
			time.Since(started).Seconds(),
			metric.WithAttributes(attributes...),
		)
	}), nil
}

func requestFields(request *http.Request, status int) []logger.Field {
	fields := []logger.Field{
		logger.String("http_method", normalizedMethod(request.Method)),
		logger.Int("http_status", status),
	}
	if route := routePattern(request); route != "" {
		fields = append(fields, logger.String("http_route", route))
	}

	return fields
}

func requestAttributes(request *http.Request, status int) []attribute.KeyValue {
	attributes := []attribute.KeyValue{
		semconv.HTTPRequestMethodKey.String(normalizedMethod(request.Method)),
		semconv.HTTPResponseStatusCodeKey.Int(status),
	}
	if route := routePattern(request); route != "" {
		attributes = append(attributes, semconv.HTTPRouteKey.String(route))
	}

	return attributes
}

func normalizedMethod(method string) string {
	switch method {
	case http.MethodConnect,
		http.MethodDelete,
		http.MethodGet,
		http.MethodHead,
		http.MethodOptions,
		http.MethodPatch,
		http.MethodPost,
		http.MethodPut,
		http.MethodTrace,
		"QUERY":
		return method
	default:
		return "_OTHER"
	}
}

func routePattern(request *http.Request) string {
	pattern := strings.TrimSpace(request.Pattern)
	if _, route, found := strings.Cut(pattern, " "); found {
		return route
	}

	return pattern
}
