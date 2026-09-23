package tunnel

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"strings"
	"testing"
	"time"

	contractv1 "github.com/v0hmly/marketmesh/api/gen/go/tunnel/v1"
	grpcgo "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/emptypb"
)

func TestSecureClientTLSConfig(t *testing.T) {
	t.Parallel()

	pki := newTestPKI(t)
	tests := []struct {
		name      string
		config    *tls.Config
		identity  string
		wantError string
	}{
		{
			name:     "valid mTLS and identity",
			config:   pki.clientTLS,
			identity: testServerIdentity,
		},
		{
			name:      "missing TLS",
			identity:  testServerIdentity,
			wantError: "mTLS config is required",
		},
		{
			name: "skip verification",
			config: &tls.Config{
				InsecureSkipVerify: true, //nolint:gosec // проверяется fail-closed validation.
			},
			identity:  testServerIdentity,
			wantError: "InsecureSkipVerify is forbidden",
		},
		{
			name: "missing server name",
			config: &tls.Config{
				RootCAs:      x509.NewCertPool(),
				Certificates: pki.clientTLS.Certificates,
			},
			identity:  testServerIdentity,
			wantError: "server name is required",
		},
		{
			name: "missing roots",
			config: &tls.Config{
				ServerName:   testServerName,
				Certificates: pki.clientTLS.Certificates,
			},
			identity:  testServerIdentity,
			wantError: "server CA pool is required",
		},
		{
			name: "missing client certificate",
			config: &tls.Config{
				ServerName: testServerName,
				RootCAs:    x509.NewCertPool(),
			},
			identity:  testServerIdentity,
			wantError: "client certificate is required",
		},
		{
			name:      "invalid workload identity",
			config:    pki.clientTLS,
			identity:  "gateway-in",
			wantError: "expected server identity is invalid",
		},
		{
			name: "old TLS",
			config: func() *tls.Config {
				config := pki.clientTLS.Clone()
				config.MinVersion = tls.VersionTLS11
				return config
			}(),
			identity:  testServerIdentity,
			wantError: "TLS version",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			secured, err := secureClientTLSConfig(test.config, test.identity)
			if test.wantError == "" {
				if err != nil {
					t.Fatalf("secureClientTLSConfig() error = %v", err)
				}
				if secured == test.config {
					t.Fatal("secureClientTLSConfig() returned caller config")
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("secureClientTLSConfig() error = %v, want %q", err, test.wantError)
			}
		})
	}
}

func TestBackoffIsExponentiallyBoundedAndJittered(t *testing.T) {
	t.Parallel()

	client := &Client{settings: settings{
		reconnect: ReconnectPolicy{
			InitialBackoff: 100 * time.Millisecond,
			MaxBackoff:     400 * time.Millisecond,
			Multiplier:     2,
			JitterRatio:    .25,
		},
		jitter: func(maximum time.Duration) time.Duration { return maximum },
	}}

	tests := []struct {
		name     string
		failures int
		want     time.Duration
	}{
		{name: "first", failures: 1, want: 125 * time.Millisecond},
		{name: "second", failures: 2, want: 250 * time.Millisecond},
		{name: "bounded", failures: 20, want: 400 * time.Millisecond},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := client.backoff(test.failures); got != test.want {
				t.Fatalf("backoff(%d) = %v, want %v", test.failures, got, test.want)
			}
		})
	}
}

func TestRegistryRejectsUnsafeRoutes(t *testing.T) {
	t.Parallel()

	connection := &fakeClientConnection{}
	clients := ClassClients{ControlAuth: connection, Regular: connection, Realtime: connection}
	valid := RouteSpec{
		ID:               contractv1.RouteId_ROUTE_ID_USER_GET_ME,
		TrafficClass:     contractv1.TrafficClass_TRAFFIC_CLASS_REGULAR,
		Method:           "/test.Service/Get",
		NewRequest:       func() proto.Message { return &emptypb.Empty{} },
		NewResponse:      func() proto.Message { return &emptypb.Empty{} },
		MaxRequestBytes:  1024,
		MaxResponseBytes: 1024,
		MaxDeadline:      time.Second,
	}

	tests := []struct {
		name   string
		mutate func(RouteSpec) RouteSpec
	}{
		{
			name: "unknown route",
			mutate: func(spec RouteSpec) RouteSpec {
				spec.ID = contractv1.RouteId(999)
				return spec
			},
		},
		{
			name: "class mismatch",
			mutate: func(spec RouteSpec) RouteSpec {
				spec.TrafficClass = contractv1.TrafficClass_TRAFFIC_CLASS_REALTIME
				return spec
			},
		},
		{
			name: "method without service",
			mutate: func(spec RouteSpec) RouteSpec {
				spec.Method = "/Get"
				return spec
			},
		},
		{
			name: "unbounded request",
			mutate: func(spec RouteSpec) RouteSpec {
				spec.MaxRequestBytes = 0
				return spec
			},
		},
		{
			name: "idempotency on read",
			mutate: func(spec RouteSpec) RouteSpec {
				spec.RequireIdempotencyKey = true
				return spec
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := NewRegistry(clients, test.mutate(valid)); err == nil {
				t.Fatal("NewRegistry() error = nil, want fail-closed validation")
			}
		})
	}

	if _, err := NewRegistry(clients, valid, valid); err == nil {
		t.Fatal("NewRegistry() duplicate error = nil")
	}
}

func TestInternalErrorsMapToFiniteResultCodes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want contractv1.ResultCode
	}{
		{
			name: "not found",
			err:  status.Error(codes.NotFound, "private record name"),
			want: contractv1.ResultCode_RESULT_CODE_NOT_FOUND,
		},
		{
			name: "conflict",
			err:  status.Error(codes.AlreadyExists, "private unique constraint"),
			want: contractv1.ResultCode_RESULT_CODE_CONFLICT,
		},
		{
			name: "unknown is internal",
			err:  status.Error(codes.Unknown, "private-dsn-and-stack-must-not-leak"),
			want: contractv1.ResultCode_RESULT_CODE_INTERNAL,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := resultCode(safeResultCode(test.err))
			if got != test.want {
				t.Fatalf("mapped code = %s, want %s", got, test.want)
			}
		})
	}
}

type fakeClientConnection struct {
	invokeErr error
}

func (connection *fakeClientConnection) Invoke(
	context.Context,
	string,
	any,
	any,
	...grpcgo.CallOption,
) error {
	return connection.invokeErr
}

func (*fakeClientConnection) NewStream(
	context.Context,
	*grpcgo.StreamDesc,
	string,
	...grpcgo.CallOption,
) (grpcgo.ClientStream, error) {
	return nil, errors.New("not implemented")
}
