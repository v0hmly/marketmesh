package grpc

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/v0hmly/marketmesh/platform/telemetry"
	grpcgo "google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

func TestServerComponentRejectsTypedNilListener(t *testing.T) {
	t.Parallel()

	log, _ := newIntegrationLogger(t)
	server, err := NewServer(validIntegrationServerConfig(log, telemetry.NewNoop()))
	if err != nil {
		t.Fatalf("NewServer() error = %v", err)
	}

	var listener *typedNilListener
	if _, err := server.Component("grpc", listener); err == nil {
		t.Fatal("Component() error = nil, want typed nil listener error")
	}
}

func TestNewServerRequiresLimits(t *testing.T) {
	t.Parallel()

	log, _ := newIntegrationLogger(t)
	valid := validIntegrationServerConfig(log, telemetry.NewNoop())
	tests := []struct {
		name   string
		mutate func(*ServerConfig)
	}{
		{name: "connection timeout", mutate: func(config *ServerConfig) { config.ConnectionTimeout = 0 }},
		{name: "request timeout", mutate: func(config *ServerConfig) { config.RequestTimeout = 0 }},
		{name: "keepalive time", mutate: func(config *ServerConfig) { config.KeepaliveTime = 0 }},
		{name: "keepalive timeout", mutate: func(config *ServerConfig) { config.KeepaliveTimeout = 0 }},
		{name: "receive bytes", mutate: func(config *ServerConfig) { config.MaxReceiveMessageBytes = 0 }},
		{name: "send bytes", mutate: func(config *ServerConfig) { config.MaxSendMessageBytes = 0 }},
		{
			name: "nil unary interceptor",
			mutate: func(config *ServerConfig) {
				config.UnaryInterceptors = []grpcgo.UnaryServerInterceptor{nil}
			},
		},
		{
			name: "nil stream interceptor",
			mutate: func(config *ServerConfig) {
				config.StreamInterceptors = []grpcgo.StreamServerInterceptor{nil}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			config := valid
			test.mutate(&config)
			if _, err := NewServer(config); err == nil {
				t.Fatal("NewServer() error = nil, want validation error")
			}
		})
	}
}

func TestUnaryServerTimeoutInterceptorAddsAndPreservesDeadlines(t *testing.T) {
	t.Parallel()

	const timeout = time.Second
	interceptor := unaryServerTimeoutInterceptor(timeout)

	t.Run("adds maximum deadline", func(t *testing.T) {
		t.Parallel()

		_, err := interceptor(
			context.Background(),
			nil,
			nil,
			func(ctx context.Context, _ any) (any, error) {
				deadline, found := ctx.Deadline()
				if !found {
					t.Fatal("handler context has no deadline")
				}
				remaining := time.Until(deadline)
				if remaining <= 0 || remaining > timeout {
					t.Fatalf("handler deadline remaining = %v, want (0, %v]", remaining, timeout)
				}
				return nil, nil
			},
		)
		if err != nil {
			t.Fatalf("interceptor error = %v", err)
		}
	})

	t.Run("keeps shorter caller deadline", func(t *testing.T) {
		t.Parallel()

		parent, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		defer cancel()
		parentDeadline, _ := parent.Deadline()

		_, err := interceptor(parent, nil, nil, func(ctx context.Context, _ any) (any, error) {
			deadline, found := ctx.Deadline()
			if !found || !deadline.Equal(parentDeadline) {
				t.Fatalf("handler deadline = %v, %v; want %v", deadline, found, parentDeadline)
			}
			return nil, nil
		})
		if err != nil {
			t.Fatalf("interceptor error = %v", err)
		}
	})
}

func TestStreamServerTimeoutInterceptorAddsDeadline(t *testing.T) {
	t.Parallel()

	interceptor := streamServerTimeoutInterceptor(time.Second)
	stream := &stubServerStream{ctx: context.Background()}
	err := interceptor(nil, stream, nil, func(_ any, wrapped grpcgo.ServerStream) error {
		if _, found := wrapped.Context().Deadline(); !found {
			t.Fatal("stream context has no deadline")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("interceptor error = %v", err)
	}
}

type stubServerStream struct {
	ctx context.Context
}

type typedNilListener struct{}

func (*typedNilListener) Accept() (net.Conn, error) {
	return nil, errors.New("not implemented")
}

func (*typedNilListener) Close() error {
	return nil
}

func (*typedNilListener) Addr() net.Addr {
	return nil
}

func (*stubServerStream) SetHeader(metadata.MD) error {
	return nil
}

func (*stubServerStream) SendHeader(metadata.MD) error {
	return nil
}

func (*stubServerStream) SetTrailer(metadata.MD) {}

func (stream *stubServerStream) Context() context.Context {
	return stream.ctx
}

func (*stubServerStream) SendMsg(any) error {
	return errors.New("not implemented")
}

func (*stubServerStream) RecvMsg(any) error {
	return errors.New("not implemented")
}
