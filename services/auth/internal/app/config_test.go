package app

import (
	"strings"
	"testing"
	"time"

	serviceruntime "github.com/v0hmly/marketmesh/platform/runtime"
)

func TestLoadConfigUsesBoundedDefaultsAndSecrets(t *testing.T) {
	t.Parallel()

	config, err := loadConfig(serviceruntime.MapEnv(requiredEnvironment()))
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if config.httpAddress != defaultHTTPAddress || config.httpMaxBodyBytes != defaultHTTPMaxBodyBytes {
		t.Fatalf("HTTP defaults = %q, %d", config.httpAddress, config.httpMaxBodyBytes)
	}
	if config.postgresMaxConns != defaultPostgresMaxConns || config.postgresQueryTimeout != defaultPostgresQueryTimeout {
		t.Fatalf("PostgreSQL defaults = max %d, timeout %v", config.postgresMaxConns, config.postgresQueryTimeout)
	}
	if !config.postgresRWDSN.Present() || !config.postgresRODSN.Present() {
		t.Fatal("PostgreSQL DSNs were not represented as secrets")
	}
}

func TestLoadConfigErrorsNeverContainSecretValues(t *testing.T) {
	t.Parallel()

	values := requiredEnvironment()
	secret := "postgres://auth:do-not-leak@example.invalid/auth"
	values["POSTGRES_RW_DSN"] = secret
	values["POSTGRES_MAX_CONNS"] = "not-a-number"
	_, err := loadConfig(serviceruntime.MapEnv(values))
	if err == nil {
		t.Fatal("loadConfig() error = nil")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), "do-not-leak") {
		t.Fatalf("loadConfig() error contains secret: %v", err)
	}
}

func TestLoadConfigRejectsUnboundedPoolsAndArgonIntegerRanges(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "negative minimum", key: "POSTGRES_MIN_CONNS", value: "-1"},
		{name: "pool overflow", key: "POSTGRES_MAX_CONNS", value: "2147483648"},
		{name: "argon parallelism overflow", key: "ARGON2_PARALLELISM", value: "256"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			values := requiredEnvironment()
			values[test.key] = test.value
			if _, err := loadConfig(serviceruntime.MapEnv(values)); err == nil {
				t.Fatal("loadConfig() error = nil")
			}
		})
	}
}

func TestLoadConfigRejectsInvalidTypedValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "console", key: "LOG_CONSOLE", value: "sometimes"},
		{name: "header timeout", key: "HTTP_READ_HEADER_TIMEOUT", value: "0s"},
		{name: "read timeout", key: "HTTP_READ_TIMEOUT", value: "invalid"},
		{name: "write timeout", key: "HTTP_WRITE_TIMEOUT", value: "-1s"},
		{name: "idle timeout", key: "HTTP_IDLE_TIMEOUT", value: "0s"},
		{name: "request timeout", key: "HTTP_REQUEST_TIMEOUT", value: "0s"},
		{name: "header bytes", key: "HTTP_MAX_HEADER_BYTES", value: "0"},
		{name: "body bytes", key: "HTTP_MAX_BODY_BYTES", value: "invalid"},
		{name: "health timeout", key: "HEALTH_CHECK_TIMEOUT", value: "0s"},
		{name: "shutdown timeout", key: "SHUTDOWN_TIMEOUT", value: "0s"},
		{name: "otel insecure", key: "OTEL_INSECURE", value: "maybe"},
		{name: "trace ratio", key: "OTEL_TRACE_SAMPLE_RATIO", value: "2"},
		{name: "export timeout", key: "OTEL_EXPORT_TIMEOUT", value: "0s"},
		{name: "metric interval", key: "OTEL_METRIC_EXPORT_INTERVAL", value: "0s"},
		{name: "connect timeout", key: "POSTGRES_CONNECT_TIMEOUT", value: "0s"},
		{name: "query timeout", key: "POSTGRES_QUERY_TIMEOUT", value: "0s"},
		{name: "lifetime", key: "POSTGRES_MAX_CONN_LIFETIME", value: "0s"},
		{name: "jitter", key: "POSTGRES_MAX_CONN_LIFETIME_JITTER", value: "-1s"},
		{name: "idle", key: "POSTGRES_MAX_CONN_IDLE_TIME", value: "0s"},
		{name: "health period", key: "POSTGRES_HEALTH_CHECK_PERIOD", value: "0s"},
		{name: "ping timeout", key: "POSTGRES_PING_TIMEOUT", value: "0s"},
		{name: "argon memory", key: "ARGON2_MEMORY_KIB", value: "0"},
		{name: "argon time", key: "ARGON2_TIME", value: "0"},
		{name: "argon salt", key: "ARGON2_SALT_BYTES", value: "0"},
		{name: "argon key", key: "ARGON2_KEY_BYTES", value: "0"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			values := requiredEnvironment()
			values[test.key] = test.value
			if _, err := loadConfig(serviceruntime.MapEnv(values)); err == nil {
				t.Fatal("loadConfig() error = nil")
			}
		})
	}
}

func TestLoadConfigAcceptsOverrides(t *testing.T) {
	t.Parallel()

	values := requiredEnvironment()
	values["HTTP_ADDRESS"] = "127.0.0.1:9999"
	values["HTTP_READ_HEADER_TIMEOUT"] = "1s"
	values["HTTP_READ_TIMEOUT"] = "2s"
	values["HTTP_WRITE_TIMEOUT"] = "3s"
	values["HTTP_IDLE_TIMEOUT"] = "4s"
	values["HTTP_REQUEST_TIMEOUT"] = "5s"
	values["POSTGRES_MAX_CONNS"] = "7"
	values["POSTGRES_MIN_CONNS"] = "1"
	values["POSTGRES_MIN_IDLE_CONNS"] = "1"
	values["OTEL_TRACE_SAMPLE_RATIO"] = "0.25"
	loaded, err := loadConfig(serviceruntime.MapEnv(values))
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if loaded.httpAddress != "127.0.0.1:9999" || loaded.httpReadHeaderTimeout != time.Second ||
		loaded.httpReadTimeout != 2*time.Second || loaded.httpWriteTimeout != 3*time.Second ||
		loaded.httpIdleTimeout != 4*time.Second || loaded.httpRequestTimeout != 5*time.Second || loaded.postgresMaxConns != 7 || loaded.postgresMinConns != 1 ||
		loaded.postgresMinIdleConns != 1 || loaded.telemetryTraceRatio != 0.25 {
		t.Fatalf("loadConfig() overrides = %+v", loaded)
	}
}

func requiredEnvironment() map[string]string {
	return map[string]string{
		"SERVICE_VERSION":     "test",
		"ENVIRONMENT":         "test",
		"SERVICE_INSTANCE_ID": "instance-1",
		"POSTGRES_RW_DSN":     "postgres://auth-rw@example.invalid/auth",
		"POSTGRES_RO_DSN":     "postgres://auth-ro@example.invalid/auth",
	}
}
