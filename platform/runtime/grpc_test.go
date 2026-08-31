package runtime

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/test/bufconn"
)

func TestNewGRPCServerRequiresLimits(t *testing.T) {
	t.Parallel()

	valid := validGRPCServerConfig()
	tests := []struct {
		name   string
		mutate func(*GRPCServerConfig)
	}{
		{name: "connection timeout", mutate: func(config *GRPCServerConfig) { config.ConnectionTimeout = 0 }},
		{name: "request timeout", mutate: func(config *GRPCServerConfig) { config.RequestTimeout = 0 }},
		{name: "keepalive time", mutate: func(config *GRPCServerConfig) { config.KeepaliveTime = 0 }},
		{name: "keepalive timeout", mutate: func(config *GRPCServerConfig) { config.KeepaliveTimeout = 0 }},
		{name: "receive bytes", mutate: func(config *GRPCServerConfig) { config.MaxReceiveMessageBytes = 0 }},
		{name: "send bytes", mutate: func(config *GRPCServerConfig) { config.MaxSendMessageBytes = 0 }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			config := valid
			test.mutate(&config)
			if _, err := NewGRPCServer(config); err == nil {
				t.Fatal("NewGRPCServer() error = nil, want validation error")
			}
		})
	}
}

func TestUnaryTimeoutInterceptorAddsAndPreservesDeadlines(t *testing.T) {
	t.Parallel()

	const timeout = time.Second
	interceptor := unaryTimeoutInterceptor(timeout)

	t.Run("adds default deadline", func(t *testing.T) {
		t.Parallel()

		started := time.Now()
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
		if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
			t.Fatalf("interceptor elapsed = %v", elapsed)
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

func TestStreamTimeoutInterceptorAddsDeadline(t *testing.T) {
	t.Parallel()

	interceptor := streamTimeoutInterceptor(time.Second)
	stream := &stubServerStream{ctx: context.Background()}
	err := interceptor(nil, stream, nil, func(_ any, wrapped grpc.ServerStream) error {
		if _, found := wrapped.Context().Deadline(); !found {
			t.Fatal("stream context has no deadline")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("interceptor error = %v", err)
	}
}

func TestGRPCComponentStopsServe(t *testing.T) {
	t.Parallel()

	server, err := NewGRPCServer(validGRPCServerConfig())
	if err != nil {
		t.Fatalf("NewGRPCServer() error = %v", err)
	}
	listener := bufconn.Listen(1024 * 1024)
	component, err := NewGRPCComponent("grpc", server, listener)
	if err != nil {
		t.Fatalf("NewGRPCComponent() error = %v", err)
	}

	runDone := make(chan error, 1)
	go func() { runDone <- component.Run(t.Context()) }()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := component.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("component.Shutdown() error = %v", err)
	}
	select {
	case err := <-runDone:
		if err != nil {
			t.Fatalf("component.Run() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("component.Run() did not stop")
	}
}

func validGRPCServerConfig() GRPCServerConfig {
	return GRPCServerConfig{
		ConnectionTimeout:      5 * time.Second,
		RequestTimeout:         10 * time.Second,
		KeepaliveTime:          30 * time.Second,
		KeepaliveTimeout:       10 * time.Second,
		MaxReceiveMessageBytes: 4 * 1024 * 1024,
		MaxSendMessageBytes:    4 * 1024 * 1024,
	}
}

type stubServerStream struct {
	ctx context.Context
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
