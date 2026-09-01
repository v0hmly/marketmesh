package app

import (
	"bytes"
	"context"
	"net"
	"strings"
	"testing"
	"time"

	serviceruntime "github.com/v0hmly/marketmesh/platform/runtime"
)

func TestRunLogsConfigurationErrorOnceWithoutSecrets(t *testing.T) {
	t.Parallel()

	values := requiredEnvironment()
	secret := "postgres://auth:secret-value@example.invalid/auth"
	values["POSTGRES_RW_DSN"] = secret
	values["OTEL_TRACE_SAMPLE_RATIO"] = "invalid"
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err := run(context.Background(), systemDependencies{
		env: serviceruntime.MapEnv(values), stdout: &stdout, stderr: &stderr,
		listen: func(string, string) (net.Listener, error) {
			t.Fatal("listen called for invalid configuration")
			return nil, nil
		},
	})
	if err == nil {
		t.Fatal("run() error = nil")
	}
	if strings.Count(stderr.String(), `"level":"error"`) != 1 || stdout.Len() != 0 {
		t.Fatalf("unexpected logs: stdout %q stderr %q", stdout.String(), stderr.String())
	}
	if strings.Contains(stderr.String(), secret) || strings.Contains(err.Error(), secret) {
		t.Fatal("configuration failure exposed a secret")
	}
}

func TestPostgresConfigAndNoopTelemetry(t *testing.T) {
	t.Parallel()

	loaded, err := loadConfig(serviceruntime.MapEnv(requiredEnvironment()))
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	postgres := postgresConfig(loaded)
	if postgres.ApplicationName != "auth/instance-1" || postgres.RW.MaxConns != defaultPostgresMaxConns || postgres.RO.QueryTimeout != defaultPostgresQueryTimeout {
		t.Fatalf("postgresConfig() = %+v", postgres)
	}
	pipeline, err := newTelemetry(context.Background(), loaded)
	if err != nil {
		t.Fatalf("newTelemetry() error = %v", err)
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := pipeline.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Telemetry.Shutdown() error = %v", err)
	}
}
