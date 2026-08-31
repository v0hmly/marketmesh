package app

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/v0hmly/marketmesh/platform/logger"
	serviceruntime "github.com/v0hmly/marketmesh/platform/runtime"
)

func TestNewHTTPServerLimitsRequestBody(t *testing.T) {
	t.Parallel()

	config := validHTTPTestConfig()
	config.httpMaxBodyBytes = 4
	server := newHTTPServer(
		config,
		newHTTPTestLogger(t),
		http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			_, readErr := io.ReadAll(request.Body)
			var maxBytesErr *http.MaxBytesError
			if !errors.As(readErr, &maxBytesErr) {
				http.Error(response, "body was not limited", http.StatusInternalServerError)
				return
			}
			response.WriteHeader(http.StatusRequestEntityTooLarge)
		}),
	)

	request := httptest.NewRequest(http.MethodPost, "/", strings.NewReader("12345"))
	response := httptest.NewRecorder()
	server.Handler.ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", response.Code)
	}
}

func TestHealthHandler(t *testing.T) {
	t.Parallel()

	health, err := serviceruntime.NewHealth(serviceruntime.HealthConfig{
		CheckTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("runtime.NewHealth() error = %v", err)
	}
	handler := newHealthHandler(health)

	assertHTTPStatus(t, handler, "/livez", http.StatusNoContent)
	assertHTTPStatus(t, handler, "/readyz", http.StatusServiceUnavailable)
	health.MarkReady()
	assertHTTPStatus(t, handler, "/readyz", http.StatusNoContent)
	health.MarkNotReady()
	assertHTTPStatus(t, handler, "/readyz", http.StatusServiceUnavailable)
}

func TestHealthHandlerDoesNotExposeReadinessError(t *testing.T) {
	t.Parallel()

	const secret = "database-password-must-not-leak"
	health, err := serviceruntime.NewHealth(serviceruntime.HealthConfig{
		CheckTimeout: time.Second,
		Dependencies: []serviceruntime.CriticalDependency{
			{
				Name: "database",
				Check: func(context.Context) error {
					return errors.New(secret)
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("runtime.NewHealth() error = %v", err)
	}
	health.MarkReady()

	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	response := httptest.NewRecorder()
	newHealthHandler(health).ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("readiness status = %d, want 503", response.Code)
	}
	if strings.Contains(response.Body.String(), secret) {
		t.Fatalf("readiness response %q exposes dependency error", response.Body.String())
	}
}

func assertHTTPStatus(
	t *testing.T,
	handler http.Handler,
	path string,
	want int,
) {
	t.Helper()

	request := httptest.NewRequest(http.MethodGet, path, nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != want {
		t.Fatalf("GET %s status = %d, want %d", path, response.Code, want)
	}
	if value := response.Header().Get("Cache-Control"); value != "no-store" {
		t.Fatalf("GET %s Cache-Control = %q, want no-store", path, value)
	}
}

func newHTTPTestLogger(t *testing.T) *logger.Logger {
	t.Helper()

	log, err := logger.New(logger.Config{
		Service:     "user-test",
		Version:     "test",
		Environment: "test",
		Output:      io.Discard,
	})
	if err != nil {
		t.Fatalf("logger.New() error = %v", err)
	}

	return log
}

func validHTTPTestConfig() config {
	return config{
		httpReadHeaderTimeout: time.Second,
		httpReadTimeout:       2 * time.Second,
		httpWriteTimeout:      3 * time.Second,
		httpIdleTimeout:       30 * time.Second,
		httpMaxHeaderBytes:    16 * 1024,
		httpMaxBodyBytes:      1024,
	}
}
