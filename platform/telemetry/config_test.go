package telemetry

import (
	"context"
	"crypto/tls"
	"math"
	"strings"
	"testing"
	"time"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestNewValidatesConfig(t *testing.T) {
	t.Parallel()

	zeroRatio := 0.0
	testCases := map[string]struct {
		change    func(*Config)
		options   []Option
		errorPart string
	}{
		"service name": {
			change:    func(config *Config) { config.ServiceName = "" },
			errorPart: "service name",
		},
		"service version": {
			change:    func(config *Config) { config.ServiceVersion = "" },
			errorPart: "service version",
		},
		"environment": {
			change:    func(config *Config) { config.Environment = "" },
			errorPart: "environment",
		},
		"instance ID": {
			change:    func(config *Config) { config.InstanceID = "" },
			errorPart: "instance ID",
		},
		"missing endpoint": {
			change:    func(config *Config) { config.Endpoint = "" },
			errorPart: "endpoint",
		},
		"endpoint with scheme": {
			change:    func(config *Config) { config.Endpoint = "https://collector:4317" },
			errorPart: "without scheme",
		},
		"invalid ratio": {
			change: func(config *Config) {
				ratio := 1.1
				config.TraceSampleRatio = &ratio
			},
			errorPart: "sample ratio",
		},
		"NaN ratio": {
			change: func(config *Config) {
				ratio := math.NaN()
				config.TraceSampleRatio = &ratio
			},
			errorPart: "sample ratio",
		},
		"negative duration": {
			change:    func(config *Config) { config.ExportTimeout = -time.Second },
			errorPart: "export timeout",
		},
		"negative cardinality": {
			change:    func(config *Config) { config.MetricCardinalityLimit = -1 },
			errorPart: "cardinality limit",
		},
		"TLS and insecure": {
			change: func(config *Config) {
				config.Insecure = true
				config.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS13}
			},
			errorPart: "mutually exclusive",
		},
		"unsafe TLS": {
			change: func(config *Config) {
				config.TLSConfig = &tls.Config{InsecureSkipVerify: true} //nolint:gosec // проверяется запрет настройки
			},
			errorPart: "InsecureSkipVerify",
		},
		"old TLS": {
			change: func(config *Config) {
				config.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS11}
			},
			errorPart: "TLS 1.2",
		},
		"empty header": {
			change:    func(config *Config) { config.Headers = map[string]string{" ": "secret"} },
			errorPart: "header name",
		},
		"endpoint is optional with test dependencies": {
			change: func(config *Config) {
				config.Endpoint = ""
				config.TraceSampleRatio = &zeroRatio
			},
			options: []Option{
				WithSpanExporter(tracetest.NewNoopExporter()),
				WithMetricReader(sdkmetric.NewManualReader()),
			},
		},
	}

	for name, testCase := range testCases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			config := validConfig()
			testCase.change(&config)

			pipeline, err := New(context.Background(), config, testCase.options...)
			if testCase.errorPart != "" {
				if err == nil || !strings.Contains(err.Error(), testCase.errorPart) {
					t.Fatalf("New() error = %v, want error containing %q", err, testCase.errorPart)
				}
				return
			}
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			if err := pipeline.Shutdown(context.Background()); err != nil {
				t.Fatalf("Shutdown() error = %v", err)
			}
		})
	}
}

func TestNewValidatesOptionsAndContext(t *testing.T) {
	t.Parallel()

	var nilSpanExporter *tracetest.InMemoryExporter
	var nilMetricReader *sdkmetric.ManualReader
	testCases := map[string]struct {
		ctx       context.Context
		options   []Option
		errorPart string
	}{
		"nil context": {
			options:   nil,
			errorPart: "context",
		},
		"nil option": {
			ctx:       context.Background(),
			options:   []Option{nil},
			errorPart: "option",
		},
		"typed nil span exporter": {
			ctx:       context.Background(),
			options:   []Option{WithSpanExporter(nilSpanExporter)},
			errorPart: "span exporter",
		},
		"typed nil metric reader": {
			ctx:       context.Background(),
			options:   []Option{WithMetricReader(nilMetricReader)},
			errorPart: "metric reader",
		},
		"duplicate span exporter": {
			ctx: context.Background(),
			options: []Option{
				WithSpanExporter(tracetest.NewNoopExporter()),
				WithSpanExporter(tracetest.NewNoopExporter()),
			},
			errorPart: "already configured",
		},
		"duplicate metric reader": {
			ctx: context.Background(),
			options: []Option{
				WithMetricReader(sdkmetric.NewManualReader()),
				WithMetricReader(sdkmetric.NewManualReader()),
			},
			errorPart: "already configured",
		},
	}

	for name, testCase := range testCases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := New(testCase.ctx, validConfig(), testCase.options...)
			if err == nil || !strings.Contains(err.Error(), testCase.errorPart) {
				t.Fatalf("New() error = %v, want error containing %q", err, testCase.errorPart)
			}
		})
	}
}

func TestNormalizeConfigCopiesMutableValues(t *testing.T) {
	t.Parallel()

	tlsConfig := &tls.Config{ServerName: "collector.internal"}
	headers := map[string]string{"authorization": "secret"}
	config := validConfig()
	config.TLSConfig = tlsConfig
	config.Headers = headers

	settings, err := normalizeConfig(config, true)
	if err != nil {
		t.Fatalf("normalizeConfig() error = %v", err)
	}

	tlsConfig.ServerName = "changed"
	headers["authorization"] = "changed"

	if settings.tlsConfig.ServerName != "collector.internal" {
		t.Fatalf("TLS ServerName = %q, want collector.internal", settings.tlsConfig.ServerName)
	}
	if settings.tlsConfig.MinVersion != tls.VersionTLS12 {
		t.Fatalf("TLS MinVersion = %d, want TLS 1.2", settings.tlsConfig.MinVersion)
	}
	if settings.headers["authorization"] != "secret" {
		t.Fatalf("header was not copied: %q", settings.headers["authorization"])
	}
}

func validConfig() Config {
	return Config{
		ServiceName:    "auth",
		ServiceVersion: "1.2.3",
		Environment:    "test",
		InstanceID:     "auth-1",
		Endpoint:       "collector.internal:4317",
	}
}
