package app

import (
	"bytes"
	"context"
	"errors"
	"net"
	"strings"
	"testing"

	serviceruntime "github.com/v0hmly/marketmesh/platform/runtime"
)

func TestRunLogsStartupErrorOnceAndPreservesIt(t *testing.T) {
	t.Parallel()

	listenErr := errors.New("listen failed")
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err := run(
		context.Background(),
		systemDependencies{
			env:    serviceruntime.MapEnv(validEnvironment()),
			stdout: &stdout,
			stderr: &stderr,
			listen: func(string, string) (net.Listener, error) {
				return nil, listenErr
			},
		},
	)
	if !errors.Is(err, listenErr) {
		t.Fatalf("run() error = %v, want listen error preserved", err)
	}
	if count := strings.Count(stdout.String(), `"level":"error"`); count != 1 {
		t.Fatalf("error log count = %d, want 1; output = %q", count, stdout.String())
	}
	if strings.Contains(stderr.String(), `"level":"error"`) {
		t.Fatalf("bootstrap logger duplicated error: %q", stderr.String())
	}
}

func TestRunLogsConfigurationErrorOnceWithoutSecret(t *testing.T) {
	t.Parallel()

	const secret = "collector-secret"
	values := validEnvironment()
	values["OTEL_AUTH_TOKEN"] = secret
	values["OTEL_TRACE_SAMPLE_RATIO"] = "invalid"
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err := run(
		context.Background(),
		systemDependencies{
			env:    serviceruntime.MapEnv(values),
			stdout: &stdout,
			stderr: &stderr,
			listen: func(string, string) (net.Listener, error) {
				t.Fatal("listen must not be called for invalid config")
				return nil, nil
			},
		},
	)
	if err == nil {
		t.Fatal("run() error = nil, want configuration error")
	}
	if count := strings.Count(stderr.String(), `"level":"error"`); count != 1 {
		t.Fatalf("bootstrap error log count = %d, want 1; output = %q", count, stderr.String())
	}
	if strings.Contains(stderr.String(), secret) || strings.Contains(err.Error(), secret) {
		t.Fatal("configuration error exposed telemetry secret")
	}
	if stdout.Len() != 0 {
		t.Fatalf("service logger output = %q, want empty", stdout.String())
	}
}
