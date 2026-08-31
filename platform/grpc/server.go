package grpc

import (
	"context"
	"errors"
	"fmt"
	"net"
	"reflect"
	"strings"
	"sync/atomic"
	"time"

	"github.com/v0hmly/marketmesh/platform/logger"
	serviceruntime "github.com/v0hmly/marketmesh/platform/runtime"
	"github.com/v0hmly/marketmesh/platform/telemetry"
	grpcgo "google.golang.org/grpc"
	"google.golang.org/grpc/health"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/keepalive"
	"google.golang.org/grpc/reflection"
)

const (
	// LivenessService — имя отдельного standard gRPC health service.
	LivenessService = "marketmesh.health.v1.Liveness"

	// ReadinessService — имя отдельного standard gRPC health service.
	ReadinessService = "marketmesh.health.v1.Readiness"
)

// ServerConfig задаёт безопасный transport и обязательные middleware.
// Authentication interceptors выполняются после recovery, logging и status
// mapping, но до дополнительных interceptors.
type ServerConfig struct {
	Environment            string
	ConnectionTimeout      time.Duration
	RequestTimeout         time.Duration
	KeepaliveTime          time.Duration
	KeepaliveTimeout       time.Duration
	MaxReceiveMessageBytes int
	MaxSendMessageBytes    int
	Security               ServerSecurity
	Logger                 *logger.Logger
	Telemetry              *telemetry.Telemetry
	ErrorCodeMapper        ErrorCodeMapper
	UnaryAuthentication    grpcgo.UnaryServerInterceptor
	StreamAuthentication   grpcgo.StreamServerInterceptor
	UnaryInterceptors      []grpcgo.UnaryServerInterceptor
	StreamInterceptors     []grpcgo.StreamServerInterceptor
	EnableReflection       bool
}

// Server хранит обычный grpc.Server и standard health service.
type Server struct {
	server           *grpcgo.Server
	health           *health.Server
	componentCreated atomic.Bool
	running          atomic.Bool
	stopping         atomic.Bool
}

// NewServer создаёт gRPC server без запуска listener и без регистрации
// доменных services.
func NewServer(config ServerConfig) (*Server, error) {
	if strings.TrimSpace(config.Environment) == "" {
		return nil, errors.New("grpc: server environment must not be empty")
	}
	if config.Logger == nil {
		return nil, errors.New("grpc: server logger must not be nil")
	}
	if config.Telemetry == nil {
		return nil, errors.New("grpc: server telemetry must not be nil")
	}
	if config.EnableReflection && isProduction(config.Environment) {
		return nil, errors.New("grpc: reflection is forbidden in production")
	}
	if err := validateServerConfig(config); err != nil {
		return nil, err
	}

	transportCredentials, err := serverTransportCredentials(config.Environment, config.Security)
	if err != nil {
		return nil, err
	}

	unaryInterceptors := []grpcgo.UnaryServerInterceptor{
		unaryServerTimeoutInterceptor(config.RequestTimeout),
		unaryServerRecoveryInterceptor(config.Logger),
		unaryServerLoggingInterceptor(config.Logger),
		unaryServerStatusInterceptor(config.ErrorCodeMapper),
	}
	if config.UnaryAuthentication != nil {
		unaryInterceptors = append(unaryInterceptors, config.UnaryAuthentication)
	}
	unaryInterceptors = append(unaryInterceptors, config.UnaryInterceptors...)

	streamInterceptors := []grpcgo.StreamServerInterceptor{
		streamServerTimeoutInterceptor(config.RequestTimeout),
		streamServerRecoveryInterceptor(config.Logger),
		streamServerLoggingInterceptor(config.Logger),
		streamServerStatusInterceptor(config.ErrorCodeMapper),
	}
	if config.StreamAuthentication != nil {
		streamInterceptors = append(streamInterceptors, config.StreamAuthentication)
	}
	streamInterceptors = append(streamInterceptors, config.StreamInterceptors...)

	serverOptions := []grpcgo.ServerOption{
		grpcgo.ConnectionTimeout(config.ConnectionTimeout),
		grpcgo.MaxRecvMsgSize(config.MaxReceiveMessageBytes),
		grpcgo.MaxSendMsgSize(config.MaxSendMessageBytes),
		grpcgo.KeepaliveParams(keepalive.ServerParameters{
			Time:    config.KeepaliveTime,
			Timeout: config.KeepaliveTimeout,
		}),
		grpcgo.Creds(transportCredentials),
		grpcgo.StatsHandler(config.Telemetry.GRPCServerStatsHandler()),
		grpcgo.ChainUnaryInterceptor(unaryInterceptors...),
		grpcgo.ChainStreamInterceptor(streamInterceptors...),
	}
	server := grpcgo.NewServer(serverOptions...)

	healthServer := health.NewServer()
	healthServer.SetServingStatus("", grpc_health_v1.HealthCheckResponse_NOT_SERVING)
	healthServer.SetServingStatus(LivenessService, grpc_health_v1.HealthCheckResponse_NOT_SERVING)
	healthServer.SetServingStatus(ReadinessService, grpc_health_v1.HealthCheckResponse_NOT_SERVING)
	grpc_health_v1.RegisterHealthServer(server, healthServer)
	if config.EnableReflection {
		reflection.Register(server)
	}

	return &Server{
		server: server,
		health: healthServer,
	}, nil
}

// GRPCServer возвращает стандартный server для регистрации protobuf services.
func (server *Server) GRPCServer() *grpcgo.Server {
	if server == nil {
		return nil
	}

	return server.server
}

// MarkReady переводит readiness в SERVING до начала shutdown.
func (server *Server) MarkReady() {
	if server == nil || !server.running.Load() || server.stopping.Load() {
		return
	}

	server.health.SetServingStatus("", grpc_health_v1.HealthCheckResponse_SERVING)
	server.health.SetServingStatus(ReadinessService, grpc_health_v1.HealthCheckResponse_SERVING)
}

// MarkNotReady немедленно прекращает readiness.
func (server *Server) MarkNotReady() {
	if server == nil {
		return
	}

	server.health.SetServingStatus("", grpc_health_v1.HealthCheckResponse_NOT_SERVING)
	server.health.SetServingStatus(ReadinessService, grpc_health_v1.HealthCheckResponse_NOT_SERVING)
}

// Component связывает server с listener и runtime lifecycle. Метод можно
// вызвать только один раз. При старте liveness/readiness становятся SERVING;
// shutdown сначала снимает readiness, затем ограниченно ждёт активные RPC.
func (server *Server) Component(
	name string,
	listener net.Listener,
) (serviceruntime.Component, error) {
	if server == nil || server.server == nil {
		return serviceruntime.Component{}, errors.New("grpc: server must not be nil")
	}
	if isNilListener(listener) {
		return serviceruntime.Component{}, errors.New("grpc: server listener must not be nil")
	}
	if !server.componentCreated.CompareAndSwap(false, true) {
		return serviceruntime.Component{}, errors.New("grpc: server component has already been created")
	}

	return serviceruntime.Component{
		Name: name,
		Run: func(ctx context.Context) error {
			if !server.stopping.Load() {
				server.running.Store(true)
				server.health.SetServingStatus(
					LivenessService,
					grpc_health_v1.HealthCheckResponse_SERVING,
				)
				server.MarkReady()
			}
			defer func() {
				server.running.Store(false)
				server.MarkNotReady()
				server.health.SetServingStatus(
					LivenessService,
					grpc_health_v1.HealthCheckResponse_NOT_SERVING,
				)
			}()

			err := server.server.Serve(listener)
			if errors.Is(err, grpcgo.ErrServerStopped) || errors.Is(err, net.ErrClosed) {
				return nil
			}
			if err != nil {
				return fmt.Errorf("grpc: serving: %w", err)
			}

			return nil
		},
		Shutdown: func(ctx context.Context) error {
			server.stopping.Store(true)
			server.MarkNotReady()

			stopped := make(chan struct{})
			go func() {
				server.server.GracefulStop()
				close(stopped)
			}()

			var err error
			select {
			case <-stopped:
			case <-ctx.Done():
				server.server.Stop()
				err = fmt.Errorf("grpc: forcing server stop: %w", ctx.Err())
			}
			server.running.Store(false)
			server.health.SetServingStatus(
				LivenessService,
				grpc_health_v1.HealthCheckResponse_NOT_SERVING,
			)

			return err
		},
	}, nil
}

func isNilListener(listener net.Listener) bool {
	if listener == nil {
		return true
	}

	value := reflect.ValueOf(listener)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func validateServerConfig(config ServerConfig) error {
	if config.ConnectionTimeout <= 0 {
		return errors.New("grpc: server connection timeout must be positive")
	}
	if config.RequestTimeout <= 0 {
		return errors.New("grpc: server request timeout must be positive")
	}
	if config.KeepaliveTime <= 0 {
		return errors.New("grpc: server keepalive time must be positive")
	}
	if config.KeepaliveTimeout <= 0 {
		return errors.New("grpc: server keepalive timeout must be positive")
	}
	if config.MaxReceiveMessageBytes <= 0 {
		return errors.New("grpc: server max receive message bytes must be positive")
	}
	if config.MaxSendMessageBytes <= 0 {
		return errors.New("grpc: server max send message bytes must be positive")
	}
	for _, interceptor := range config.UnaryInterceptors {
		if interceptor == nil {
			return errors.New("grpc: server unary interceptor must not be nil")
		}
	}
	for _, interceptor := range config.StreamInterceptors {
		if interceptor == nil {
			return errors.New("grpc: server stream interceptor must not be nil")
		}
	}

	return nil
}
