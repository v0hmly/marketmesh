//go:build integration

package grpc

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	platformlogger "github.com/v0hmly/marketmesh/platform/logger"
	"github.com/v0hmly/marketmesh/platform/telemetry"
	"github.com/v0hmly/marketmesh/platform/testkit"
	grpcgo "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/interop/grpc_testing"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

const (
	testAuthorization = "Bearer integration-secret-token"
	testSensitiveBody = "request-body-must-not-be-logged"
)

var errDomainMissing = errors.New("database row missing: private-dsn-password")

func TestClientServerBufconnIntegration(t *testing.T) {
	t.Parallel()

	service := newIntegrationService()
	harness := newIntegrationHarness(t, service, 250*time.Millisecond)
	client := grpc_testing.NewTestServiceClient(harness.client.Connection())

	t.Run("connection reuse and metadata", func(t *testing.T) {
		firstConnection := harness.client.Connection()
		for range 2 {
			response, err := client.EmptyCall(withTestMode(t.Context(), "success"), &grpc_testing.Empty{})
			if err != nil || response == nil {
				t.Fatalf("EmptyCall() = %v, %v", response, err)
			}
			if harness.client.Connection() != firstConnection {
				t.Fatal("client replaced its shared connection")
			}
		}
	})

	t.Run("default unary deadline", func(t *testing.T) {
		_, err := client.EmptyCall(withTestMode(t.Context(), "observe-deadline"), &grpc_testing.Empty{})
		if err != nil {
			t.Fatalf("EmptyCall() error = %v", err)
		}

		select {
		case remaining := <-service.unaryDeadline:
			if remaining <= 0 || remaining > 250*time.Millisecond {
				t.Fatalf("unary deadline = %v, want (0, 250ms]", remaining)
			}
		case <-time.After(time.Second):
			t.Fatal("server did not observe unary deadline")
		}
	})

	t.Run("caller cancellation", func(t *testing.T) {
		ctx, cancel := context.WithCancel(withTestMode(t.Context(), "block-unary"))
		result := make(chan error, 1)
		go func() {
			_, err := client.EmptyCall(ctx, &grpc_testing.Empty{})
			result <- err
		}()

		select {
		case <-service.unaryStarted:
		case <-time.After(time.Second):
			t.Fatal("unary handler did not start")
		}
		cancel()

		select {
		case err := <-result:
			if status.Code(err) != codes.Canceled {
				t.Fatalf("EmptyCall() code = %s, want Canceled", status.Code(err))
			}
		case <-time.After(time.Second):
			t.Fatal("canceled unary call did not finish")
		}
	})

	t.Run("status mapping hides details", func(t *testing.T) {
		_, err := client.EmptyCall(withTestMode(t.Context(), "domain-error"), &grpc_testing.Empty{})
		grpcStatus := status.Convert(err)
		if grpcStatus.Code() != codes.NotFound || grpcStatus.Message() != "not found" {
			t.Fatalf("EmptyCall() status = %s %q", grpcStatus.Code(), grpcStatus.Message())
		}
		if strings.Contains(err.Error(), "private-dsn-password") {
			t.Fatalf("EmptyCall() exposed internal error: %v", err)
		}
	})

	t.Run("panic recovery keeps server alive", func(t *testing.T) {
		_, err := client.EmptyCall(withTestMode(t.Context(), "panic"), &grpc_testing.Empty{})
		grpcStatus := status.Convert(err)
		if grpcStatus.Code() != codes.Internal || grpcStatus.Message() != "internal error" {
			t.Fatalf("panic status = %s %q", grpcStatus.Code(), grpcStatus.Message())
		}
		if _, err := client.EmptyCall(withTestMode(t.Context(), "success"), &grpc_testing.Empty{}); err != nil {
			t.Fatalf("server did not survive panic: %v", err)
		}
	})

	t.Run("retry only idempotent method", func(t *testing.T) {
		_, err := client.EmptyCall(withTestMode(t.Context(), "retry"), &grpc_testing.Empty{})
		if err != nil {
			t.Fatalf("idempotent EmptyCall() error = %v", err)
		}
		if service.retryAttempts.Load() != 2 {
			t.Fatalf("EmptyCall() attempts = %d, want 2", service.retryAttempts.Load())
		}

		_, err = client.UnaryCall(
			withTestMode(t.Context(), "unavailable"),
			&grpc_testing.SimpleRequest{
				Payload: &grpc_testing.Payload{Body: []byte(testSensitiveBody)},
			},
		)
		if status.Code(err) != codes.Unavailable {
			t.Fatalf("UnaryCall() code = %s, want Unavailable", status.Code(err))
		}
		if service.nonIdempotentAttempts.Load() != 1 {
			t.Fatalf("UnaryCall() attempts = %d, want 1", service.nonIdempotentAttempts.Load())
		}
	})

	t.Run("stream metadata deadline and cancellation", func(t *testing.T) {
		stream, err := client.FullDuplexCall(withTestMode(t.Context(), "stream"))
		if err != nil {
			t.Fatalf("FullDuplexCall() error = %v", err)
		}
		if err := stream.Send(&grpc_testing.StreamingOutputCallRequest{}); err != nil {
			t.Fatalf("stream.Send() error = %v", err)
		}
		if _, err := stream.Recv(); err != nil {
			t.Fatalf("stream.Recv() error = %v", err)
		}
		headers, err := stream.Header()
		if err != nil {
			t.Fatalf("stream.Header() error = %v", err)
		}
		if values := headers.Get("x-server-metadata"); len(values) != 1 || values[0] != "present" {
			t.Fatalf("stream headers = %v, want server metadata", headers)
		}
		if _, err := stream.Recv(); err != io.EOF {
			t.Fatalf("final stream.Recv() error = %v, want EOF", err)
		}

		select {
		case remaining := <-service.streamDeadline:
			if remaining <= 0 || remaining > 250*time.Millisecond {
				t.Fatalf("stream deadline = %v, want (0, 250ms]", remaining)
			}
		case <-time.After(time.Second):
			t.Fatal("server did not observe stream deadline")
		}

		ctx, cancel := context.WithCancel(withTestMode(t.Context(), "block-stream"))
		blocking, err := client.FullDuplexCall(ctx)
		if err != nil {
			cancel()
			t.Fatalf("blocking FullDuplexCall() error = %v", err)
		}
		if err := blocking.Send(&grpc_testing.StreamingOutputCallRequest{}); err != nil {
			cancel()
			t.Fatalf("blocking stream.Send() error = %v", err)
		}
		select {
		case <-service.streamStarted:
		case <-time.After(time.Second):
			cancel()
			t.Fatal("stream handler did not start")
		}
		cancel()
		if _, err := blocking.Recv(); status.Code(err) != codes.Canceled {
			t.Fatalf("blocking stream.Recv() code = %s, want Canceled", status.Code(err))
		}
	})

	t.Run("standard health services are separate", func(t *testing.T) {
		healthClient := grpc_health_v1.NewHealthClient(harness.client.Connection())
		for _, serviceName := range []string{"", LivenessService, ReadinessService} {
			response, err := healthClient.Check(t.Context(), &grpc_health_v1.HealthCheckRequest{
				Service: serviceName,
			})
			if err != nil {
				t.Fatalf("health.Check(%s) error = %v", serviceName, err)
			}
			if response.Status != grpc_health_v1.HealthCheckResponse_SERVING {
				t.Fatalf("health.Check(%s) = %s, want SERVING", serviceName, response.Status)
			}
		}
	})

	t.Run("client closes shared connection once", func(t *testing.T) {
		if err := harness.client.Close(); err != nil {
			t.Fatalf("first client.Close() error = %v", err)
		}
		if err := harness.client.Close(); err != nil {
			t.Fatalf("second client.Close() error = %v", err)
		}
		if state := harness.client.Connection().GetState(); state != connectivity.Shutdown {
			t.Fatalf("connection state = %s, want Shutdown", state)
		}
	})

	logs := harness.logs.String()
	for _, sensitive := range []string{
		testAuthorization,
		testSensitiveBody,
		"private-dsn-password",
		"panic-value-must-not-be-logged",
	} {
		if strings.Contains(logs, sensitive) {
			t.Fatalf("gRPC interceptor logs contain sensitive value %q: %s", sensitive, logs)
		}
	}
}

func TestServerShutdownIsBoundedAndUpdatesHealth(t *testing.T) {
	t.Parallel()

	service := newIntegrationService()
	harness := newIntegrationHarness(t, service, 5*time.Second)
	client := grpc_testing.NewTestServiceClient(harness.client.Connection())

	callResult := make(chan error, 1)
	go func() {
		_, err := client.EmptyCall(withTestMode(t.Context(), "shutdown-block"), &grpc_testing.Empty{})
		callResult <- err
	}()
	select {
	case <-service.shutdownStarted:
	case <-time.After(time.Second):
		t.Fatal("blocking RPC did not start")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	shutdownErr := harness.shutdown(shutdownCtx)
	if !errors.Is(shutdownErr, context.DeadlineExceeded) {
		t.Fatalf("shutdown error = %v, want DeadlineExceeded", shutdownErr)
	}

	select {
	case <-service.shutdownCanceled:
	case <-time.After(time.Second):
		t.Fatal("forced stop did not cancel active RPC")
	}
	select {
	case <-callResult:
	case <-time.After(time.Second):
		t.Fatal("client RPC did not finish after forced stop")
	}

	for _, serviceName := range []string{"", LivenessService, ReadinessService} {
		response, err := harness.server.health.Check(
			context.Background(),
			&grpc_health_v1.HealthCheckRequest{Service: serviceName},
		)
		if err != nil {
			t.Fatalf("health.Check(%s) error = %v", serviceName, err)
		}
		if response.Status != grpc_health_v1.HealthCheckResponse_NOT_SERVING {
			t.Fatalf("health.Check(%s) = %s, want NOT_SERVING", serviceName, response.Status)
		}
	}
}

func TestServerRejectsProductionReflection(t *testing.T) {
	t.Parallel()

	log, _ := testkit.NewLogger(t)
	config := validIntegrationServerConfig(log, testkit.NoopTelemetry(t))
	config.Environment = "production"
	config.Security.Plaintext = PlaintextTrustedMesh
	config.EnableReflection = true

	_, err := NewServer(config)
	if err == nil || !strings.Contains(err.Error(), "reflection is forbidden") {
		t.Fatalf("NewServer() error = %v, want reflection policy error", err)
	}
}

func TestClientConnectionTimeout(t *testing.T) {
	t.Parallel()

	log, _ := testkit.NewLogger(t)
	config := validIntegrationClientConfig(log, testkit.NoopTelemetry(t), 50*time.Millisecond)
	config.Target = "passthrough:///blocked"
	config.ConnectTimeout = 20 * time.Millisecond
	config.Dialer = func(ctx context.Context, _ string) (net.Conn, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}

	started := time.Now()
	_, err := NewClient(t.Context(), config)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("NewClient() error = %v, want DeadlineExceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("NewClient() elapsed = %v, want bounded connection attempt", elapsed)
	}
}

type integrationHarness struct {
	server    *Server
	client    *Client
	logs      *testkit.LogCapture
	component interface {
		Shutdown(context.Context) error
	}
	shutdownOnce sync.Once
	shutdownErr  error
	runDone      chan error
}

func newIntegrationHarness(
	t *testing.T,
	service *integrationService,
	callTimeout time.Duration,
) *integrationHarness {
	t.Helper()

	log, logs := testkit.NewLogger(t)
	pipeline := testkit.NoopTelemetry(t)
	server, err := NewServer(validIntegrationServerConfig(log, pipeline))
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}
	grpc_testing.RegisterTestServiceServer(server.GRPCServer(), service)

	listener := bufconn.Listen(1024 * 1024)
	component, err := server.Component("grpc", listener)
	if err != nil {
		t.Fatalf("server.Component() error = %v", err)
	}
	runDone := make(chan error, 1)
	go func() {
		runDone <- component.Run(t.Context())
	}()

	clientConfig := validIntegrationClientConfig(log, pipeline, callTimeout)
	clientConfig.Dialer = func(ctx context.Context, _ string) (net.Conn, error) {
		return listener.DialContext(ctx)
	}
	client, err := NewClient(t.Context(), clientConfig)
	if err != nil {
		server.GRPCServer().Stop()
		t.Fatalf("NewClient() error = %v", err)
	}

	harness := &integrationHarness{
		server:    server,
		client:    client,
		logs:      logs,
		component: componentAdapter{shutdown: component.Shutdown},
		runDone:   runDone,
	}
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("client.Close() error = %v", err)
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = harness.shutdown(shutdownCtx)
		select {
		case <-runDone:
		case <-time.After(time.Second):
			t.Error("gRPC server did not stop")
		}
	})

	return harness
}

func (harness *integrationHarness) shutdown(ctx context.Context) error {
	harness.shutdownOnce.Do(func() {
		harness.shutdownErr = harness.component.Shutdown(ctx)
	})

	return harness.shutdownErr
}

type componentAdapter struct {
	shutdown func(context.Context) error
}

func (adapter componentAdapter) Shutdown(ctx context.Context) error {
	return adapter.shutdown(ctx)
}

func validIntegrationServerConfig(
	log *platformlogger.Logger,
	pipeline *telemetry.Telemetry,
) ServerConfig {
	return ServerConfig{
		Environment:            "test",
		ConnectionTimeout:      time.Second,
		RequestTimeout:         10 * time.Second,
		KeepaliveTime:          time.Second,
		KeepaliveTimeout:       time.Second,
		MaxReceiveMessageBytes: 1024 * 1024,
		MaxSendMessageBytes:    1024 * 1024,
		Security: ServerSecurity{
			Plaintext: PlaintextLocal,
		},
		Logger:               log,
		Telemetry:            pipeline,
		ErrorCodeMapper:      integrationErrorMapper,
		UnaryAuthentication:  unaryServerAuthentication,
		StreamAuthentication: streamServerAuthentication,
	}
}

func validIntegrationClientConfig(
	log *platformlogger.Logger,
	pipeline *telemetry.Telemetry,
	callTimeout time.Duration,
) ClientConfig {
	return ClientConfig{
		Target:                 "passthrough:///bufconn",
		Environment:            "test",
		ConnectTimeout:         time.Second,
		CallTimeout:            callTimeout,
		KeepaliveTime:          time.Second,
		KeepaliveTimeout:       time.Second,
		MaxReceiveMessageBytes: 1024 * 1024,
		MaxSendMessageBytes:    1024 * 1024,
		Security: ClientSecurity{
			Plaintext: PlaintextLocal,
		},
		Logger:    log,
		Telemetry: pipeline,
		Retry: &RetryPolicy{
			IdempotentMethods: []string{grpc_testing.TestService_EmptyCall_FullMethodName},
			RetryableCodes:    []codes.Code{codes.Unavailable},
			MaxAttempts:       3,
			InitialBackoff:    time.Millisecond,
			MaxBackoff:        2 * time.Millisecond,
			BackoffMultiplier: 2,
		},
		UnaryAuthentication:  unaryClientAuthentication,
		StreamAuthentication: streamClientAuthentication,
	}
}

func unaryClientAuthentication(
	ctx context.Context,
	method string,
	request any,
	response any,
	connection *grpcgo.ClientConn,
	invoker grpcgo.UnaryInvoker,
	options ...grpcgo.CallOption,
) error {
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", testAuthorization)
	return invoker(ctx, method, request, response, connection, options...)
}

func streamClientAuthentication(
	ctx context.Context,
	description *grpcgo.StreamDesc,
	connection *grpcgo.ClientConn,
	method string,
	streamer grpcgo.Streamer,
	options ...grpcgo.CallOption,
) (grpcgo.ClientStream, error) {
	ctx = metadata.AppendToOutgoingContext(ctx, "authorization", testAuthorization)
	return streamer(ctx, description, connection, method, options...)
}

func unaryServerAuthentication(
	ctx context.Context,
	request any,
	info *grpcgo.UnaryServerInfo,
	handler grpcgo.UnaryHandler,
) (any, error) {
	if !hasTestAuthorization(ctx) {
		return nil, status.Error(codes.Unauthenticated, "sensitive auth detail")
	}

	return handler(ctx, request)
}

func streamServerAuthentication(
	service any,
	stream grpcgo.ServerStream,
	info *grpcgo.StreamServerInfo,
	handler grpcgo.StreamHandler,
) error {
	if !hasTestAuthorization(stream.Context()) {
		return status.Error(codes.Unauthenticated, "sensitive auth detail")
	}

	return handler(service, stream)
}

func hasTestAuthorization(ctx context.Context) bool {
	incoming, found := metadata.FromIncomingContext(ctx)
	if !found {
		return false
	}
	values := incoming.Get("authorization")
	return len(values) == 1 && values[0] == testAuthorization
}

func integrationErrorMapper(err error) (codes.Code, bool) {
	if errors.Is(err, errDomainMissing) {
		return codes.NotFound, true
	}

	return codes.OK, false
}

func withTestMode(ctx context.Context, mode string) context.Context {
	return metadata.AppendToOutgoingContext(ctx, "x-test-mode", mode)
}

func incomingTestMode(ctx context.Context) string {
	incoming, _ := metadata.FromIncomingContext(ctx)
	values := incoming.Get("x-test-mode")
	if len(values) == 0 {
		return ""
	}

	return values[0]
}

type integrationService struct {
	grpc_testing.UnimplementedTestServiceServer
	unaryDeadline         chan time.Duration
	streamDeadline        chan time.Duration
	unaryStarted          chan struct{}
	streamStarted         chan struct{}
	shutdownStarted       chan struct{}
	shutdownCanceled      chan struct{}
	unaryStartOnce        sync.Once
	streamStartOnce       sync.Once
	shutdownStartOnce     sync.Once
	shutdownCancelOnce    sync.Once
	retryAttempts         atomic.Int32
	nonIdempotentAttempts atomic.Int32
}

func newIntegrationService() *integrationService {
	return &integrationService{
		unaryDeadline:    make(chan time.Duration, 1),
		streamDeadline:   make(chan time.Duration, 1),
		unaryStarted:     make(chan struct{}),
		streamStarted:    make(chan struct{}),
		shutdownStarted:  make(chan struct{}),
		shutdownCanceled: make(chan struct{}),
	}
}

func (service *integrationService) EmptyCall(
	ctx context.Context,
	_ *grpc_testing.Empty,
) (*grpc_testing.Empty, error) {
	switch incomingTestMode(ctx) {
	case "observe-deadline":
		deadline, found := ctx.Deadline()
		if !found {
			return nil, errors.New("server did not receive deadline")
		}
		service.unaryDeadline <- time.Until(deadline)
	case "block-unary":
		service.unaryStartOnce.Do(func() { close(service.unaryStarted) })
		<-ctx.Done()
		return nil, ctx.Err()
	case "shutdown-block":
		service.shutdownStartOnce.Do(func() { close(service.shutdownStarted) })
		<-ctx.Done()
		service.shutdownCancelOnce.Do(func() { close(service.shutdownCanceled) })
		return nil, ctx.Err()
	case "domain-error":
		return nil, errDomainMissing
	case "panic":
		panic("panic-value-must-not-be-logged")
	case "retry":
		if service.retryAttempts.Add(1) == 1 {
			return nil, status.Error(codes.Unavailable, "backend address and secret")
		}
	}

	return &grpc_testing.Empty{}, nil
}

func (service *integrationService) UnaryCall(
	ctx context.Context,
	_ *grpc_testing.SimpleRequest,
) (*grpc_testing.SimpleResponse, error) {
	if incomingTestMode(ctx) == "unavailable" {
		service.nonIdempotentAttempts.Add(1)
		return nil, status.Error(codes.Unavailable, "temporary internal endpoint")
	}

	return &grpc_testing.SimpleResponse{}, nil
}

func (service *integrationService) FullDuplexCall(
	stream grpcgo.BidiStreamingServer[
		grpc_testing.StreamingOutputCallRequest,
		grpc_testing.StreamingOutputCallResponse,
	],
) error {
	deadline, found := stream.Context().Deadline()
	if found {
		select {
		case service.streamDeadline <- time.Until(deadline):
		default:
		}
	}
	if err := stream.SetHeader(metadata.Pairs("x-server-metadata", "present")); err != nil {
		return err
	}
	if _, err := stream.Recv(); err != nil {
		return err
	}
	if incomingTestMode(stream.Context()) == "block-stream" {
		service.streamStartOnce.Do(func() { close(service.streamStarted) })
		<-stream.Context().Done()
		return stream.Context().Err()
	}

	return stream.Send(&grpc_testing.StreamingOutputCallResponse{})
}
