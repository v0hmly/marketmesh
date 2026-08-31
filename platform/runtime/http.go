package runtime

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"time"
)

// HTTPServerConfig содержит обязательные HTTP timeout и пределы ресурсов.
type HTTPServerConfig struct {
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	MaxHeaderBytes    int
	MaxBodyBytes      int64
	ErrorLog          *log.Logger
}

// NewHTTPServer создаёт *http.Server с обязательными timeout, пределом
// заголовков и ограничением request body. Listener и TLS остаются
// ответственностью composition root.
func NewHTTPServer(config HTTPServerConfig, handler http.Handler) (*http.Server, error) {
	if isNilInterface(handler) {
		return nil, errors.New("runtime: HTTP handler must not be nil")
	}
	if config.ReadHeaderTimeout <= 0 {
		return nil, errors.New("runtime: HTTP read header timeout must be positive")
	}
	if config.ReadTimeout <= 0 {
		return nil, errors.New("runtime: HTTP read timeout must be positive")
	}
	if config.WriteTimeout <= 0 {
		return nil, errors.New("runtime: HTTP write timeout must be positive")
	}
	if config.IdleTimeout <= 0 {
		return nil, errors.New("runtime: HTTP idle timeout must be positive")
	}
	if config.MaxHeaderBytes <= 0 {
		return nil, errors.New("runtime: HTTP max header bytes must be positive")
	}
	if config.MaxBodyBytes <= 0 {
		return nil, errors.New("runtime: HTTP max body bytes must be positive")
	}

	return &http.Server{
		Handler:           http.MaxBytesHandler(handler, config.MaxBodyBytes),
		ReadHeaderTimeout: config.ReadHeaderTimeout,
		ReadTimeout:       config.ReadTimeout,
		WriteTimeout:      config.WriteTimeout,
		IdleTimeout:       config.IdleTimeout,
		MaxHeaderBytes:    config.MaxHeaderBytes,
		ErrorLog:          config.ErrorLog,
	}, nil
}

// NewHTTPComponent адаптирует стандартный http.Server к Component. Component
// принимает владение listener и закрывает server через Shutdown.
func NewHTTPComponent(
	name string,
	server *http.Server,
	listener net.Listener,
) (Component, error) {
	if server == nil {
		return Component{}, errors.New("runtime: HTTP server must not be nil")
	}
	if isNilInterface(listener) {
		return Component{}, errors.New("runtime: HTTP listener must not be nil")
	}

	return Component{
		Name: name,
		Run: func(ctx context.Context) error {
			if server.BaseContext == nil {
				server.BaseContext = func(net.Listener) context.Context {
					return ctx
				}
			}

			err := server.Serve(listener)
			if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
				return nil
			}
			if err != nil {
				return fmt.Errorf("serving HTTP: %w", err)
			}

			return nil
		},
		Shutdown: func(ctx context.Context) error {
			if err := server.Shutdown(ctx); err != nil {
				return fmt.Errorf("stopping HTTP server: %w", err)
			}

			return nil
		},
	}, nil
}
