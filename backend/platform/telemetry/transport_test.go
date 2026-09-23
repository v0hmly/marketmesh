package telemetry

import (
	"context"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestTransportAdapters(t *testing.T) {
	t.Parallel()

	pipeline := NewNoop()
	if pipeline.GRPCServerStatsHandler() == nil {
		t.Fatal("GRPCServerStatsHandler() returned nil")
	}
	if pipeline.GRPCClientStatsHandler() == nil {
		t.Fatal("GRPCClientStatsHandler() returned nil")
	}
	if interceptor, err := pipeline.PublicConnectInterceptor(); err != nil || interceptor == nil {
		t.Fatalf("PublicConnectInterceptor() = %v, %v", interceptor, err)
	}
	if interceptor, err := pipeline.TrustedConnectInterceptor(); err != nil || interceptor == nil {
		t.Fatalf("TrustedConnectInterceptor() = %v, %v", interceptor, err)
	}
}

func TestConnectBoundaryTrust(t *testing.T) {
	t.Parallel()

	testCases := map[string]struct {
		trusted bool
	}{
		"public creates root and link": {trusted: false},
		"internal preserves parent":    {trusted: true},
	}

	for name, testCase := range testCases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			testConnectBoundaryTrust(t, testCase.trusted)
		})
	}
}

func testConnectBoundaryTrust(t *testing.T, trusted bool) {
	t.Helper()

	spanExporter := tracetest.NewInMemoryExporter()
	pipeline, err := New(
		context.Background(),
		validConfigWithoutEndpoint(),
		WithSpanExporter(spanExporter),
		WithMetricReader(sdkmetric.NewManualReader()),
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	var interceptor connect.Interceptor
	if trusted {
		interceptor, err = pipeline.TrustedConnectInterceptor()
	} else {
		interceptor, err = pipeline.PublicConnectInterceptor()
	}
	if err != nil {
		t.Fatalf("create interceptor: %v", err)
	}

	const procedure = "/marketmesh.test.v1.TestService/Ping"
	handler := connect.NewUnaryHandler(
		procedure,
		func(_ context.Context, _ *connect.Request[emptypb.Empty]) (*connect.Response[emptypb.Empty], error) {
			return connect.NewResponse(&emptypb.Empty{}), nil
		},
		connect.WithInterceptors(interceptor),
	)
	server := httptest.NewServer(handler)
	defer server.Close()

	client := connect.NewClient[emptypb.Empty, emptypb.Empty](server.Client(), server.URL+procedure)
	request := connect.NewRequest(&emptypb.Empty{})
	remote := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    trace.TraceID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16},
		SpanID:     trace.SpanID{1, 2, 3, 4, 5, 6, 7, 8},
		TraceFlags: trace.FlagsSampled,
		Remote:     true,
	})
	remoteCtx := trace.ContextWithRemoteSpanContext(context.Background(), remote)
	pipeline.Propagator().Inject(remoteCtx, propagation.HeaderCarrier(request.Header()))

	if _, err := client.CallUnary(context.Background(), request); err != nil {
		t.Fatalf("CallUnary() error = %v", err)
	}
	if err := pipeline.ForceFlush(context.Background()); err != nil {
		t.Fatalf("ForceFlush() error = %v", err)
	}

	spans := spanExporter.GetSpans()
	if len(spans) != 1 {
		t.Fatalf("exported spans = %d, want 1", len(spans))
	}
	serverSpan := spans[0]
	if trusted {
		if !serverSpan.Parent.Equal(remote) {
			t.Errorf("trusted parent = %v, want %v", serverSpan.Parent, remote)
		}
		if serverSpan.SpanContext.TraceID() != remote.TraceID() {
			t.Errorf("trusted trace ID = %v, want %v", serverSpan.SpanContext.TraceID(), remote.TraceID())
		}
	} else {
		if serverSpan.Parent.IsValid() {
			t.Errorf("public parent = %v, want invalid", serverSpan.Parent)
		}
		if serverSpan.SpanContext.TraceID() == remote.TraceID() {
			t.Errorf("public trace ID = %v, must differ from remote", serverSpan.SpanContext.TraceID())
		}
		if len(serverSpan.Links) != 1 || !serverSpan.Links[0].SpanContext.Equal(remote) {
			t.Errorf("public links = %v, want link to %v", serverSpan.Links, remote)
		}
	}

	if err := pipeline.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
}
