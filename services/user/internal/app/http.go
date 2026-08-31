package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"

	"github.com/v0hmly/marketmesh/platform/logger"
	serviceruntime "github.com/v0hmly/marketmesh/platform/runtime"
)

func newHTTPServer(
	config config,
	log *logger.Logger,
	handler http.Handler,
) *http.Server {
	return &http.Server{
		Handler:           http.MaxBytesHandler(handler, config.httpMaxBodyBytes),
		ReadHeaderTimeout: config.httpReadHeaderTimeout,
		ReadTimeout:       config.httpReadTimeout,
		WriteTimeout:      config.httpWriteTimeout,
		IdleTimeout:       config.httpIdleTimeout,
		MaxHeaderBytes:    config.httpMaxHeaderBytes,
		ErrorLog:          log.HTTPErrorLog(),
	}
}

func newHealthHandler(health *serviceruntime.Health) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /livez", func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		response.WriteHeader(http.StatusNoContent)
	})
	mux.HandleFunc("GET /readyz", func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Cache-Control", "no-store")
		if err := health.Ready(request.Context()); err != nil {
			http.Error(response, "not ready", http.StatusServiceUnavailable)
			return
		}

		response.WriteHeader(http.StatusNoContent)
	})

	return mux
}

func newHTTPComponent(
	server *http.Server,
	listener net.Listener,
) serviceruntime.Component {
	return serviceruntime.Component{
		Name: "http",
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
	}
}
