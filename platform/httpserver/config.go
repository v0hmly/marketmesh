package httpserver

import (
	"errors"
	"net/http"
	"time"

	"github.com/v0hmly/marketmesh/platform/logger"
	"github.com/v0hmly/marketmesh/platform/telemetry"
)

// Middleware оборачивает HTTP handler. Первый middleware в Config.Middleware
// выполняется первым и завершает обработку последним.
type Middleware func(http.Handler) http.Handler

// Config задаёт обязательные transport limits, request deadline и явно
// переданные зависимости наблюдаемости.
type Config struct {
	Handler           http.Handler
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	RequestTimeout    time.Duration
	MaxHeaderBytes    int
	MaxBodyBytes      int64
	Logger            *logger.Logger
	Telemetry         *telemetry.Telemetry
	Middleware        []Middleware
}

// New создаёт стандартный http.Server с ограничением заголовков, тела и
// времени обработки. Listener и TLS создаёт composition root.
//
// Встроенные middleware не журналируют URL, headers или body. Дополнительные
// middleware выполняются после deadline, telemetry, logging и recovery.
func New(config Config) (*http.Server, error) {
	if isNilInterface(config.Handler) {
		return nil, errors.New("httpserver: handler must not be nil")
	}
	if config.Logger == nil {
		return nil, errors.New("httpserver: logger must not be nil")
	}
	if config.Telemetry == nil {
		return nil, errors.New("httpserver: telemetry must not be nil")
	}
	if err := validateConfig(config); err != nil {
		return nil, err
	}

	handler, err := applyMiddleware(config.Handler, config.Middleware)
	if err != nil {
		return nil, err
	}

	handler = recoveryMiddleware(config.Logger, handler)
	handler = observedLoggingMiddleware(config.Logger, handler)
	handler, err = telemetryMiddleware(config.Telemetry, handler)
	if err != nil {
		return nil, err
	}
	handler = bodyLimitMiddleware(
		config.MaxBodyBytes,
		requestDeadlineMiddleware(config.RequestTimeout, handler),
	)

	return &http.Server{
		Handler:           handler,
		ReadHeaderTimeout: config.ReadHeaderTimeout,
		ReadTimeout:       config.ReadTimeout,
		WriteTimeout:      config.WriteTimeout,
		IdleTimeout:       config.IdleTimeout,
		MaxHeaderBytes:    config.MaxHeaderBytes,
		ErrorLog:          config.Logger.HTTPErrorLog(),
	}, nil
}

func validateConfig(config Config) error {
	if config.ReadHeaderTimeout <= 0 {
		return errors.New("httpserver: read header timeout must be positive")
	}
	if config.ReadTimeout <= 0 {
		return errors.New("httpserver: read timeout must be positive")
	}
	if config.WriteTimeout <= 0 {
		return errors.New("httpserver: write timeout must be positive")
	}
	if config.IdleTimeout <= 0 {
		return errors.New("httpserver: idle timeout must be positive")
	}
	if config.RequestTimeout <= 0 {
		return errors.New("httpserver: request timeout must be positive")
	}
	if config.MaxHeaderBytes <= 0 {
		return errors.New("httpserver: max header bytes must be positive")
	}
	if config.MaxBodyBytes <= 0 {
		return errors.New("httpserver: max body bytes must be positive")
	}

	for _, middleware := range config.Middleware {
		if middleware == nil {
			return errors.New("httpserver: middleware must not be nil")
		}
	}

	return nil
}

func applyMiddleware(handler http.Handler, middleware []Middleware) (http.Handler, error) {
	result := handler
	for index := len(middleware) - 1; index >= 0; index-- {
		result = middleware[index](result)
		if isNilInterface(result) {
			return nil, errors.New("httpserver: middleware returned nil handler")
		}
	}

	return result, nil
}
