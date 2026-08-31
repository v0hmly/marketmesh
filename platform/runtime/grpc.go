package runtime

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/stats"
)

// GRPCServerConfig содержит обязательные timeout и пределы gRPC server.
// Interceptors выполняются после встроенного deadline interceptor.
type GRPCServerConfig struct {
	ConnectionTimeout      time.Duration
	RequestTimeout         time.Duration
	KeepaliveTime          time.Duration
	KeepaliveTimeout       time.Duration
	MaxReceiveMessageBytes int
	MaxSendMessageBytes    int
	Credentials            credentials.TransportCredentials
	StatsHandler           stats.Handler
	UnaryInterceptors      []grpc.UnaryServerInterceptor
	StreamInterceptors     []grpc.StreamServerInterceptor
}

// NewGRPCServer создаёт *grpc.Server с обязательными connection/RPC timeout,
// keepalive timeout и ограничениями сообщений. Регистрация RPC и выбор
// listener остаются ответственностью composition root.
func NewGRPCServer(config GRPCServerConfig) (*grpc.Server, error) {
	if config.ConnectionTimeout <= 0 {
		return nil, errors.New("runtime: gRPC connection timeout must be positive")
	}
	if config.RequestTimeout <= 0 {
		return nil, errors.New("runtime: gRPC request timeout must be positive")
	}
	if config.KeepaliveTime <= 0 {
		return nil, errors.New("runtime: gRPC keepalive time must be positive")
	}
	if config.KeepaliveTimeout <= 0 {
		return nil, errors.New("runtime: gRPC keepalive timeout must be positive")
	}
	if config.MaxReceiveMessageBytes <= 0 {
		return nil, errors.New("runtime: gRPC max receive message bytes must be positive")
	}
	if config.MaxSendMessageBytes <= 0 {
		return nil, errors.New("runtime: gRPC max send message bytes must be positive")
	}
	for _, interceptor := range config.UnaryInterceptors {
		if interceptor == nil {
			return nil, errors.New("runtime: gRPC unary interceptor must not be nil")
		}
	}
	for _, interceptor := range config.StreamInterceptors {
		if interceptor == nil {
			return nil, errors.New("runtime: gRPC stream interceptor must not be nil")
		}
	}
	if config.Credentials != nil && isNilInterface(config.Credentials) {
		return nil, errors.New("runtime: gRPC transport credentials must not be typed nil")
	}
	if config.StatsHandler != nil && isNilInterface(config.StatsHandler) {
		return nil, errors.New("runtime: gRPC stats handler must not be typed nil")
	}

	unaryInterceptors := make(
		[]grpc.UnaryServerInterceptor,
		0,
		len(config.UnaryInterceptors)+1,
	)
	unaryInterceptors = append(unaryInterceptors, unaryTimeoutInterceptor(config.RequestTimeout))
	unaryInterceptors = append(unaryInterceptors, config.UnaryInterceptors...)

	streamInterceptors := make(
		[]grpc.StreamServerInterceptor,
		0,
		len(config.StreamInterceptors)+1,
	)
	streamInterceptors = append(streamInterceptors, streamTimeoutInterceptor(config.RequestTimeout))
	streamInterceptors = append(streamInterceptors, config.StreamInterceptors...)

	serverOptions := []grpc.ServerOption{
		grpc.ConnectionTimeout(config.ConnectionTimeout),
		grpc.MaxRecvMsgSize(config.MaxReceiveMessageBytes),
		grpc.MaxSendMsgSize(config.MaxSendMessageBytes),
		grpc.KeepaliveParams(keepalive.ServerParameters{
			Time:    config.KeepaliveTime,
			Timeout: config.KeepaliveTimeout,
		}),
		grpc.ChainUnaryInterceptor(unaryInterceptors...),
		grpc.ChainStreamInterceptor(streamInterceptors...),
	}
	if config.Credentials != nil {
		serverOptions = append(serverOptions, grpc.Creds(config.Credentials))
	}
	if config.StatsHandler != nil {
		serverOptions = append(serverOptions, grpc.StatsHandler(config.StatsHandler))
	}

	return grpc.NewServer(serverOptions...), nil
}

// NewGRPCComponent адаптирует стандартный grpc.Server к Component. При
// исчерпании shutdown deadline GracefulStop принудительно завершается Stop.
func NewGRPCComponent(
	name string,
	server *grpc.Server,
	listener net.Listener,
) (Component, error) {
	if server == nil {
		return Component{}, errors.New("runtime: gRPC server must not be nil")
	}
	if isNilInterface(listener) {
		return Component{}, errors.New("runtime: gRPC listener must not be nil")
	}

	return Component{
		Name: name,
		Run: func(context.Context) error {
			err := server.Serve(listener)
			if errors.Is(err, grpc.ErrServerStopped) || errors.Is(err, net.ErrClosed) {
				return nil
			}
			if err != nil {
				return fmt.Errorf("serving gRPC: %w", err)
			}

			return nil
		},
		Shutdown: func(ctx context.Context) error {
			stopped := make(chan struct{})
			go func() {
				server.GracefulStop()
				close(stopped)
			}()

			select {
			case <-stopped:
				return nil
			case <-ctx.Done():
				server.Stop()
				return fmt.Errorf("forcing gRPC server stop: %w", ctx.Err())
			}
		},
	}, nil
}

func unaryTimeoutInterceptor(timeout time.Duration) grpc.UnaryServerInterceptor {
	return func(
		ctx context.Context,
		request any,
		_ *grpc.UnaryServerInfo,
		handler grpc.UnaryHandler,
	) (any, error) {
		requestCtx, cancel := contextWithMaximumTimeout(ctx, timeout)
		defer cancel()

		return handler(requestCtx, request)
	}
}

func streamTimeoutInterceptor(timeout time.Duration) grpc.StreamServerInterceptor {
	return func(
		server any,
		stream grpc.ServerStream,
		_ *grpc.StreamServerInfo,
		handler grpc.StreamHandler,
	) error {
		streamCtx, cancel := contextWithMaximumTimeout(stream.Context(), timeout)
		defer cancel()

		return handler(server, &serverStreamWithContext{
			ServerStream: stream,
			ctx:          streamCtx,
		})
	}
}

type serverStreamWithContext struct {
	grpc.ServerStream
	ctx context.Context
}

func (stream *serverStreamWithContext) Context() context.Context {
	return stream.ctx
}

func contextWithMaximumTimeout(
	ctx context.Context,
	timeout time.Duration,
) (context.Context, context.CancelFunc) {
	deadline, hasDeadline := ctx.Deadline()
	if hasDeadline && time.Until(deadline) <= timeout {
		return context.WithCancel(ctx)
	}

	return context.WithTimeout(ctx, timeout)
}
