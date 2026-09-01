package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"

	"connectrpc.com/connect"
	"github.com/v0hmly/marketmesh/api/gen/go/auth/v1/authv1connect"
	"github.com/v0hmly/marketmesh/platform/httpserver"
	"github.com/v0hmly/marketmesh/platform/logger"
	platformpostgres "github.com/v0hmly/marketmesh/platform/postgres"
	serviceruntime "github.com/v0hmly/marketmesh/platform/runtime"
	"github.com/v0hmly/marketmesh/platform/telemetry"
	connectadapter "github.com/v0hmly/marketmesh/services/auth/internal/adapter/in/connectrpc"
	"github.com/v0hmly/marketmesh/services/auth/internal/adapter/out/argon2id"
	"github.com/v0hmly/marketmesh/services/auth/internal/adapter/out/audit"
	postgresadapter "github.com/v0hmly/marketmesh/services/auth/internal/adapter/out/postgres"
	"github.com/v0hmly/marketmesh/services/auth/internal/adapter/out/randomid"
	"github.com/v0hmly/marketmesh/services/auth/internal/application/login"
	"github.com/v0hmly/marketmesh/services/auth/internal/application/register"
)

// Run loads configuration, composes Auth dependencies, and owns the service lifecycle.
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
	config, err := loadConfig(dependencies.env)
	if err != nil {
		bootstrapLog.Error("не удалось загрузить конфигурацию", logger.Err(err))
		return fmt.Errorf("loading configuration: %w", err)
	}
	log, err := logger.New(logger.Config{
		Service:     serviceName,
		Version:     config.serviceVersion,
		Environment: config.environment,
		Level:       config.logLevel,
		Output:      dependencies.stdout,
		Console:     config.logConsole,
		MaskFields: []string{
			"authorization", "cookie", "identifier", "email", "password", "password_digest",
			"salt", "subject_id", "token", "payload",
		},
	})
	if err != nil {
		bootstrapLog.Error("не удалось создать logger", logger.Err(err))
		return fmt.Errorf("creating logger: %w", err)
	}

	err = runService(ctx, config, log, dependencies.listen)
	if err != nil {
		log.ErrorContext(context.WithoutCancel(ctx), "auth service завершился с ошибкой", logger.Err(err))
	}
	return err
}

func runService(ctx context.Context, config config, log *logger.Logger, listen listenFunc) (resultErr error) {
	pipeline, err := newTelemetry(ctx, config)
	if err != nil {
		return fmt.Errorf("creating telemetry: %w", err)
	}
	pipelineOwned := true
	defer func() {
		if pipelineOwned {
			shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), config.shutdownTimeout)
			defer cancel()
			resultErr = errors.Join(resultErr, pipeline.Shutdown(shutdownCtx))
		}
	}()
	hasher, err := argon2id.New(config.argon2)
	if err != nil {
		return fmt.Errorf("creating password hasher: %w", err)
	}

	database, err := platformpostgres.New(ctx, postgresConfig(config), pipeline)
	if err != nil {
		return fmt.Errorf("creating PostgreSQL: %w", err)
	}
	databaseOwned := true
	defer func() {
		if databaseOwned {
			shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), config.shutdownTimeout)
			defer cancel()
			resultErr = errors.Join(resultErr, database.Close(shutdownCtx))
		}
	}()

	repository, err := postgresadapter.New(database.RW())
	if err != nil {
		return fmt.Errorf("creating credential repository: %w", err)
	}
	recorder, err := audit.New(log, pipeline.Meter("github.com/v0hmly/marketmesh/services/auth"))
	if err != nil {
		return fmt.Errorf("creating audit recorder: %w", err)
	}
	registration, err := register.New(repository, hasher, randomid.New())
	if err != nil {
		return fmt.Errorf("creating registration use case: %w", err)
	}
	verification, err := login.New(repository, repository, hasher, recorder)
	if err != nil {
		return fmt.Errorf("creating login use case: %w", err)
	}
	connectHandler, err := connectadapter.New(registration, verification, log)
	if err != nil {
		return fmt.Errorf("creating Connect handler: %w", err)
	}
	interceptor, err := pipeline.PublicConnectInterceptor()
	if err != nil {
		return fmt.Errorf("creating Connect telemetry interceptor: %w", err)
	}

	health, err := serviceruntime.NewHealth(serviceruntime.HealthConfig{
		CheckTimeout: config.healthCheckTimeout,
		Dependencies: database.ReadinessDependencies(),
	})
	if err != nil {
		return fmt.Errorf("creating health checks: %w", err)
	}
	healthHandler, err := httpserver.NewHealthHandler(health)
	if err != nil {
		return fmt.Errorf("creating HTTP health handler: %w", err)
	}
	mux := http.NewServeMux()
	mux.Handle("/", healthHandler)
	path, rpcHandler := authv1connect.NewAuthServiceHandler(
		connectHandler,
		connect.WithInterceptors(interceptor),
	)
	mux.Handle(path, rpcHandler)
	httpServer, err := httpserver.New(httpserver.Config{
		Handler:           mux,
		ReadHeaderTimeout: config.httpReadHeaderTimeout,
		ReadTimeout:       config.httpReadTimeout,
		WriteTimeout:      config.httpWriteTimeout,
		IdleTimeout:       config.httpIdleTimeout,
		RequestTimeout:    config.httpRequestTimeout,
		MaxHeaderBytes:    config.httpMaxHeaderBytes,
		MaxBodyBytes:      config.httpMaxBodyBytes,
		Logger:            log,
		Telemetry:         pipeline,
	})
	if err != nil {
		return fmt.Errorf("creating HTTP server: %w", err)
	}
	listener, err := listen("tcp", config.httpAddress)
	if err != nil {
		return fmt.Errorf("listening for HTTP: %w", err)
	}
	listenerOwned := true
	defer func() {
		if listenerOwned {
			if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
				resultErr = errors.Join(resultErr, fmt.Errorf("closing HTTP listener: %w", err))
			}
		}
	}()

	databaseComponent, err := database.Component("postgres")
	if err != nil {
		return fmt.Errorf("creating PostgreSQL component: %w", err)
	}
	telemetryComponent := serviceruntime.Component{
		Name: "telemetry",
		Run: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		},
		Shutdown: pipeline.Shutdown,
	}
	httpComponent, err := httpserver.Component("http", httpServer, listener)
	if err != nil {
		return fmt.Errorf("creating HTTP component: %w", err)
	}
	runner, err := serviceruntime.NewRunner(
		serviceruntime.RunnerConfig{ShutdownTimeout: config.shutdownTimeout, Health: health},
		telemetryComponent,
		databaseComponent,
		httpComponent,
	)
	if err != nil {
		return fmt.Errorf("creating service runner: %w", err)
	}

	pipelineOwned = false
	databaseOwned = false
	listenerOwned = false
	log.Info("auth service запущен", logger.String("http_address", listener.Addr().String()))
	if err := runner.Run(ctx); err != nil {
		return err
	}
	log.Info("auth service остановлен")
	return nil
}

func postgresConfig(config config) platformpostgres.Config {
	pool := func(dsn serviceruntime.Secret) platformpostgres.PoolConfig {
		return platformpostgres.PoolConfig{
			DSN:                   dsn,
			MaxConns:              config.postgresMaxConns,
			MinConns:              config.postgresMinConns,
			MinIdleConns:          config.postgresMinIdleConns,
			ConnectTimeout:        config.postgresConnectTimeout,
			QueryTimeout:          config.postgresQueryTimeout,
			MaxConnLifetime:       config.postgresMaxConnLifetime,
			MaxConnLifetimeJitter: config.postgresMaxConnLifetimeJitter,
			MaxConnIdleTime:       config.postgresMaxConnIdleTime,
			HealthCheckPeriod:     config.postgresHealthCheckPeriod,
			PingTimeout:           config.postgresPingTimeout,
		}
	}
	return platformpostgres.Config{
		ApplicationName: serviceName + "/" + config.instanceID,
		RW:              pool(config.postgresRWDSN),
		RO:              pool(config.postgresRODSN),
	}
}

func newTelemetry(ctx context.Context, config config) (*telemetry.Telemetry, error) {
	if config.telemetryEndpoint == "" {
		return telemetry.NewNoop(), nil
	}
	headers := map[string]string{}
	if config.telemetryAuthorization.Present() {
		headers["authorization"] = "Bearer " + config.telemetryAuthorization.Reveal()
	}
	ratio := config.telemetryTraceRatio
	return telemetry.New(ctx, telemetry.Config{
		ServiceName: serviceName, ServiceVersion: config.serviceVersion, Environment: config.environment,
		InstanceID: config.instanceID, Endpoint: config.telemetryEndpoint, Insecure: config.telemetryInsecure,
		Headers: headers, TraceSampleRatio: &ratio, ExportTimeout: config.telemetryExportTimeout,
		MetricExportInterval: config.metricExportInterval,
	})
}
