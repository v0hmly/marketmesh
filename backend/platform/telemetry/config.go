package telemetry

import (
	"crypto/tls"
	"errors"
	"fmt"
	"math"
	"net"
	"strings"
	"time"
)

const (
	defaultTraceSampleRatio       = 1.0
	defaultTraceBatchTimeout      = 5 * time.Second
	defaultExportTimeout          = 10 * time.Second
	defaultMetricExportInterval   = time.Minute
	defaultMetricCardinalityLimit = 2000
)

// Config описывает ресурс OpenTelemetry и OTLP-подключение к Collector.
// Endpoint задаётся в формате host:port без схемы.
type Config struct {
	ServiceName    string
	ServiceVersion string
	Environment    string
	InstanceID     string

	Endpoint  string
	Insecure  bool
	TLSConfig *tls.Config
	Headers   map[string]string

	// TraceSampleRatio задаёт долю новых корневых trace от 0 до 1.
	// nil означает значение по умолчанию 1.0. Решение родительского span
	// всегда сохраняется благодаря ParentBased sampler.
	TraceSampleRatio  *float64
	TraceBatchTimeout time.Duration
	ExportTimeout     time.Duration

	MetricExportInterval   time.Duration
	MetricCardinalityLimit int
}

type settings struct {
	serviceName    string
	serviceVersion string
	environment    string
	instanceID     string
	endpoint       string
	insecure       bool
	tlsConfig      *tls.Config
	headers        map[string]string

	traceSampleRatio       float64
	traceBatchTimeout      time.Duration
	exportTimeout          time.Duration
	metricExportInterval   time.Duration
	metricCardinalityLimit int
}

func normalizeConfig(config Config, requireEndpoint bool) (settings, error) {
	result := settings{
		serviceName:            strings.TrimSpace(config.ServiceName),
		serviceVersion:         strings.TrimSpace(config.ServiceVersion),
		environment:            strings.TrimSpace(config.Environment),
		instanceID:             strings.TrimSpace(config.InstanceID),
		endpoint:               strings.TrimSpace(config.Endpoint),
		insecure:               config.Insecure,
		traceSampleRatio:       defaultTraceSampleRatio,
		traceBatchTimeout:      config.TraceBatchTimeout,
		exportTimeout:          config.ExportTimeout,
		metricExportInterval:   config.MetricExportInterval,
		metricCardinalityLimit: config.MetricCardinalityLimit,
	}

	if result.serviceName == "" {
		return settings{}, errors.New("telemetry: service name must not be empty")
	}
	if result.serviceVersion == "" {
		return settings{}, errors.New("telemetry: service version must not be empty")
	}
	if result.environment == "" {
		return settings{}, errors.New("telemetry: environment must not be empty")
	}
	if result.instanceID == "" {
		return settings{}, errors.New("telemetry: instance ID must not be empty")
	}
	if requireEndpoint && result.endpoint == "" {
		return settings{}, errors.New("telemetry: OTLP endpoint must not be empty")
	}
	if result.endpoint != "" {
		if err := validateEndpoint(result.endpoint); err != nil {
			return settings{}, err
		}
	}

	if config.TraceSampleRatio != nil {
		result.traceSampleRatio = *config.TraceSampleRatio
	}
	if math.IsNaN(result.traceSampleRatio) || math.IsInf(result.traceSampleRatio, 0) ||
		result.traceSampleRatio < 0 || result.traceSampleRatio > 1 {
		return settings{}, fmt.Errorf(
			"telemetry: trace sample ratio must be between 0 and 1: %v",
			result.traceSampleRatio,
		)
	}

	var err error
	result.traceBatchTimeout, err = durationOrDefault(
		"trace batch timeout",
		result.traceBatchTimeout,
		defaultTraceBatchTimeout,
	)
	if err != nil {
		return settings{}, err
	}
	result.exportTimeout, err = durationOrDefault(
		"export timeout",
		result.exportTimeout,
		defaultExportTimeout,
	)
	if err != nil {
		return settings{}, err
	}
	result.metricExportInterval, err = durationOrDefault(
		"metric export interval",
		result.metricExportInterval,
		defaultMetricExportInterval,
	)
	if err != nil {
		return settings{}, err
	}

	if result.metricCardinalityLimit < 0 {
		return settings{}, errors.New("telemetry: metric cardinality limit must not be negative")
	}
	if result.metricCardinalityLimit == 0 {
		result.metricCardinalityLimit = defaultMetricCardinalityLimit
	}

	if config.TLSConfig != nil && config.Insecure {
		return settings{}, errors.New("telemetry: TLS config and insecure transport are mutually exclusive")
	}
	if config.TLSConfig != nil {
		result.tlsConfig = config.TLSConfig.Clone()
		if result.tlsConfig.InsecureSkipVerify {
			return settings{}, errors.New("telemetry: TLS InsecureSkipVerify is forbidden")
		}
		if result.tlsConfig.MinVersion == 0 {
			result.tlsConfig.MinVersion = tls.VersionTLS12
		}
		if result.tlsConfig.MinVersion < tls.VersionTLS12 {
			return settings{}, errors.New("telemetry: minimum TLS version must be TLS 1.2 or newer")
		}
	} else if !result.insecure {
		result.tlsConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}

	result.headers, err = copyHeaders(config.Headers)
	if err != nil {
		return settings{}, err
	}

	return result, nil
}

func validateEndpoint(endpoint string) error {
	if strings.Contains(endpoint, "://") {
		return errors.New("telemetry: OTLP endpoint must use host:port format without scheme")
	}

	host, port, err := net.SplitHostPort(endpoint)
	if err != nil || strings.TrimSpace(host) == "" || strings.TrimSpace(port) == "" {
		return fmt.Errorf("telemetry: invalid OTLP endpoint %q: expected host:port", endpoint)
	}

	return nil
}

func durationOrDefault(name string, value time.Duration, defaultValue time.Duration) (time.Duration, error) {
	if value < 0 {
		return 0, fmt.Errorf("telemetry: %s must not be negative", name)
	}
	if value == 0 {
		return defaultValue, nil
	}

	return value, nil
}

func copyHeaders(headers map[string]string) (map[string]string, error) {
	if len(headers) == 0 {
		return nil, nil
	}

	result := make(map[string]string, len(headers))
	for key, value := range headers {
		key = strings.TrimSpace(key)
		if key == "" {
			return nil, errors.New("telemetry: OTLP header name must not be empty")
		}
		result[key] = value
	}

	return result, nil
}
