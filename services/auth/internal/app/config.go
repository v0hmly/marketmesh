package app

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	serviceruntime "github.com/v0hmly/marketmesh/platform/runtime"
	"github.com/v0hmly/marketmesh/services/auth/internal/adapter/out/argon2id"
)

const (
	serviceName = "auth"

	defaultHTTPAddress           = "127.0.0.1:8081"
	defaultHTTPReadHeaderTimeout = 5 * time.Second
	defaultHTTPReadTimeout       = 15 * time.Second
	defaultHTTPWriteTimeout      = 15 * time.Second
	defaultHTTPIdleTimeout       = 60 * time.Second
	defaultHTTPRequestTimeout    = 10 * time.Second
	defaultHTTPMaxHeaderBytes    = 64 * 1024
	defaultHTTPMaxBodyBytes      = int64(16 * 1024)
	defaultHealthCheckTimeout    = 2 * time.Second
	defaultShutdownTimeout       = 15 * time.Second
	defaultTelemetryExport       = 5 * time.Second
	defaultMetricExportInterval  = 30 * time.Second

	defaultPostgresMaxConns              = 10
	defaultPostgresMinConns              = 0
	defaultPostgresMinIdleConns          = 0
	defaultPostgresConnectTimeout        = 5 * time.Second
	defaultPostgresQueryTimeout          = 3 * time.Second
	defaultPostgresMaxConnLifetime       = 30 * time.Minute
	defaultPostgresMaxConnLifetimeJitter = 3 * time.Minute
	defaultPostgresMaxConnIdleTime       = 5 * time.Minute
	defaultPostgresHealthCheckPeriod     = 30 * time.Second
	defaultPostgresPingTimeout           = 2 * time.Second
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
	httpRequestTimeout    time.Duration
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

	postgresRWDSN                 serviceruntime.Secret
	postgresRODSN                 serviceruntime.Secret
	postgresMaxConns              int32
	postgresMinConns              int32
	postgresMinIdleConns          int32
	postgresConnectTimeout        time.Duration
	postgresQueryTimeout          time.Duration
	postgresMaxConnLifetime       time.Duration
	postgresMaxConnLifetimeJitter time.Duration
	postgresMaxConnIdleTime       time.Duration
	postgresHealthCheckPeriod     time.Duration
	postgresPingTimeout           time.Duration

	argon2 argon2id.Config
}

func loadConfig(env serviceruntime.Env) (config, error) {
	var result config
	var err error

	if result.serviceVersion, err = env.RequiredString("SERVICE_VERSION"); err != nil {
		return config{}, err
	}
	if result.environment, err = env.RequiredString("ENVIRONMENT"); err != nil {
		return config{}, err
	}
	if result.instanceID, err = env.RequiredString("SERVICE_INSTANCE_ID"); err != nil {
		return config{}, err
	}
	if result.logLevel, err = env.String("LOG_LEVEL", "info"); err != nil {
		return config{}, err
	}
	if result.logConsole, err = env.Bool("LOG_CONSOLE", false); err != nil {
		return config{}, err
	}
	if result.httpAddress, err = env.String("HTTP_ADDRESS", defaultHTTPAddress); err != nil {
		return config{}, err
	}
	if result.httpReadHeaderTimeout, err = env.PositiveDuration("HTTP_READ_HEADER_TIMEOUT", defaultHTTPReadHeaderTimeout); err != nil {
		return config{}, err
	}
	if result.httpReadTimeout, err = env.PositiveDuration("HTTP_READ_TIMEOUT", defaultHTTPReadTimeout); err != nil {
		return config{}, err
	}
	if result.httpWriteTimeout, err = env.PositiveDuration("HTTP_WRITE_TIMEOUT", defaultHTTPWriteTimeout); err != nil {
		return config{}, err
	}
	if result.httpIdleTimeout, err = env.PositiveDuration("HTTP_IDLE_TIMEOUT", defaultHTTPIdleTimeout); err != nil {
		return config{}, err
	}
	if result.httpRequestTimeout, err = env.PositiveDuration("HTTP_REQUEST_TIMEOUT", defaultHTTPRequestTimeout); err != nil {
		return config{}, err
	}
	if result.httpMaxHeaderBytes, err = env.PositiveInt("HTTP_MAX_HEADER_BYTES", defaultHTTPMaxHeaderBytes); err != nil {
		return config{}, err
	}
	if result.httpMaxBodyBytes, err = env.PositiveInt64("HTTP_MAX_BODY_BYTES", defaultHTTPMaxBodyBytes); err != nil {
		return config{}, err
	}
	if result.healthCheckTimeout, err = env.PositiveDuration("HEALTH_CHECK_TIMEOUT", defaultHealthCheckTimeout); err != nil {
		return config{}, err
	}
	if result.shutdownTimeout, err = env.PositiveDuration("SHUTDOWN_TIMEOUT", defaultShutdownTimeout); err != nil {
		return config{}, err
	}

	if result.telemetryEndpoint, err = env.String("OTEL_ENDPOINT", ""); err != nil {
		return config{}, err
	}
	if result.telemetryInsecure, err = env.Bool("OTEL_INSECURE", false); err != nil {
		return config{}, err
	}
	if result.telemetryTraceRatio, err = env.Float64("OTEL_TRACE_SAMPLE_RATIO", 1); err != nil {
		return config{}, err
	}
	if math.IsNaN(result.telemetryTraceRatio) || math.IsInf(result.telemetryTraceRatio, 0) || result.telemetryTraceRatio < 0 || result.telemetryTraceRatio > 1 {
		return config{}, errors.New("environment variable OTEL_TRACE_SAMPLE_RATIO must be between 0 and 1")
	}
	if result.telemetryExportTimeout, err = env.PositiveDuration("OTEL_EXPORT_TIMEOUT", defaultTelemetryExport); err != nil {
		return config{}, err
	}
	if result.metricExportInterval, err = env.PositiveDuration("OTEL_METRIC_EXPORT_INTERVAL", defaultMetricExportInterval); err != nil {
		return config{}, err
	}
	if result.telemetryAuthorization, err = env.Secret("OTEL_AUTH_TOKEN", false); err != nil {
		return config{}, fmt.Errorf("loading telemetry authorization: %w", err)
	}

	if result.postgresRWDSN, err = env.Secret("POSTGRES_RW_DSN", true); err != nil {
		return config{}, fmt.Errorf("loading PostgreSQL RW DSN: %w", err)
	}
	if result.postgresRODSN, err = env.Secret("POSTGRES_RO_DSN", true); err != nil {
		return config{}, fmt.Errorf("loading PostgreSQL RO DSN: %w", err)
	}
	if result.postgresMaxConns, err = positiveInt32(env, "POSTGRES_MAX_CONNS", defaultPostgresMaxConns); err != nil {
		return config{}, err
	}
	if result.postgresMinConns, err = nonNegativeInt32(env, "POSTGRES_MIN_CONNS", defaultPostgresMinConns); err != nil {
		return config{}, err
	}
	if result.postgresMinIdleConns, err = nonNegativeInt32(env, "POSTGRES_MIN_IDLE_CONNS", defaultPostgresMinIdleConns); err != nil {
		return config{}, err
	}
	if result.postgresConnectTimeout, err = env.PositiveDuration("POSTGRES_CONNECT_TIMEOUT", defaultPostgresConnectTimeout); err != nil {
		return config{}, err
	}
	if result.postgresQueryTimeout, err = env.PositiveDuration("POSTGRES_QUERY_TIMEOUT", defaultPostgresQueryTimeout); err != nil {
		return config{}, err
	}
	if result.postgresMaxConnLifetime, err = env.PositiveDuration("POSTGRES_MAX_CONN_LIFETIME", defaultPostgresMaxConnLifetime); err != nil {
		return config{}, err
	}
	if result.postgresMaxConnLifetimeJitter, err = env.Duration("POSTGRES_MAX_CONN_LIFETIME_JITTER", defaultPostgresMaxConnLifetimeJitter); err != nil || result.postgresMaxConnLifetimeJitter < 0 {
		if err != nil {
			return config{}, err
		}
		return config{}, errors.New("environment variable POSTGRES_MAX_CONN_LIFETIME_JITTER must not be negative")
	}
	if result.postgresMaxConnIdleTime, err = env.PositiveDuration("POSTGRES_MAX_CONN_IDLE_TIME", defaultPostgresMaxConnIdleTime); err != nil {
		return config{}, err
	}
	if result.postgresHealthCheckPeriod, err = env.PositiveDuration("POSTGRES_HEALTH_CHECK_PERIOD", defaultPostgresHealthCheckPeriod); err != nil {
		return config{}, err
	}
	if result.postgresPingTimeout, err = env.PositiveDuration("POSTGRES_PING_TIMEOUT", defaultPostgresPingTimeout); err != nil {
		return config{}, err
	}

	defaults := argon2id.DefaultConfig()
	memory, err := positiveUint32(env, "ARGON2_MEMORY_KIB", defaults.Memory)
	if err != nil {
		return config{}, err
	}
	timeCost, err := positiveUint32(env, "ARGON2_TIME", defaults.Time)
	if err != nil {
		return config{}, err
	}
	parallelism, err := positiveUint8(env, "ARGON2_PARALLELISM", defaults.Parallelism)
	if err != nil {
		return config{}, err
	}
	saltLength, err := positiveUint32(env, "ARGON2_SALT_BYTES", defaults.SaltLength)
	if err != nil {
		return config{}, err
	}
	keyLength, err := positiveUint32(env, "ARGON2_KEY_BYTES", defaults.KeyLength)
	if err != nil {
		return config{}, err
	}
	result.argon2 = argon2id.Config{Memory: memory, Time: timeCost, Parallelism: parallelism, SaltLength: saltLength, KeyLength: keyLength}

	return result, nil
}

func positiveInt32(env serviceruntime.Env, name string, fallback int) (int32, error) {
	raw, err := env.String(name, strconv.Itoa(fallback))
	if err != nil {
		return 0, err
	}
	var value int32
	count, scanErr := fmt.Sscanf(raw, "%d", &value)
	if scanErr != nil || count != 1 || value <= 0 || strconv.FormatInt(int64(value), 10) != raw {
		return 0, fmt.Errorf("environment variable %s must be a positive 32-bit integer", name)
	}
	return value, nil
}

func nonNegativeInt32(env serviceruntime.Env, name string, fallback int) (int32, error) {
	raw, err := env.String(name, strconv.Itoa(fallback))
	if err != nil {
		return 0, err
	}
	var value int32
	count, scanErr := fmt.Sscanf(raw, "%d", &value)
	if scanErr != nil || count != 1 || strconv.FormatInt(int64(value), 10) != raw {
		return 0, fmt.Errorf("environment variable %s must be a non-negative integer", name)
	}
	if value < 0 {
		return 0, fmt.Errorf("environment variable %s must be between zero and %d", name, math.MaxInt32)
	}
	return value, nil
}

func positiveUint32(env serviceruntime.Env, name string, fallback uint32) (uint32, error) {
	raw, err := env.String(name, strconv.FormatUint(uint64(fallback), 10))
	if err != nil {
		return 0, err
	}
	var value uint32
	count, scanErr := fmt.Sscanf(raw, "%d", &value)
	if scanErr != nil || count != 1 || value == 0 || strconv.FormatUint(uint64(value), 10) != raw {
		return 0, fmt.Errorf("environment variable %s must be a positive 32-bit integer", name)
	}
	return value, nil
}

func positiveUint8(env serviceruntime.Env, name string, fallback uint8) (uint8, error) {
	raw, err := env.String(name, strconv.FormatUint(uint64(fallback), 10))
	if err != nil {
		return 0, err
	}
	var value uint8
	count, scanErr := fmt.Sscanf(raw, "%d", &value)
	if scanErr != nil || count != 1 || value == 0 || strconv.FormatUint(uint64(value), 10) != raw {
		return 0, fmt.Errorf("environment variable %s must be a positive 8-bit integer", name)
	}
	return value, nil
}
