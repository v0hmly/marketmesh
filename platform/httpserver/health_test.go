package httpserver

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	serviceruntime "github.com/v0hmly/marketmesh/platform/runtime"
)

func TestNewHealthHandlerReflectsLivenessAndReadiness(t *testing.T) {
	t.Parallel()

	health, err := serviceruntime.NewHealth(serviceruntime.HealthConfig{
		CheckTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("runtime.NewHealth() error = %v", err)
	}
	handler, err := NewHealthHandler(health)
	if err != nil {
		t.Fatalf("NewHealthHandler() error = %v", err)
	}

	assertHTTPStatus(t, handler, "/livez", http.StatusNoContent)
	assertHTTPStatus(t, handler, "/readyz", http.StatusServiceUnavailable)
	health.MarkReady()
	assertHTTPStatus(t, handler, "/readyz", http.StatusNoContent)
	health.MarkNotReady()
	assertHTTPStatus(t, handler, "/readyz", http.StatusServiceUnavailable)
}

func TestNewHealthHandlerDoesNotExposeDependencyError(t *testing.T) {
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
	handler, err := NewHealthHandler(health)
	if err != nil {
		t.Fatalf("NewHealthHandler() error = %v", err)
	}

	request := newTestRequest(t, http.MethodGet, "/readyz", nil)
	response := newTestRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("readiness status = %d, want 503", response.Code)
	}
	if strings.Contains(response.Body.String(), secret) {
		t.Fatalf("readiness response %q exposes dependency error", response.Body.String())
	}
}

func TestNewHealthHandlerRejectsNilHealth(t *testing.T) {
	t.Parallel()

	if _, err := NewHealthHandler(nil); err == nil {
		t.Fatal("NewHealthHandler() error = nil, want validation error")
	}
}

func assertHTTPStatus(
	t *testing.T,
	handler http.Handler,
	path string,
	want int,
) {
	t.Helper()

	request := newTestRequest(t, http.MethodGet, path, nil)
	response := newTestRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != want {
		t.Fatalf("GET %s status = %d, want %d", path, response.Code, want)
	}
	if value := response.Header().Get("Cache-Control"); value != "no-store" {
		t.Fatalf("GET %s Cache-Control = %q, want no-store", path, value)
	}
}
