package fakeapp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"

	e2ev1 "github.com/v0hmly/marketmesh/api/gen/go/e2e/v1"
	"github.com/v0hmly/marketmesh/e2e/tunnel/internal/fakeinternal"
	platformgrpc "github.com/v0hmly/marketmesh/platform/grpc"
	"github.com/v0hmly/marketmesh/platform/httpserver"
	"github.com/v0hmly/marketmesh/platform/logger"
	serviceruntime "github.com/v0hmly/marketmesh/platform/runtime"
	"github.com/v0hmly/marketmesh/platform/telemetry"
)

// Run loads configuration and owns the fake internal workload lifecycle.
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
		Service:     serviceName,
		Version:     cfg.serviceVersion,
		Environment: cfg.environment,
		Level:       cfg.logLevel,
		Output:      dependencies.stdout,
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
		log.ErrorContext(context.WithoutCancel(ctx), "fake internal завершился с ошибкой", logger.Err(err))
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
	authorizer, err := peerAuthorizer(cfg.expectedClientURI)
	if err != nil {
		return err
	}
	service, err := fakeinternal.New(fakeinternal.Config{
		InstanceID: cfg.instanceID, MaxLedgerEntries: cfg.maxLedgerEntries,
	})
	if err != nil {
		return fmt.Errorf("creating fake internal service: %w", err)
	}
	pipeline := telemetry.NewNoop()
	server, err := platformgrpc.NewServer(platformgrpc.ServerConfig{
		Environment:            cfg.environment,
		ConnectionTimeout:      defaultConnectionWait,
		RequestTimeout:         cfg.requestTimeout,
		KeepaliveTime:          defaultKeepaliveTime,
		KeepaliveTimeout:       defaultKeepaliveTimeout,
		MaxReceiveMessageBytes: defaultMessageBytes,
		MaxSendMessageBytes:    defaultMessageBytes,
		Security: platformgrpc.ServerSecurity{
			TLSConfig: tlsConfig, RequireClientCertificate: true,
		},
		Logger:              log,
		Telemetry:           pipeline,
		UnaryAuthentication: authorizer,
	})
	if err != nil {
		return fmt.Errorf("creating gRPC server: %w", err)
	}
	e2ev1.RegisterFakeInternalServiceServer(server.GRPCServer(), service)
	health, err := serviceruntime.NewHealth(serviceruntime.HealthConfig{
		CheckTimeout: defaultRequestTimeout,
	})
	if err != nil {
		return fmt.Errorf("creating health checks: %w", err)
	}
	healthHandler, err := fakeHealthHandler(health)
	if err != nil {
		return err
	}
	httpServer, err := httpserver.New(httpserver.Config{
		Handler: healthHandler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second,
		WriteTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second,
		RequestTimeout: defaultRequestTimeout, MaxHeaderBytes: 8 * 1024,
		MaxBodyBytes: 1024, Logger: log, Telemetry: pipeline,
	})
	if err != nil {
		return fmt.Errorf("creating health HTTP server: %w", err)
	}
	listener, err := listen("tcp", cfg.grpcAddress)
	if err != nil {
		return fmt.Errorf("listening for gRPC: %w", err)
	}
	httpListener, err := listen("tcp", cfg.httpAddress)
	if err != nil {
		_ = listener.Close()
		return fmt.Errorf("listening for health HTTP: %w", err)
	}
	listenerOwned := true
	defer func() {
		if listenerOwned {
			resultErr = errors.Join(
				resultErr,
				wrapServeError("closing gRPC listener", listener.Close()),
				wrapServeError("closing HTTP listener", httpListener.Close()),
			)
		}
	}()

	grpcComponent, err := server.Component("grpc", listener)
	if err != nil {
		return fmt.Errorf("creating gRPC component: %w", err)
	}
	httpComponent, err := httpserver.Component("health-http", httpServer, httpListener)
	if err != nil {
		return fmt.Errorf("creating health HTTP component: %w", err)
	}
	telemetryComponent := serviceruntime.Component{
		Name: "telemetry",
		Run: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		},
		Shutdown: pipeline.Shutdown,
	}
	runner, err := serviceruntime.NewRunner(
		serviceruntime.RunnerConfig{ShutdownTimeout: cfg.shutdownTimeout, Health: health},
		telemetryComponent,
		grpcComponent,
		httpComponent,
	)
	if err != nil {
		return fmt.Errorf("creating runner: %w", err)
	}

	listenerOwned = false
	log.Info(
		"fake internal запущен",
		logger.String("grpc_address", listener.Addr().String()),
		logger.String("http_address", httpListener.Addr().String()),
	)
	if err := runner.Run(ctx); err != nil {
		return err
	}
	log.Info("fake internal остановлен", logger.Duration("shutdown_budget", cfg.shutdownTimeout))

	return nil
}

func fakeHealthHandler(health *serviceruntime.Health) (http.Handler, error) {
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
