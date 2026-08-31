package app

import (
	"errors"
	"fmt"
	"math"
	"time"

	serviceruntime "github.com/v0hmly/marketmesh/platform/runtime"
)

const (
	serviceName = "user"

	defaultHTTPAddress           = "127.0.0.1:8080"
	defaultHTTPReadHeaderTimeout = 5 * time.Second
	defaultHTTPReadTimeout       = 15 * time.Second
	defaultHTTPWriteTimeout      = 15 * time.Second
	defaultHTTPIdleTimeout       = 60 * time.Second
	defaultHTTPMaxHeaderBytes    = 64 * 1024
	defaultHTTPMaxBodyBytes      = int64(1024 * 1024)
	defaultHealthCheckTimeout    = 2 * time.Second
	defaultShutdownTimeout       = 15 * time.Second
	defaultTelemetryExport       = 5 * time.Second
	defaultMetricExportInterval  = 30 * time.Second
)

type config struct {
	serviceVersion string
	environment    string
	instanceID     string

	logLevel   string
	logConsole bool

	httpAddress           string
	httpReadHeaderTimeout time.Duration
	httpReadTimeout       time.Duration
	httpWriteTimeout      time.Duration
	httpIdleTimeout       time.Duration
	httpMaxHeaderBytes    int
	httpMaxBodyBytes      int64

	healthCheckTimeout time.Duration
	shutdownTimeout    time.Duration

	telemetryEndpoint      string
	telemetryInsecure      bool
	telemetryTraceRatio    float64
	telemetryExportTimeout time.Duration
	metricExportInterval   time.Duration
	telemetryAuthorization serviceruntime.Secret
}

func loadConfig(env serviceruntime.Env) (config, error) {
	var result config
	var err error

	result.serviceVersion, err = env.RequiredString("SERVICE_VERSION")
	if err != nil {
		return config{}, err
	}
	result.environment, err = env.RequiredString("ENVIRONMENT")
	if err != nil {
		return config{}, err
	}
	result.instanceID, err = env.RequiredString("SERVICE_INSTANCE_ID")
	if err != nil {
		return config{}, err
	}

	result.logLevel, err = env.String("LOG_LEVEL", "info")
	if err != nil {
		return config{}, err
	}
	result.logConsole, err = env.Bool("LOG_CONSOLE", false)
	if err != nil {
		return config{}, err
	}

	result.httpAddress, err = env.String("HTTP_ADDRESS", defaultHTTPAddress)
	if err != nil {
		return config{}, err
	}
	result.httpReadHeaderTimeout, err = env.PositiveDuration(
		"HTTP_READ_HEADER_TIMEOUT",
		defaultHTTPReadHeaderTimeout,
	)
	if err != nil {
		return config{}, err
	}
	result.httpReadTimeout, err = env.PositiveDuration(
		"HTTP_READ_TIMEOUT",
		defaultHTTPReadTimeout,
	)
	if err != nil {
		return config{}, err
	}
	result.httpWriteTimeout, err = env.PositiveDuration(
		"HTTP_WRITE_TIMEOUT",
		defaultHTTPWriteTimeout,
	)
	if err != nil {
		return config{}, err
	}
	result.httpIdleTimeout, err = env.PositiveDuration(
		"HTTP_IDLE_TIMEOUT",
		defaultHTTPIdleTimeout,
	)
	if err != nil {
		return config{}, err
	}
	result.httpMaxHeaderBytes, err = env.PositiveInt(
		"HTTP_MAX_HEADER_BYTES",
		defaultHTTPMaxHeaderBytes,
	)
	if err != nil {
		return config{}, err
	}
	result.httpMaxBodyBytes, err = env.PositiveInt64(
		"HTTP_MAX_BODY_BYTES",
		defaultHTTPMaxBodyBytes,
	)
	if err != nil {
		return config{}, err
	}

	result.healthCheckTimeout, err = env.PositiveDuration(
		"HEALTH_CHECK_TIMEOUT",
		defaultHealthCheckTimeout,
	)
	if err != nil {
		return config{}, err
	}
	result.shutdownTimeout, err = env.PositiveDuration(
		"SHUTDOWN_TIMEOUT",
		defaultShutdownTimeout,
	)
	if err != nil {
		return config{}, err
	}

	result.telemetryEndpoint, err = env.String("OTEL_ENDPOINT", "")
	if err != nil {
		return config{}, err
	}
	result.telemetryInsecure, err = env.Bool("OTEL_INSECURE", false)
	if err != nil {
		return config{}, err
	}
	result.telemetryTraceRatio, err = env.Float64("OTEL_TRACE_SAMPLE_RATIO", 1)
	if err != nil {
		return config{}, err
	}
	if math.IsNaN(result.telemetryTraceRatio) ||
		math.IsInf(result.telemetryTraceRatio, 0) ||
		result.telemetryTraceRatio < 0 ||
		result.telemetryTraceRatio > 1 {
		return config{}, errors.New(
			"environment variable OTEL_TRACE_SAMPLE_RATIO must be between 0 and 1",
		)
	}
	result.telemetryExportTimeout, err = env.PositiveDuration(
		"OTEL_EXPORT_TIMEOUT",
		defaultTelemetryExport,
	)
	if err != nil {
		return config{}, err
	}
	result.metricExportInterval, err = env.PositiveDuration(
		"OTEL_METRIC_EXPORT_INTERVAL",
		defaultMetricExportInterval,
	)
	if err != nil {
		return config{}, err
	}
	result.telemetryAuthorization, err = env.Secret("OTEL_AUTH_TOKEN", false)
	if err != nil {
		return config{}, fmt.Errorf("loading telemetry authorization: %w", err)
	}

	return result, nil
}
