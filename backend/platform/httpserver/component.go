package httpserver

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"reflect"

	serviceruntime "github.com/v0hmly/marketmesh/platform/runtime"
)

// Component связывает готовый HTTP server с listener и runtime lifecycle.
// Переданный shutdown context является общим deadline Runner; при его истечении
// активные соединения принудительно закрываются через http.Server.Close.
func Component(
	name string,
	server *http.Server,
	listener net.Listener,
) (serviceruntime.Component, error) {
	if server == nil {
		return serviceruntime.Component{}, errors.New("httpserver: server must not be nil")
	}
	if isNilInterface(server.Handler) {
		return serviceruntime.Component{}, errors.New("httpserver: server handler must not be nil")
	}
	if isNilInterface(listener) {
		return serviceruntime.Component{}, errors.New("httpserver: listener must not be nil")
	}

	return serviceruntime.Component{
		Name: name,
		Run: func(ctx context.Context) error {
			if ctx == nil {
				return errors.New("httpserver: run context must not be nil")
			}
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
				return fmt.Errorf("httpserver: serving: %w", err)
			}

			return nil
		},
		Shutdown: func(ctx context.Context) error {
			if ctx == nil {
				return errors.New("httpserver: shutdown context must not be nil")
			}

			if err := server.Shutdown(ctx); err != nil {
				closeErr := server.Close()
				return errors.Join(
					fmt.Errorf("httpserver: graceful shutdown: %w", err),
					wrapCloseError(closeErr),
				)
			}

			return nil
		},
	}, nil
}

func wrapCloseError(err error) error {
	if err == nil || errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
		return nil
	}

	return fmt.Errorf("httpserver: force close: %w", err)
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
