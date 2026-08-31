package runtime

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestHealthDistinguishesLivenessAndReadiness(t *testing.T) {
	t.Parallel()

	health, err := NewHealth(HealthConfig{CheckTimeout: time.Second})
	if err != nil {
		t.Fatalf("NewHealth() error = %v", err)
	}

	assertStatus(t, health.LivenessHandler(), http.StatusNoContent)
	assertStatus(t, health.ReadinessHandler(), http.StatusServiceUnavailable)

	health.MarkReady()
	assertStatus(t, health.ReadinessHandler(), http.StatusNoContent)

	health.MarkNotReady()
	assertStatus(t, health.LivenessHandler(), http.StatusNoContent)
	assertStatus(t, health.ReadinessHandler(), http.StatusServiceUnavailable)
}

func TestHealthChecksCriticalDependenciesWithoutExposingErrors(t *testing.T) {
	t.Parallel()

	dependencyErr := errors.New("database password=secret-value")
	health, err := NewHealth(HealthConfig{
		CheckTimeout: time.Second,
		Dependencies: []CriticalDependency{
			{
				Name: "database",
				Check: func(context.Context) error {
					return dependencyErr
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("NewHealth() error = %v", err)
	}
	health.MarkReady()

	readyErr := health.Ready(context.Background())
	if !errors.Is(readyErr, ErrNotReady) {
		t.Fatalf("Ready() error = %v, want ErrNotReady", readyErr)
	}
	if !errors.Is(readyErr, dependencyErr) {
		t.Fatalf("Ready() error = %v, want dependency error preserved", readyErr)
	}

	request := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	response := httptest.NewRecorder()
	health.ReadinessHandler().ServeHTTP(response, request)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("readiness status = %d, want 503", response.Code)
	}
	if strings.Contains(response.Body.String(), "secret-value") {
		t.Fatalf("readiness response %q exposes dependency error", response.Body.String())
	}
}

func TestHealthBoundsStuckDependency(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	health, err := NewHealth(HealthConfig{
		CheckTimeout: 20 * time.Millisecond,
		Dependencies: []CriticalDependency{
			{
				Name: "stuck",
				Check: func(context.Context) error {
					<-release
					return nil
				},
			},
		},
	})
	if err != nil {
		t.Fatalf("NewHealth() error = %v", err)
	}
	health.MarkReady()

	started := time.Now()
	readyErr := health.Ready(context.Background())
	if !errors.Is(readyErr, context.DeadlineExceeded) {
		t.Fatalf("Ready() error = %v, want deadline exceeded", readyErr)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("Ready() elapsed = %v, want bounded check", elapsed)
	}
}

func assertStatus(t *testing.T, handler http.Handler, want int) {
	t.Helper()

	request := httptest.NewRequest(http.MethodGet, "/", nil)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != want {
		t.Errorf("status = %d, want %d", response.Code, want)
	}
}
