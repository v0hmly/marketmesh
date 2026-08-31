package runtime

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestHealthReadinessTransitions(t *testing.T) {
	t.Parallel()

	health, err := NewHealth(HealthConfig{CheckTimeout: time.Second})
	if err != nil {
		t.Fatalf("NewHealth() error = %v", err)
	}

	if !errors.Is(health.Ready(t.Context()), ErrNotReady) {
		t.Fatal("Ready() before MarkReady did not return ErrNotReady")
	}

	health.MarkReady()
	if err := health.Ready(t.Context()); err != nil {
		t.Fatalf("Ready() after MarkReady error = %v", err)
	}

	health.MarkNotReady()
	if !errors.Is(health.Ready(t.Context()), ErrNotReady) {
		t.Fatal("Ready() after MarkNotReady did not return ErrNotReady")
	}
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
