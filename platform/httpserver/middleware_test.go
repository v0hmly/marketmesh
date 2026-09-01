package httpserver

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/v0hmly/marketmesh/platform/telemetry"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

func TestBodyLimitRejectsKnownOversizeRequest(t *testing.T) {
	t.Parallel()

	var handlerCalled atomic.Bool
	config := validTestConfig(t, &bytes.Buffer{})
	config.MaxBodyBytes = 4
	config.Handler = http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		handlerCalled.Store(true)
	})
	server, err := New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	request := newTestRequest(t, http.MethodPost, "/", strings.NewReader("12345"))
	response := newTestRecorder()
	server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", response.Code)
	}
	if handlerCalled.Load() {
		t.Fatal("handler was called for known oversize request")
	}
}

func TestBodyLimitUsesMaxBytesErrorForStreamingRequest(t *testing.T) {
	t.Parallel()

	config := validTestConfig(t, &bytes.Buffer{})
	config.MaxBodyBytes = 4
	config.Handler = http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		_, readErr := io.ReadAll(request.Body)
		var maxBytesErr *http.MaxBytesError
		if !errors.As(readErr, &maxBytesErr) {
			http.Error(response, "body was not limited", http.StatusInternalServerError)
			return
		}
		response.WriteHeader(http.StatusRequestEntityTooLarge)
	})
	server, err := New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	request := newTestRequest(t, http.MethodPost, "/", strings.NewReader("12345"))
	request.ContentLength = -1
	response := newTestRecorder()
	server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", response.Code)
	}
}

func TestRequestDeadlinePreservesShorterCallerDeadline(t *testing.T) {
	t.Parallel()

	const timeout = time.Second
	config := validTestConfig(t, &bytes.Buffer{})
	config.RequestTimeout = timeout
	config.Handler = http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		deadline, found := request.Context().Deadline()
		if !found {
			t.Fatal("handler context has no deadline")
		}
		callerDeadline := request.Context().Value(deadlineKey{}).(time.Time)
		if !deadline.Equal(callerDeadline) {
			t.Fatalf("handler deadline = %v, want caller deadline %v", deadline, callerDeadline)
		}
		response.WriteHeader(http.StatusNoContent)
	})
	server, err := New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	callerDeadline, _ := ctx.Deadline()
	ctx = context.WithValue(ctx, deadlineKey{}, callerDeadline)
	request := newTestRequest(t, http.MethodGet, "/", nil).WithContext(ctx)
	server.Handler.ServeHTTP(newTestRecorder(), request)
}

func TestRequestDeadlinePreservesCancellation(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	canceled := make(chan error, 1)
	config := validTestConfig(t, &bytes.Buffer{})
	config.Handler = http.HandlerFunc(func(_ http.ResponseWriter, request *http.Request) {
		close(started)
		<-request.Context().Done()
		canceled <- request.Context().Err()
	})
	server, err := New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		server.Handler.ServeHTTP(
			newTestRecorder(),
			newTestRequest(t, http.MethodGet, "/", nil).WithContext(ctx),
		)
	}()
	<-started
	cancel()

	select {
	case err := <-canceled:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("handler context error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("handler did not observe caller cancellation")
	}
	<-done
}

func TestRecoveryReturnsSafe500AndServerContinues(t *testing.T) {
	t.Parallel()

	const (
		panicSecret        = "panic-value-must-not-leak"
		authorizationValue = "Bearer authorization-must-not-leak"
		cookieValue        = "session=cookie-must-not-leak"
		bodyValue          = "body-must-not-leak"
		privatePath        = "/private-path-must-not-leak"
	)
	var panicNext atomic.Bool
	panicNext.Store(true)
	var logs bytes.Buffer
	config := validTestConfig(t, &logs)
	config.Handler = http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		if panicNext.CompareAndSwap(true, false) {
			panic(panicSecret)
		}
		response.WriteHeader(http.StatusNoContent)
	})
	server, err := New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	request := newTestRequest(t, http.MethodPost, privatePath, strings.NewReader(bodyValue))
	request.Header.Set("Authorization", authorizationValue)
	request.Header.Set("Cookie", cookieValue)
	response := newTestRecorder()
	server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("panic status = %d, want 500", response.Code)
	}
	if response.Body.String() != "internal server error\n" {
		t.Fatalf("panic response = %q, want generic message", response.Body.String())
	}

	second := newTestRecorder()
	server.Handler.ServeHTTP(second, newTestRequest(t, http.MethodGet, "/", nil))
	if second.Code != http.StatusNoContent {
		t.Fatalf("second status = %d, want 204", second.Code)
	}

	for _, sensitive := range []string{
		panicSecret,
		authorizationValue,
		cookieValue,
		bodyValue,
		privatePath,
	} {
		if strings.Contains(logs.String(), sensitive) {
			t.Fatalf("logs contain sensitive value %q: %s", sensitive, logs.String())
		}
	}
}

func TestLoggingUsesOnlyLowCardinalityRequestFields(t *testing.T) {
	t.Parallel()

	const (
		privateID     = "customer-private-id"
		unknownMethod = "METHOD-private-secret"
	)
	var logs bytes.Buffer
	mux := http.NewServeMux()
	mux.HandleFunc("GET /users/{id}", func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusAccepted)
	})
	config := validTestConfig(t, &logs)
	config.Handler = mux
	server, err := New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	server.Handler.ServeHTTP(
		newTestRecorder(),
		newTestRequest(t, http.MethodGet, "/users/"+privateID, nil),
	)
	server.Handler.ServeHTTP(
		newTestRecorder(),
		newTestRequest(t, unknownMethod, "/users/"+privateID, nil),
	)

	output := logs.String()
	for _, expected := range []string{
		`"http_method":"GET"`,
		`"http_method":"_OTHER"`,
		`"http_route":"/users/{id}"`,
		`"http_status":202`,
	} {
		if !strings.Contains(output, expected) {
			t.Fatalf("logs %q do not contain %q", output, expected)
		}
	}
	if strings.Contains(output, privateID) || strings.Contains(output, unknownMethod) {
		t.Fatalf("logs contain high-cardinality request data: %s", output)
	}
}

func TestOpenTelemetryUsesSafeServerAttributes(t *testing.T) {
	t.Parallel()

	const (
		privateID          = "customer-private-id"
		authorizationValue = "Bearer trace-secret"
		cookieValue        = "session=trace-secret"
	)
	spanExporter := tracetest.NewInMemoryExporter()
	metricReader := sdkmetric.NewManualReader()
	pipeline, err := telemetry.New(
		context.Background(),
		telemetry.Config{
			ServiceName:    "httpserver-test",
			ServiceVersion: "test",
			Environment:    "test",
			InstanceID:     "httpserver-test-1",
		},
		telemetry.WithSpanExporter(spanExporter),
		telemetry.WithMetricReader(metricReader),
	)
	if err != nil {
		t.Fatalf("telemetry.New() error = %v", err)
	}
	defer func() {
		if err := pipeline.Shutdown(context.Background()); err != nil {
			t.Errorf("Telemetry.Shutdown() error = %v", err)
		}
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("GET /users/{id}", func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusAccepted)
	})
	config := validTestConfig(t, &bytes.Buffer{})
	config.Handler = mux
	config.Telemetry = pipeline
	server, err := New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	remote := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
		SpanID:     trace.SpanID{1, 2, 3, 4, 5, 6, 7, 8},
		TraceFlags: trace.FlagsSampled,
		Remote:     true,
	})
	request := newTestRequest(t, http.MethodGet, "/users/"+privateID, nil)
	request.Header.Set("Authorization", authorizationValue)
	request.Header.Set("Cookie", cookieValue)
	pipeline.Propagator().Inject(
		trace.ContextWithRemoteSpanContext(context.Background(), remote),
		propagation.HeaderCarrier(request.Header),
	)
	server.Handler.ServeHTTP(newTestRecorder(), request)
	if err := pipeline.ForceFlush(context.Background()); err != nil {
		t.Fatalf("Telemetry.ForceFlush() error = %v", err)
	}

	spans := spanExporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("exported spans = %d, want 1", len(spans))
	}
	span := spans[0]
	if !span.Parent.Equal(remote) {
		t.Fatalf("span parent = %v, want %v", span.Parent, remote)
	}
	if span.Name != "GET /users/{id}" || span.SpanKind != trace.SpanKindServer {
		t.Fatalf("span = %q kind %s, want server route span", span.Name, span.SpanKind)
	}
	if span.Status.Code != codes.Unset {
		t.Fatalf("span status = %s, want unset for HTTP 202", span.Status.Code)
	}
	assertSafeAttributes(t, span.Attributes, http.MethodGet, "/users/{id}", http.StatusAccepted)

	var resourceMetrics metricdata.ResourceMetrics
	if err := metricReader.Collect(context.Background(), &resourceMetrics); err != nil {
		t.Fatalf("metricReader.Collect() error = %v", err)
	}
	metricAttributes := requestDurationAttributes(t, resourceMetrics)
	assertSafeAttributes(t, metricAttributes, http.MethodGet, "/users/{id}", http.StatusAccepted)

	diagnostic := fmt.Sprintf("%+v %+v", span, resourceMetrics)
	for _, sensitive := range []string{privateID, authorizationValue, cookieValue} {
		if strings.Contains(diagnostic, sensitive) {
			t.Fatalf("telemetry contains sensitive value %q", sensitive)
		}
	}
}

func TestOpenTelemetryDoesNotRecordPanicValue(t *testing.T) {
	t.Parallel()

	const panicSecret = "panic-trace-secret-must-not-leak"
	spanExporter := tracetest.NewInMemoryExporter()
	metricReader := sdkmetric.NewManualReader()
	pipeline, err := telemetry.New(
		context.Background(),
		telemetry.Config{
			ServiceName:    "httpserver-test",
			ServiceVersion: "test",
			Environment:    "test",
			InstanceID:     "httpserver-test-2",
		},
		telemetry.WithSpanExporter(spanExporter),
		telemetry.WithMetricReader(metricReader),
	)
	if err != nil {
		t.Fatalf("telemetry.New() error = %v", err)
	}
	defer func() {
		if err := pipeline.Shutdown(context.Background()); err != nil {
			t.Errorf("Telemetry.Shutdown() error = %v", err)
		}
	}()

	config := validTestConfig(t, &bytes.Buffer{})
	config.Telemetry = pipeline
	config.Handler = http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(panicSecret)
	})
	server, err := New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	response := newTestRecorder()
	server.Handler.ServeHTTP(response, newTestRequest(t, http.MethodGet, "/panic", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("panic status = %d, want 500", response.Code)
	}
	if err := pipeline.ForceFlush(context.Background()); err != nil {
		t.Fatalf("Telemetry.ForceFlush() error = %v", err)
	}

	spans := spanExporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("exported spans = %d, want 1", len(spans))
	}
	if spans[0].Status.Code != codes.Error || spans[0].Status.Description != "internal server error" {
		t.Fatalf("panic span status = %+v, want safe error", spans[0].Status)
	}
	if strings.Contains(fmt.Sprintf("%+v", spans[0]), panicSecret) {
		t.Fatal("panic span contains recovered value")
	}
}

func assertSafeAttributes(
	t *testing.T,
	attributes []attribute.KeyValue,
	method string,
	route string,
	status int,
) {
	t.Helper()

	values := map[attribute.Key]attribute.Value{}
	for _, keyValue := range attributes {
		values[keyValue.Key] = keyValue.Value
	}
	if value := values[semconv.HTTPRequestMethodKey]; value.AsString() != method {
		t.Errorf("HTTP method attribute = %q, want %q", value.AsString(), method)
	}
	if value := values[semconv.HTTPRouteKey]; value.AsString() != route {
		t.Errorf("HTTP route attribute = %q, want %q", value.AsString(), route)
	}
	if value := values[semconv.HTTPResponseStatusCodeKey]; int(value.AsInt64()) != status {
		t.Errorf("HTTP status attribute = %d, want %d", value.AsInt64(), status)
	}
	if len(values) != 3 {
		t.Errorf("HTTP attributes = %v, want only method, route and status", values)
	}
}

func requestDurationAttributes(
	t *testing.T,
	resourceMetrics metricdata.ResourceMetrics,
) []attribute.KeyValue {
	t.Helper()

	for _, scope := range resourceMetrics.ScopeMetrics {
		for _, measured := range scope.Metrics {
			if measured.Name != "http.server.request.duration" {
				continue
			}
			histogram, ok := measured.Data.(metricdata.Histogram[float64])
			if !ok {
				t.Fatalf("request duration data type = %T, want float64 histogram", measured.Data)
			}
			if len(histogram.DataPoints) != 1 || histogram.DataPoints[0].Count != 1 {
				t.Fatalf("request duration points = %+v, want one observation", histogram.DataPoints)
			}

			return histogram.DataPoints[0].Attributes.ToSlice()
		}
	}

	t.Fatal("http.server.request.duration metric was not exported")
	return nil
}

type deadlineKey struct{}
