package app

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"

	e2ev1 "github.com/v0hmly/marketmesh/api/gen/go/e2e/v1"
	contractv1 "github.com/v0hmly/marketmesh/api/gen/go/tunnel/v1"
	protocolv1 "github.com/v0hmly/marketmesh/api/tunnel/v1"
	platformgrpc "github.com/v0hmly/marketmesh/platform/grpc"
	"github.com/v0hmly/marketmesh/platform/httpserver"
	"github.com/v0hmly/marketmesh/platform/logger"
	serviceruntime "github.com/v0hmly/marketmesh/platform/runtime"
	"github.com/v0hmly/marketmesh/platform/telemetry"
	"github.com/v0hmly/marketmesh/services/gateway-out/internal/tunnel"
	"google.golang.org/protobuf/proto"
)

// Run loads configuration and owns the E2E gateway-out lifecycle.
func Run(ctx context.Context) error {
	return run(ctx, systemDependencies{
		env:    serviceruntime.SystemEnv(),
		stdout: os.Stdout,
		stderr: os.Stderr,
		listen: net.Listen,
	})
}

type listenFunc func(network string, address string) (net.Listener, error)

type systemDependencies struct {
	env    serviceruntime.Env
	stdout io.Writer
	stderr io.Writer
	listen listenFunc
}

func run(ctx context.Context, dependencies systemDependencies) error {
	bootstrapLog, err := logger.New(logger.Config{
		Service: serviceName, Version: "unknown", Environment: "bootstrap", Output: dependencies.stderr,
	})
	if err != nil {
		return fmt.Errorf("creating bootstrap logger: %w", err)
	}
	cfg, err := loadConfig(dependencies.env)
	if err != nil {
		bootstrapLog.Error("не удалось загрузить конфигурацию", logger.Err(err))
		return fmt.Errorf("loading configuration: %w", err)
	}
	log, err := logger.New(logger.Config{
		Service: serviceName, Version: cfg.serviceVersion, Environment: cfg.environment,
		Level: cfg.logLevel, Output: dependencies.stdout,
		MaskFields: []string{
			"authorization", "cookie", "idempotency_key", "payload", "request_id", "token",
		},
	})
	if err != nil {
		bootstrapLog.Error("не удалось создать logger", logger.Err(err))
		return fmt.Errorf("creating logger: %w", err)
	}

	err = runService(ctx, cfg, log, dependencies.listen)
	if err != nil {
		log.ErrorContext(context.WithoutCancel(ctx), "gateway-out завершился с ошибкой", logger.Err(err))
	}

	return err
}

func runService(
	ctx context.Context,
	cfg config,
	log *logger.Logger,
	listen listenFunc,
) (resultErr error) {
	pipeline := telemetry.NewNoop()
	internalTLS, err := loadClientTLS(
		cfg.internalCertificate,
		cfg.internalPrivateKey,
		cfg.internalRootCA,
		cfg.internalServerName,
		cfg.expectedInternalURI,
	)
	if err != nil {
		return err
	}
	internalClient, err := platformgrpc.NewClient(ctx, platformgrpc.ClientConfig{
		Target: cfg.internalTarget, Environment: cfg.environment,
		ConnectTimeout: cfg.connectTimeout, CallTimeout: cfg.callTimeout,
		KeepaliveTime: 30 * time.Second, KeepaliveTimeout: 5 * time.Second,
		MaxReceiveMessageBytes: 64 * 1024, MaxSendMessageBytes: 64 * 1024,
		Security: platformgrpc.ClientSecurity{
			TLSConfig: internalTLS, RequireClientCertificate: true,
		},
		Logger: log, Telemetry: pipeline,
	})
	if err != nil {
		return fmt.Errorf("creating internal gRPC client: %w", err)
	}
	internalOwned := true
	defer func() {
		if internalOwned {
			resultErr = errors.Join(resultErr, internalClient.Close())
		}
	}()

	registry, err := tunnel.NewRegistry(
		tunnel.ClassClients{
			ControlAuth: internalClient.Connection(),
			Regular:     internalClient.Connection(),
			Realtime:    internalClient.Connection(),
		},
		readRoute(cfg.callTimeout),
		mutateRoute(cfg.callTimeout),
	)
	if err != nil {
		return fmt.Errorf("creating static route registry: %w", err)
	}
	tunnelTLS, err := loadClientTLS(
		cfg.tunnelCertificate,
		cfg.tunnelPrivateKey,
		cfg.tunnelRootCA,
		cfg.gatewayInServerName,
		cfg.expectedGatewayInURI,
	)
	if err != nil {
		return err
	}
	managedClients := make([]managedTunnel, 0, tunnelPoolSize)
	for slot := range tunnelPoolSize {
		client, clientErr := tunnel.NewClient(
			tunnelConfig(cfg, log, pipeline, tunnelTLS.Clone(), slot),
			registry,
		)
		if clientErr != nil {
			return fmt.Errorf("creating tunnel client: %w", clientErr)
		}
		managedClients = append(managedClients, client)
	}
	pool, err := newTunnelPool(managedClients)
	if err != nil {
		return err
	}
	health, err := serviceruntime.NewHealth(serviceruntime.HealthConfig{
		CheckTimeout: cfg.healthTimeout,
		Dependencies: []serviceruntime.CriticalDependency{{
			Name: "tunnel-session",
			Check: func(context.Context) error {
				if !pool.IsReady() {
					return errors.New("tunnel is not ready")
				}
				return nil
			},
		}},
	})
	if err != nil {
		return fmt.Errorf("creating health checks: %w", err)
	}
	handler, err := healthHandler(health)
	if err != nil {
		return err
	}
	httpServer, err := httpserver.New(httpserver.Config{
		Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second,
		RequestTimeout: cfg.healthTimeout, MaxHeaderBytes: 8 * 1024,
		MaxBodyBytes: 1024, Logger: log, Telemetry: pipeline,
	})
	if err != nil {
		return fmt.Errorf("creating health HTTP server: %w", err)
	}
	listener, err := listen("tcp", cfg.httpAddress)
	if err != nil {
		return fmt.Errorf("listening for health HTTP: %w", err)
	}
	listenerOwned := true
	defer func() {
		if listenerOwned {
			resultErr = errors.Join(resultErr, closeListener(listener))
		}
	}()
	httpComponent, err := httpserver.Component("health-http", httpServer, listener)
	if err != nil {
		return fmt.Errorf("creating health HTTP component: %w", err)
	}
	components := []serviceruntime.Component{
		telemetryComponent(pipeline),
		internalClientComponent(internalClient),
		pool.Component(),
	}
	components = append(components, httpComponent)
	runner, err := serviceruntime.NewRunner(
		serviceruntime.RunnerConfig{ShutdownTimeout: cfg.shutdownTimeout, Health: health},
		components...,
	)
	if err != nil {
		return fmt.Errorf("creating runner: %w", err)
	}

	internalOwned = false
	listenerOwned = false
	log.Info("gateway-out запущен", logger.String("http_address", listener.Addr().String()))
	if err := runner.Run(ctx); err != nil {
		return err
	}
	log.Info("gateway-out остановлен")

	return nil
}

func readRoute(callTimeout time.Duration) tunnel.RouteSpec {
	return tunnel.RouteSpec{
		ID:              contractv1.RouteId_ROUTE_ID_USER_GET_ME,
		TrafficClass:    contractv1.TrafficClass_TRAFFIC_CLASS_REGULAR,
		Method:          e2ev1.FakeInternalService_Read_FullMethodName,
		NewRequest:      func() proto.Message { return new(e2ev1.ReadRequest) },
		NewResponse:     func() proto.Message { return new(e2ev1.ReadResponse) },
		MaxRequestBytes: 16 * 1024, MaxResponseBytes: 16 * 1024,
		MaxDeadline: callTimeout,
	}
}

func mutateRoute(callTimeout time.Duration) tunnel.RouteSpec {
	return tunnel.RouteSpec{
		ID:              contractv1.RouteId_ROUTE_ID_USER_UPDATE_ME,
		TrafficClass:    contractv1.TrafficClass_TRAFFIC_CLASS_REGULAR,
		Method:          e2ev1.FakeInternalService_Mutate_FullMethodName,
		NewRequest:      func() proto.Message { return new(e2ev1.MutateRequest) },
		NewResponse:     func() proto.Message { return new(e2ev1.MutateResponse) },
		MaxRequestBytes: 16 * 1024, MaxResponseBytes: 16 * 1024,
		MaxDeadline: callTimeout, Mutating: true, RequireIdempotencyKey: true,
	}
}

func tunnelConfig(
	cfg config,
	log *logger.Logger,
	pipeline *telemetry.Telemetry,
	tlsConfig *tls.Config,
	slot int,
) tunnel.Config {
	digest := sha256.Sum256(append([]byte(cfg.instanceID), byte(slot)))
	instanceID := [protocolv1.InstanceIDBytes]byte{}
	copy(instanceID[:], digest[:protocolv1.InstanceIDBytes])

	return tunnel.Config{
		Target: cfg.gatewayInTarget, TLSConfig: tlsConfig,
		ExpectedServerIdentity: cfg.expectedGatewayInURI, InstanceID: instanceID,
		ConnectTimeout: cfg.connectTimeout, HandshakeTimeout: 5 * time.Second,
		KeepaliveTime: 30 * time.Second, KeepaliveTimeout: 5 * time.Second,
		PingInterval: 30 * time.Second, PingTimeout: 5 * time.Second,
		DrainTimeout: cfg.shutdownTimeout,
		Limits: tunnel.ReceiveLimits{
			MaxFrameBytes: 64 * 1024, MaxDataBytes: 16 * 1024, MaxMessageBytes: 64 * 1024,
			MaxInFlightRequests: 64, MaxMetadataEntries: 8,
			MaxMetadataValueBytes: 16 * 1024, MaxCreditBytes: 32 * 1024,
		},
		ClassLimits: map[contractv1.TrafficClass]tunnel.ClassLimits{
			contractv1.TrafficClass_TRAFFIC_CLASS_CONTROL_AUTH: {
				MaxInFlight: 4, SendQueueDepth: 8, ReceiveWindowBytes: 8 * 1024,
			},
			contractv1.TrafficClass_TRAFFIC_CLASS_REGULAR: {
				MaxInFlight: 32, SendQueueDepth: 64, ReceiveWindowBytes: 32 * 1024,
			},
			contractv1.TrafficClass_TRAFFIC_CLASS_REALTIME: {
				MaxInFlight: 4, SendQueueDepth: 8, ReceiveWindowBytes: 8 * 1024,
			},
		},
		Reconnect: tunnel.ReconnectPolicy{
			MaxAttempts: 100, InitialBackoff: 100 * time.Millisecond,
			MaxBackoff: 5 * time.Second, Multiplier: 2, JitterRatio: 0.2,
			StableResetAfter: time.Minute,
		},
		Logger: log, Telemetry: pipeline,
	}
}

func healthHandler(health *serviceruntime.Health) (http.Handler, error) {
	base, err := httpserver.NewHealthHandler(health)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.Handle("/", base)
	mux.HandleFunc("POST /drainz", func(response http.ResponseWriter, request *http.Request) {
		if host, _, splitErr := net.SplitHostPort(request.RemoteAddr); splitErr != nil ||
			(host != "127.0.0.1" && host != "::1") {
			http.Error(response, "forbidden", http.StatusForbidden)
			return
		}
		health.MarkNotReady()
		response.WriteHeader(http.StatusNoContent)
	})
	return mux, nil
}

func telemetryComponent(pipeline *telemetry.Telemetry) serviceruntime.Component {
	return serviceruntime.Component{
		Name: "telemetry",
		Run: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		},
		Shutdown: pipeline.Shutdown,
	}
}

func internalClientComponent(client *platformgrpc.Client) serviceruntime.Component {
	return serviceruntime.Component{
		Name: "internal-grpc",
		Run: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		},
		Shutdown: func(context.Context) error { return client.Close() },
	}
}

func closeListener(listener net.Listener) error {
	if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		return fmt.Errorf("closing listener: %w", err)
	}
	return nil
}
