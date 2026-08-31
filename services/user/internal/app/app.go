package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"

	"github.com/v0hmly/marketmesh/platform/logger"
	serviceruntime "github.com/v0hmly/marketmesh/platform/runtime"
	"github.com/v0hmly/marketmesh/platform/telemetry"
)

// Run загружает конфигурацию, вручную собирает зависимости и выполняет
// жизненный цикл user service. Возвращаемая ошибка уже журналирована ровно
// один раз и предназначена только для выбора process exit code.
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
		Service:     serviceName,
		Version:     "unknown",
		Environment: "bootstrap",
		Output:      dependencies.stderr,
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
			"authorization",
			"cookie",
			"password",
			"token",
		},
	})
	if err != nil {
		bootstrapLog.Error("не удалось создать logger", logger.Err(err))
		return fmt.Errorf("creating logger: %w", err)
	}

	err = runService(ctx, config, log, dependencies.listen)
	if err != nil {
		log.ErrorContext(
			context.WithoutCancel(ctx),
			"user service завершился с ошибкой",
			logger.Err(err),
		)
	}

	return err
}

func runService(
	ctx context.Context,
	config config,
	log *logger.Logger,
	listen listenFunc,
) (resultErr error) {
	pipeline, err := newTelemetry(ctx, config)
	if err != nil {
		return fmt.Errorf("creating telemetry: %w", err)
	}
	pipelineOwned := true
	defer func() {
		if !pipelineOwned {
			return
		}

		shutdownCtx, cancel := context.WithTimeout(
			context.WithoutCancel(ctx),
			config.shutdownTimeout,
		)
		defer cancel()
		if err := pipeline.Shutdown(shutdownCtx); err != nil {
			resultErr = errors.Join(resultErr, fmt.Errorf("stopping telemetry after startup failure: %w", err))
		}
	}()

	health, err := serviceruntime.NewHealth(serviceruntime.HealthConfig{
		CheckTimeout: config.healthCheckTimeout,
	})
	if err != nil {
		return fmt.Errorf("creating health checks: %w", err)
	}

	httpServer := newHTTPServer(config, log, newHealthHandler(health))

	listener, err := listen("tcp", config.httpAddress)
	if err != nil {
		return fmt.Errorf("listening for HTTP: %w", err)
	}
	listenerOwned := true
	defer func() {
		if !listenerOwned {
			return
		}
		if err := listener.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
			resultErr = errors.Join(resultErr, fmt.Errorf("closing HTTP listener: %w", err))
		}
	}()

	httpComponent := newHTTPComponent(httpServer, listener)
	telemetryComponent := serviceruntime.Component{
		Name: "telemetry",
		Run: func(ctx context.Context) error {
			<-ctx.Done()
			return ctx.Err()
		},
		Shutdown: pipeline.Shutdown,
	}

	runner, err := serviceruntime.NewRunner(
		serviceruntime.RunnerConfig{
			ShutdownTimeout: config.shutdownTimeout,
			Health:          health,
		},
		telemetryComponent,
		httpComponent,
	)
	if err != nil {
		return fmt.Errorf("creating service runner: %w", err)
	}

	pipelineOwned = false
	listenerOwned = false
	log.Info(
		"user service запущен",
		logger.String("http_address", listener.Addr().String()),
	)
	if err := runner.Run(ctx); err != nil {
		return err
	}
	log.Info("user service остановлен")

	return nil
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
		ServiceName:          serviceName,
		ServiceVersion:       config.serviceVersion,
		Environment:          config.environment,
		InstanceID:           config.instanceID,
		Endpoint:             config.telemetryEndpoint,
		Insecure:             config.telemetryInsecure,
		Headers:              headers,
		TraceSampleRatio:     &ratio,
		ExportTimeout:        config.telemetryExportTimeout,
		MetricExportInterval: config.metricExportInterval,
	})
}
