package httpserver

import (
	"bytes"
	"net/http"
	"testing"
	"time"

	"github.com/v0hmly/marketmesh/platform/logger"
	"github.com/v0hmly/marketmesh/platform/telemetry"
)

func TestNewRequiresSafeConfiguration(t *testing.T) {
	t.Parallel()

	valid := validTestConfig(t, &bytes.Buffer{})
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{name: "nil handler", mutate: func(config *Config) { config.Handler = nil }},
		{
			name: "typed nil handler",
			mutate: func(config *Config) {
				var handler *typedNilHandler
				config.Handler = handler
			},
		},
		{name: "nil logger", mutate: func(config *Config) { config.Logger = nil }},
		{name: "nil telemetry", mutate: func(config *Config) { config.Telemetry = nil }},
		{
			name: "read header timeout",
			mutate: func(config *Config) {
				config.ReadHeaderTimeout = 0
			},
		},
		{name: "read timeout", mutate: func(config *Config) { config.ReadTimeout = -time.Second }},
		{name: "write timeout", mutate: func(config *Config) { config.WriteTimeout = 0 }},
		{name: "idle timeout", mutate: func(config *Config) { config.IdleTimeout = -time.Second }},
		{name: "request timeout", mutate: func(config *Config) { config.RequestTimeout = 0 }},
		{name: "header limit", mutate: func(config *Config) { config.MaxHeaderBytes = -1 }},
		{name: "body limit", mutate: func(config *Config) { config.MaxBodyBytes = 0 }},
		{
			name: "nil middleware",
			mutate: func(config *Config) {
				config.Middleware = []Middleware{nil}
			},
		},
		{
			name: "middleware returns nil",
			mutate: func(config *Config) {
				config.Middleware = []Middleware{func(http.Handler) http.Handler { return nil }}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			config := valid
			test.mutate(&config)
			if _, err := New(config); err == nil {
				t.Fatal("New() error = nil, want validation error")
			}
		})
	}
}

func TestNewConfiguresStandardServer(t *testing.T) {
	t.Parallel()

	config := validTestConfig(t, &bytes.Buffer{})
	server, err := New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if server.ReadHeaderTimeout != config.ReadHeaderTimeout ||
		server.ReadTimeout != config.ReadTimeout ||
		server.WriteTimeout != config.WriteTimeout ||
		server.IdleTimeout != config.IdleTimeout ||
		server.MaxHeaderBytes != config.MaxHeaderBytes {
		t.Fatalf("server limits = %+v, want config %+v", server, config)
	}
	if server.ErrorLog == nil {
		t.Fatal("server ErrorLog = nil")
	}
}

func TestAdditionalMiddlewareOrder(t *testing.T) {
	t.Parallel()

	order := []string{}
	config := validTestConfig(t, &bytes.Buffer{})
	config.Middleware = []Middleware{
		func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				order = append(order, "first-in")
				next.ServeHTTP(response, request)
				order = append(order, "first-out")
			})
		},
		func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				order = append(order, "second-in")
				next.ServeHTTP(response, request)
				order = append(order, "second-out")
			})
		},
	}
	config.Handler = http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		order = append(order, "handler")
		response.WriteHeader(http.StatusNoContent)
	})

	server, err := New(config)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	request := newTestRequest(t, http.MethodGet, "/", nil)
	response := newTestRecorder()
	server.Handler.ServeHTTP(response, request)

	want := []string{"first-in", "second-in", "handler", "second-out", "first-out"}
	if len(order) != len(want) {
		t.Fatalf("middleware order = %v, want %v", order, want)
	}
	for index := range want {
		if order[index] != want[index] {
			t.Fatalf("middleware order = %v, want %v", order, want)
		}
	}
}

type typedNilHandler struct{}

func (*typedNilHandler) ServeHTTP(http.ResponseWriter, *http.Request) {}

func validTestConfig(t *testing.T, output *bytes.Buffer) Config {
	t.Helper()

	log, err := logger.New(logger.Config{
		Service:     "httpserver-test",
		Version:     "test",
		Environment: "test",
		Output:      output,
	})
	if err != nil {
		t.Fatalf("logger.New() error = %v", err)
	}

	return Config{
		Handler: http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
			response.WriteHeader(http.StatusNoContent)
		}),
		ReadHeaderTimeout: time.Second,
		ReadTimeout:       2 * time.Second,
		WriteTimeout:      3 * time.Second,
		IdleTimeout:       30 * time.Second,
		RequestTimeout:    time.Second,
		MaxHeaderBytes:    16 * 1024,
		MaxBodyBytes:      1024,
		Logger:            log,
		Telemetry:         telemetry.NewNoop(),
	}
}
