package app

import (
	"strings"
	"testing"
	"time"

	serviceruntime "github.com/v0hmly/marketmesh/platform/runtime"
)

func TestLoadConfigFromIsolatedEnvironment(t *testing.T) {
	t.Parallel()

	values := validEnvironment()
	values["OTEL_AUTH_TOKEN"] = "collector-secret"
	loaded, err := loadConfig(serviceruntime.MapEnv(values))
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}

	if loaded.serviceVersion != "1.2.3" {
		t.Errorf("serviceVersion = %q, want 1.2.3", loaded.serviceVersion)
	}
	if loaded.environment != "test" {
		t.Errorf("environment = %q, want test", loaded.environment)
	}
	if loaded.instanceID != "user-test-1" {
		t.Errorf("instanceID = %q, want user-test-1", loaded.instanceID)
	}
	if loaded.httpAddress != defaultHTTPAddress {
		t.Errorf("httpAddress = %q, want %q", loaded.httpAddress, defaultHTTPAddress)
	}
	if loaded.shutdownTimeout != defaultShutdownTimeout {
		t.Errorf("shutdownTimeout = %v, want %v", loaded.shutdownTimeout, defaultShutdownTimeout)
	}
	if loaded.telemetryAuthorization.Reveal() != "collector-secret" {
		t.Fatal("telemetry authorization was not loaded")
	}
}

func TestLoadConfigRequiresIdentityFields(t *testing.T) {
	t.Parallel()

	for _, name := range []string{"SERVICE_VERSION", "ENVIRONMENT", "SERVICE_INSTANCE_ID"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			values := validEnvironment()
			delete(values, name)
			_, err := loadConfig(serviceruntime.MapEnv(values))
			if err == nil {
				t.Fatal("loadConfig() error = nil, want required error")
			}
			if !strings.Contains(err.Error(), name) {
				t.Fatalf("loadConfig() error = %q, want variable name", err)
			}
		})
	}
}

func TestLoadConfigRejectsInvalidTypedValuesWithoutLeakingSecrets(t *testing.T) {
	t.Parallel()

	const secret = "collector-secret-that-must-not-leak"
	tests := []struct {
		name  string
		key   string
		value string
	}{
		{name: "log console", key: "LOG_CONSOLE", value: "sometimes"},
		{name: "read header timeout", key: "HTTP_READ_HEADER_TIMEOUT", value: "0s"},
		{name: "read timeout", key: "HTTP_READ_TIMEOUT", value: "invalid"},
		{name: "write timeout", key: "HTTP_WRITE_TIMEOUT", value: "-1s"},
		{name: "idle timeout", key: "HTTP_IDLE_TIMEOUT", value: "0s"},
		{name: "request timeout", key: "HTTP_REQUEST_TIMEOUT", value: "-1s"},
		{name: "header limit", key: "HTTP_MAX_HEADER_BYTES", value: "0"},
		{name: "body limit", key: "HTTP_MAX_BODY_BYTES", value: "not-a-number"},
		{name: "health timeout", key: "HEALTH_CHECK_TIMEOUT", value: "0s"},
		{name: "shutdown timeout", key: "SHUTDOWN_TIMEOUT", value: "-1s"},
		{name: "telemetry insecure", key: "OTEL_INSECURE", value: "perhaps"},
		{name: "trace ratio", key: "OTEL_TRACE_SAMPLE_RATIO", value: "2"},
		{name: "export timeout", key: "OTEL_EXPORT_TIMEOUT", value: "0s"},
		{name: "metric interval", key: "OTEL_METRIC_EXPORT_INTERVAL", value: "0s"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			values := validEnvironment()
			values["OTEL_AUTH_TOKEN"] = secret
			values[test.key] = test.value
			_, err := loadConfig(serviceruntime.MapEnv(values))
			if err == nil {
				t.Fatal("loadConfig() error = nil, want validation error")
			}
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("loadConfig() error %q contains secret", err)
			}
		})
	}
}

func TestLoadConfigAcceptsExplicitOverrides(t *testing.T) {
	t.Parallel()

	values := validEnvironment()
	values["HTTP_ADDRESS"] = "127.0.0.1:9090"
	values["HTTP_READ_HEADER_TIMEOUT"] = "1s"
	values["HTTP_READ_TIMEOUT"] = "2s"
	values["HTTP_WRITE_TIMEOUT"] = "3s"
	values["HTTP_IDLE_TIMEOUT"] = "4s"
	values["HTTP_REQUEST_TIMEOUT"] = "5s"
	values["HTTP_MAX_HEADER_BYTES"] = "2048"
	values["HTTP_MAX_BODY_BYTES"] = "4096"
	values["HEALTH_CHECK_TIMEOUT"] = "500ms"
	values["SHUTDOWN_TIMEOUT"] = "6s"
	values["OTEL_ENDPOINT"] = "collector.internal:4317"
	values["OTEL_INSECURE"] = "true"
	values["OTEL_TRACE_SAMPLE_RATIO"] = "0.25"
	values["OTEL_EXPORT_TIMEOUT"] = "7s"
	values["OTEL_METRIC_EXPORT_INTERVAL"] = "8s"

	loaded, err := loadConfig(serviceruntime.MapEnv(values))
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}

	if loaded.httpAddress != "127.0.0.1:9090" ||
		loaded.httpReadHeaderTimeout != time.Second ||
		loaded.httpReadTimeout != 2*time.Second ||
		loaded.httpWriteTimeout != 3*time.Second ||
		loaded.httpIdleTimeout != 4*time.Second ||
		loaded.httpRequestTimeout != 5*time.Second ||
		loaded.httpMaxHeaderBytes != 2048 ||
		loaded.httpMaxBodyBytes != 4096 ||
		loaded.healthCheckTimeout != 500*time.Millisecond ||
		loaded.shutdownTimeout != 6*time.Second ||
		loaded.telemetryEndpoint != "collector.internal:4317" ||
		!loaded.telemetryInsecure ||
		loaded.telemetryTraceRatio != 0.25 ||
		loaded.telemetryExportTimeout != 7*time.Second ||
		loaded.metricExportInterval != 8*time.Second {
		t.Fatalf("loadConfig() overrides = %+v", loaded)
	}
}

func validEnvironment() map[string]string {
	return map[string]string{
		"SERVICE_VERSION":     "1.2.3",
		"ENVIRONMENT":         "test",
		"SERVICE_INSTANCE_ID": "user-test-1",
	}
}
