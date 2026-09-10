package grpc

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/v0hmly/marketmesh/platform/logger"
	"github.com/v0hmly/marketmesh/platform/telemetry"
	grpcgo "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/interop/grpc_testing"
	"google.golang.org/grpc/resolver"
	"google.golang.org/grpc/resolver/manual"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

var retryResolverID atomic.Uint64

type unavailableMutation struct {
	grpc_testing.UnimplementedTestServiceServer
	calls atomic.Int32
}

func (s *unavailableMutation) EmptyCall(context.Context, *grpc_testing.Empty) (*grpc_testing.Empty, error) {
	s.calls.Add(1)
	return nil, status.Error(codes.Unavailable, "mutation processed but result unavailable")
}

func retryBoundaryConfig(t *testing.T) ClientConfig {
	t.Helper()
	log, err := logger.New(logger.Config{Service: "grpc-test", Version: "test", Environment: "test", Output: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	return ClientConfig{Target: "passthrough:///bufconn", Environment: "test", ConnectTimeout: time.Second, CallTimeout: time.Second, KeepaliveTime: time.Second, KeepaliveTimeout: time.Second, MaxReceiveMessageBytes: 1024, MaxSendMessageBytes: 1024, Security: ClientSecurity{Plaintext: PlaintextLocal}, Logger: log, Telemetry: telemetry.NewNoop()}
}

func TestDisableRetriesRejectsExplicitPolicy(t *testing.T) {
	cfg := retryBoundaryConfig(t)
	cfg.DisableRetries = true
	cfg.Retry = &RetryPolicy{}
	if _, err := NewClient(t.Context(), cfg); err == nil {
		t.Fatal("accepted retry policy when retries disabled")
	}
}

func TestDisableRetriesIgnoresResolverMutationRetryPolicy(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		t.Run(fmt.Sprint(disabled), func(t *testing.T) {
			listener := bufconn.Listen(1024 * 1024)
			defer listener.Close()
			service := new(unavailableMutation)
			server := grpcgo.NewServer()
			grpc_testing.RegisterTestServiceServer(server, service)
			defer server.Stop()
			go func() { _ = server.Serve(listener) }()

			builder := manual.NewBuilderWithScheme(fmt.Sprintf("mm-retry-%d", retryResolverID.Add(1)))
			builder.BuildCallback = func(_ resolver.Target, cc resolver.ClientConn, _ resolver.BuildOptions) {
				parsed := cc.ParseServiceConfig(`{"methodConfig":[{"name":[{"service":"grpc.testing.TestService","method":"EmptyCall"}],"retryPolicy":{"maxAttempts":3,"initialBackoff":"0.001s","maxBackoff":"0.001s","backoffMultiplier":1,"retryableStatusCodes":["UNAVAILABLE"]}}]}`)
				if parsed.Err != nil {
					t.Error(parsed.Err)
					return
				}
				if err := cc.UpdateState(resolver.State{Addresses: []resolver.Address{{Addr: "bufconn"}}, ServiceConfig: parsed}); err != nil {
					t.Error(err)
				}
			}
			resolver.Register(builder)
			cfg := retryBoundaryConfig(t)
			cfg.Target = builder.Scheme() + ":///bufconn"
			cfg.DisableRetries = disabled
			cfg.Dialer = func(ctx context.Context, _ string) (net.Conn, error) { return listener.DialContext(ctx) }
			client, err := NewClient(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			_, err = grpc_testing.NewTestServiceClient(client.Connection()).EmptyCall(t.Context(), &grpc_testing.Empty{})
			if status.Code(err) != codes.Unavailable {
				t.Fatal("unexpected result", err)
			}
			want := int32(3)
			if disabled {
				want = 1
			}
			if got := service.calls.Load(); got != want {
				t.Fatalf("processed calls = %d, want %d", got, want)
			}
		})
	}
}
