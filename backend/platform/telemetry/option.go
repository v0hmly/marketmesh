package telemetry

import (
	"errors"
	"reflect"

	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// Option настраивает зависимости telemetry, в первую очередь для тестов.
type Option interface {
	apply(*options) error
}

type optionFunc func(*options) error

func (function optionFunc) apply(options *options) error {
	return function(options)
}

type options struct {
	spanExporter    sdktrace.SpanExporter
	metricReader    sdkmetric.Reader
	hasSpanExporter bool
	hasMetricReader bool
}

// WithSpanExporter заменяет OTLP trace exporter. Telemetry принимает владение
// exporter и завершает его в Shutdown.
func WithSpanExporter(exporter sdktrace.SpanExporter) Option {
	return optionFunc(func(options *options) error {
		if isNilInterface(exporter) {
			return errors.New("telemetry: span exporter must not be nil")
		}
		if options.hasSpanExporter {
			return errors.New("telemetry: span exporter is already configured")
		}

		options.spanExporter = exporter
		options.hasSpanExporter = true

		return nil
	})
}

// WithMetricReader заменяет периодический OTLP reader. Telemetry принимает
// владение reader и завершает его в Shutdown.
func WithMetricReader(reader sdkmetric.Reader) Option {
	return optionFunc(func(options *options) error {
		if isNilInterface(reader) {
			return errors.New("telemetry: metric reader must not be nil")
		}
		if options.hasMetricReader {
			return errors.New("telemetry: metric reader is already configured")
		}

		options.metricReader = reader
		options.hasMetricReader = true

		return nil
	})
}

func isNilInterface(value any) bool {
	if value == nil {
		return true
	}

	kind := reflect.TypeOf(value).Kind()
	switch kind {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflect.ValueOf(value).IsNil()
	default:
		return false
	}
}

func applyOptions(configOptions []Option) (options, error) {
	var result options
	for _, option := range configOptions {
		if option == nil {
			return options{}, errors.New("telemetry: option must not be nil")
		}
		if err := option.apply(&result); err != nil {
			return options{}, err
		}
	}

	return result, nil
}
