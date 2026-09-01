package app

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"

	e2ev1 "github.com/v0hmly/marketmesh/api/gen/go/e2e/v1"
	e2ev1connect "github.com/v0hmly/marketmesh/api/gen/go/e2e/v1/e2ev1connect"
	contractv1 "github.com/v0hmly/marketmesh/api/gen/go/tunnel/v1"
	protocolv1 "github.com/v0hmly/marketmesh/api/tunnel/v1"
	platformgrpc "github.com/v0hmly/marketmesh/platform/grpc"
	"github.com/v0hmly/marketmesh/platform/httpserver"
	"github.com/v0hmly/marketmesh/platform/logger"
	serviceruntime "github.com/v0hmly/marketmesh/platform/runtime"
	"github.com/v0hmly/marketmesh/platform/telemetry"
	"github.com/v0hmly/marketmesh/services/gateway-in/internal/connectbridge"
	"github.com/v0hmly/marketmesh/services/gateway-in/internal/tunnel"
)

// Run loads configuration and owns the E2E gateway-in lifecycle.
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
		log.ErrorContext(context.WithoutCancel(ctx), "gateway-in завершился с ошибкой", logger.Err(err))
	}

	return err
}

func runService(
	ctx context.Context,
	cfg config,
	log *logger.Logger,
	listen listenFunc,
) (resultErr error) {
	tlsConfig, err := loadServerTLS(cfg.tlsCertificate, cfg.tlsPrivateKey, cfg.tlsClientCA)
	if err != nil {
		return err
	}
	pipeline := telemetry.NewNoop()
	tunnelServer, err := tunnel.New(tunnelConfig(cfg, log, pipeline))
	if err != nil {
		return fmt.Errorf("creating tunnel server: %w", err)
	}
	grpcServer, err := platformgrpc.NewServer(platformgrpc.ServerConfig{
		Environment:            cfg.environment,
		ConnectionTimeout:      5 * time.Second,
		RequestTimeout:         cfg.tunnelSessionTimeout,
		KeepaliveTime:          30 * time.Second,
		KeepaliveTimeout:       5 * time.Second,
		MaxReceiveMessageBytes: protocolv1.MaxEncodedFrameBytes,
		MaxSendMessageBytes:    protocolv1.MaxEncodedFrameBytes,
		Security: platformgrpc.ServerSecurity{
			TLSConfig: tlsConfig, RequireClientCertificate: true,
		},
		Logger: log, Telemetry: pipeline,
	})
	if err != nil {
		return fmt.Errorf("creating tunnel gRPC server: %w", err)
	}
	contractv1.RegisterTunnelServiceServer(grpcServer.GRPCServer(), tunnelServer)

	health, err := newHealth(cfg, tunnelServer.Registry())
	if err != nil {
		return err
	}
	handler, err := publicHandler(health, tunnelServer.Registry())
	if err != nil {
		return err
	}
	httpServer, err := httpserver.New(httpserver.Config{
		Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second,
		WriteTimeout: 15 * time.Second, IdleTimeout: 60 * time.Second,
		RequestTimeout: cfg.requestTimeout, MaxHeaderBytes: 32 * 1024,
		MaxBodyBytes: 64 * 1024, Logger: log, Telemetry: pipeline,
	})
	if err != nil {
		return fmt.Errorf("creating public HTTP server: %w", err)
	}

	grpcListener, err := listen("tcp", cfg.grpcAddress)
	if err != nil {
		return fmt.Errorf("listening for tunnel gRPC: %w", err)
	}
	httpListener, err := listen("tcp", cfg.httpAddress)
	if err != nil {
		_ = grpcListener.Close()
		return fmt.Errorf("listening for public HTTP: %w", err)
	}
	listenersOwned := true
	defer func() {
		if listenersOwned {
			resultErr = errors.Join(resultErr, closeListener(grpcListener), closeListener(httpListener))
		}
	}()

	grpcComponent, err := grpcServer.Component("tunnel-grpc", grpcListener)
	if err != nil {
		return fmt.Errorf("creating tunnel gRPC component: %w", err)
	}
	httpComponent, err := httpserver.Component("public-http", httpServer, httpListener)
	if err != nil {
		return fmt.Errorf("creating public HTTP component: %w", err)
	}
	runner, err := serviceruntime.NewRunner(
		serviceruntime.RunnerConfig{ShutdownTimeout: cfg.shutdownTimeout, Health: health},
		telemetryComponent(pipeline),
		grpcComponent,
		tunnelDrainComponent(tunnelServer.Registry()),
		httpComponent,
	)
	if err != nil {
		return fmt.Errorf("creating runner: %w", err)
	}

	listenersOwned = false
	log.Info(
		"gateway-in запущен",
		logger.String("grpc_address", grpcListener.Addr().String()),
		logger.String("http_address", httpListener.Addr().String()),
	)
	if err := runner.Run(ctx); err != nil {
		return err
	}
	log.Info("gateway-in остановлен")

	return nil
}

func tunnelConfig(
	cfg config,
	log *logger.Logger,
	pipeline *telemetry.Telemetry,
) tunnel.Config {
	digest := sha256.Sum256([]byte(cfg.instanceID))
	instanceID := [protocolv1.InstanceIDBytes]byte{}
	copy(instanceID[:], digest[:protocolv1.InstanceIDBytes])
	limits := &contractv1.Limits{
		MaxFrameBytes: 64 * 1024, MaxDataBytes: 16 * 1024, MaxMessageBytes: 64 * 1024,
		MaxInFlightRequests: 64, MaxMetadataEntries: 8, MaxMetadataValueBytes: 16 * 1024,
		MaxCreditBytes: 32 * 1024,
	}
	routes := map[contractv1.RouteId]tunnel.RoutePolicy{
		contractv1.RouteId_ROUTE_ID_USER_GET_ME: {
			TrafficClass:    contractv1.TrafficClass_TRAFFIC_CLASS_REGULAR,
			MaxRequestBytes: 16 * 1024, MaxResponseBytes: 16 * 1024,
			MaxDeadline: cfg.requestTimeout, MaxInFlight: 32,
		},
		contractv1.RouteId_ROUTE_ID_USER_UPDATE_ME: {
			TrafficClass:    contractv1.TrafficClass_TRAFFIC_CLASS_REGULAR,
			MaxRequestBytes: 16 * 1024, MaxResponseBytes: 16 * 1024,
			MaxDeadline: cfg.requestTimeout, MaxInFlight: 32,
		},
	}

	return tunnel.Config{
		InstanceID: instanceID,
		Peer:       tunnel.PeerPolicy{AllowedURIs: []string{cfg.expectedGatewayOutURI}},
		Limits:     limits, Routes: routes,
		Capabilities: []contractv1.Capability{contractv1.Capability_CAPABILITY_DRAIN},
		Queues:       tunnel.QueueLimits{TunnelControl: 8, ControlAuth: 8, Regular: 64, Realtime: 8},
		MaxTunnels:   32, MaxTunnelsPerInstance: 4, MaxInFlightPerInstance: 64,
		InitialResponseCredit: 32 * 1024, HandshakeTimeout: 5 * time.Second,
		PingInterval: 30 * time.Second, PongTimeout: 5 * time.Second,
		Logger: log.Slog(), MeterProvider: pipeline.MeterProvider(),
		TracerProvider: pipeline.TracerProvider(),
	}
}

func newHealth(cfg config, registry *tunnel.Registry) (*serviceruntime.Health, error) {
	return serviceruntime.NewHealth(serviceruntime.HealthConfig{
		CheckTimeout: cfg.healthTimeout,
		Dependencies: []serviceruntime.CriticalDependency{{
			Name: "tunnel-routes",
			Check: func(context.Context) error {
				readReady := registry.IsRouteReady(contractv1.RouteId_ROUTE_ID_USER_GET_ME)
				mutateReady := registry.IsRouteReady(contractv1.RouteId_ROUTE_ID_USER_UPDATE_ME)
				if !readReady || !mutateReady {
					return errors.New("required routes are not ready")
				}
				return nil
			},
		}},
	})
}

func publicHandler(
	health *serviceruntime.Health,
	registry *tunnel.Registry,
) (http.Handler, error) {
	healthHandler, err := httpserver.NewHealthHandler(health)
	if err != nil {
		return nil, err
	}
	readHandler, err := connectbridge.NewUnaryHandler[e2ev1.ReadRequest, e2ev1.ReadResponse](
		connectbridge.Config{
			Procedure: e2ev1connect.FakeInternalServiceReadProcedure,
			Route:     contractv1.RouteId_ROUTE_ID_USER_GET_ME,
			Invoker:   registry,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("creating read bridge: %w", err)
	}
	mutateHandler, err := connectbridge.NewUnaryHandler[e2ev1.MutateRequest, e2ev1.MutateResponse](
		connectbridge.Config{
			Procedure:             e2ev1connect.FakeInternalServiceMutateProcedure,
			Route:                 contractv1.RouteId_ROUTE_ID_USER_UPDATE_ME,
			RequireIdempotencyKey: true,
			Invoker:               registry,
		},
	)
	if err != nil {
		return nil, fmt.Errorf("creating mutate bridge: %w", err)
	}

	mux := http.NewServeMux()
	mux.Handle("/", healthHandler)
	mux.Handle(e2ev1connect.FakeInternalServiceReadProcedure, readHandler)
	mux.Handle(e2ev1connect.FakeInternalServiceMutateProcedure, mutateHandler)
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

func tunnelDrainComponent(registry *tunnel.Registry) serviceruntime.Component {
	return serviceruntime.Component{
		Name: "tunnel-drain",
		Run: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		},
		Shutdown: func(ctx context.Context) error {
			deadline, found := ctx.Deadline()
			if !found {
				return errors.New("gateway-in: drain deadline is required")
			}
			return registry.Drain(ctx, deadline, contractv1.DrainReason_DRAIN_REASON_SHUTDOWN)
		},
	}
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

func closeListener(listener net.Listener) error {
	if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		return fmt.Errorf("closing listener: %w", err)
	}
	return nil
}
