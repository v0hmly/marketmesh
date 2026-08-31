package grpc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/v0hmly/marketmesh/platform/logger"
	"github.com/v0hmly/marketmesh/platform/telemetry"
	grpcgo "google.golang.org/grpc"
	"google.golang.org/grpc/connectivity"
	"google.golang.org/grpc/keepalive"
)

// ContextDialer позволяет явно заменить transport dialer, например bufconn
// в integration tests.
type ContextDialer func(ctx context.Context, address string) (net.Conn, error)

// ClientConfig задаёт одно переиспользуемое gRPC connection.
// CallTimeout ограничивает весь unary call или stream, включая safe retries.
type ClientConfig struct {
	Target                 string
	Environment            string
	ConnectTimeout         time.Duration
	CallTimeout            time.Duration
	KeepaliveTime          time.Duration
	KeepaliveTimeout       time.Duration
	MaxReceiveMessageBytes int
	MaxSendMessageBytes    int
	Security               ClientSecurity
	Logger                 *logger.Logger
	Telemetry              *telemetry.Telemetry
	Retry                  *RetryPolicy
	UnaryAuthentication    grpcgo.UnaryClientInterceptor
	StreamAuthentication   grpcgo.StreamClientInterceptor
	UnaryInterceptors      []grpcgo.UnaryClientInterceptor
	StreamInterceptors     []grpcgo.StreamClientInterceptor
	Dialer                 ContextDialer
}

// Client владеет одним переиспользуемым grpc.ClientConn.
type Client struct {
	connection *grpcgo.ClientConn
	closeOnce  sync.Once
	closeErr   error
}

// NewClient создаёт connection и ограниченно ждёт состояния Ready.
func NewClient(ctx context.Context, config ClientConfig) (*Client, error) {
	if ctx == nil {
		return nil, errors.New("grpc: client context must not be nil")
	}
	target := strings.TrimSpace(config.Target)
	if target == "" {
		return nil, errors.New("grpc: client target must not be empty")
	}
	if strings.TrimSpace(config.Environment) == "" {
		return nil, errors.New("grpc: client environment must not be empty")
	}
	if config.ConnectTimeout <= 0 {
		return nil, errors.New("grpc: client connect timeout must be positive")
	}
	if config.CallTimeout <= 0 {
		return nil, errors.New("grpc: client call timeout must be positive")
	}
	if config.KeepaliveTime <= 0 {
		return nil, errors.New("grpc: client keepalive time must be positive")
	}
	if config.KeepaliveTimeout <= 0 {
		return nil, errors.New("grpc: client keepalive timeout must be positive")
	}
	if config.MaxReceiveMessageBytes <= 0 {
		return nil, errors.New("grpc: client max receive message bytes must be positive")
	}
	if config.MaxSendMessageBytes <= 0 {
		return nil, errors.New("grpc: client max send message bytes must be positive")
	}
	if config.Logger == nil {
		return nil, errors.New("grpc: client logger must not be nil")
	}
	if config.Telemetry == nil {
		return nil, errors.New("grpc: client telemetry must not be nil")
	}
	if err := validateClientInterceptors(config); err != nil {
		return nil, err
	}

	transportCredentials, err := clientTransportCredentials(config.Environment, config.Security)
	if err != nil {
		return nil, err
	}
	retry, err := newRetrySettings(config.Retry)
	if err != nil {
		return nil, err
	}

	unaryInterceptors := []grpcgo.UnaryClientInterceptor{
		unaryClientDeadlineInterceptor(config.CallTimeout),
		unaryClientLoggingInterceptor(config.Logger),
	}
	if config.Retry != nil {
		unaryInterceptors = append(unaryInterceptors, retry.interceptor())
	}
	if config.UnaryAuthentication != nil {
		unaryInterceptors = append(unaryInterceptors, config.UnaryAuthentication)
	}
	unaryInterceptors = append(unaryInterceptors, config.UnaryInterceptors...)

	streamInterceptors := []grpcgo.StreamClientInterceptor{
		streamClientDeadlineInterceptor(config.CallTimeout),
		streamClientLoggingInterceptor(config.Logger),
	}
	if config.StreamAuthentication != nil {
		streamInterceptors = append(streamInterceptors, config.StreamAuthentication)
	}
	streamInterceptors = append(streamInterceptors, config.StreamInterceptors...)

	options := []grpcgo.DialOption{
		grpcgo.WithTransportCredentials(transportCredentials),
		grpcgo.WithStatsHandler(config.Telemetry.GRPCClientStatsHandler()),
		grpcgo.WithKeepaliveParams(keepalive.ClientParameters{
			Time:                config.KeepaliveTime,
			Timeout:             config.KeepaliveTimeout,
			PermitWithoutStream: false,
		}),
		grpcgo.WithDefaultCallOptions(
			grpcgo.MaxCallRecvMsgSize(config.MaxReceiveMessageBytes),
			grpcgo.MaxCallSendMsgSize(config.MaxSendMessageBytes),
		),
		grpcgo.WithChainUnaryInterceptor(unaryInterceptors...),
		grpcgo.WithChainStreamInterceptor(streamInterceptors...),
	}
	if config.Dialer != nil {
		options = append(options, grpcgo.WithContextDialer(config.Dialer))
	}

	connection, err := grpcgo.NewClient(target, options...)
	if err != nil {
		return nil, fmt.Errorf("grpc: creating client connection: %w", err)
	}
	connectCtx, cancel := contextWithMaximumTimeout(ctx, config.ConnectTimeout)
	defer cancel()
	if err := waitUntilReady(connectCtx, connection); err != nil {
		closeErr := connection.Close()
		return nil, errors.Join(
			fmt.Errorf("grpc: connecting client: %w", err),
			closeErr,
		)
	}

	return &Client{connection: connection}, nil
}

// Connection возвращает одно разделяемое grpc.ClientConn.
func (client *Client) Connection() *grpcgo.ClientConn {
	if client == nil {
		return nil
	}

	return client.connection
}

// Close один раз закрывает принадлежащее клиенту connection.
func (client *Client) Close() error {
	if client == nil || client.connection == nil {
		return nil
	}

	client.closeOnce.Do(func() {
		client.closeErr = client.connection.Close()
	})

	return client.closeErr
}

func waitUntilReady(ctx context.Context, connection *grpcgo.ClientConn) error {
	connection.Connect()
	for {
		state := connection.GetState()
		if state == connectivity.Ready {
			return nil
		}
		if !connection.WaitForStateChange(ctx, state) {
			return ctx.Err()
		}
	}
}

func validateClientInterceptors(config ClientConfig) error {
	for _, interceptor := range config.UnaryInterceptors {
		if interceptor == nil {
			return errors.New("grpc: client unary interceptor must not be nil")
		}
	}
	for _, interceptor := range config.StreamInterceptors {
		if interceptor == nil {
			return errors.New("grpc: client stream interceptor must not be nil")
		}
	}

	return nil
}
