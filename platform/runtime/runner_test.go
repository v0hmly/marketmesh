package runtime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

func TestRunnerShutsDownInReverseOrder(t *testing.T) {
	t.Parallel()

	started := make(chan string, 3)
	var shutdownMu sync.Mutex
	shutdownOrder := []string{}
	components := make([]Component, 0, 3)
	for _, name := range []string{"telemetry", "worker", "http"} {
		components = append(components, Component{
			Name: name,
			Run: func(ctx context.Context) error {
				started <- name
				<-ctx.Done()
				return ctx.Err()
			},
			Shutdown: func(context.Context) error {
				shutdownMu.Lock()
				defer shutdownMu.Unlock()
				shutdownOrder = append(shutdownOrder, name)
				return nil
			},
		})
	}

	health, err := NewHealth(HealthConfig{CheckTimeout: time.Second})
	if err != nil {
		t.Fatalf("NewHealth() error = %v", err)
	}
	runner, err := NewRunner(
		RunnerConfig{ShutdownTimeout: time.Second, Health: health},
		components...,
	)
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runner.Run(ctx) }()
	for range components {
		<-started
	}
	if err := health.Ready(context.Background()); err != nil {
		t.Fatalf("health.Ready() error while running = %v", err)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Runner.Run() error = %v", err)
	}
	if !errors.Is(health.Ready(context.Background()), ErrNotReady) {
		t.Fatal("health remains ready after shutdown")
	}

	want := []string{"http", "worker", "telemetry"}
	if len(shutdownOrder) != len(want) {
		t.Fatalf("shutdown order = %v, want %v", shutdownOrder, want)
	}
	for index := range want {
		if shutdownOrder[index] != want[index] {
			t.Fatalf("shutdown order = %v, want %v", shutdownOrder, want)
		}
	}
}

func TestRunnerPreservesRunAndShutdownErrors(t *testing.T) {
	t.Parallel()

	runErr := errors.New("serve failed")
	shutdownErr := errors.New("flush failed")
	runner, err := NewRunner(
		RunnerConfig{ShutdownTimeout: time.Second},
		Component{
			Name: "telemetry",
			Run: func(ctx context.Context) error {
				<-ctx.Done()
				return ctx.Err()
			},
			Shutdown: func(context.Context) error {
				return shutdownErr
			},
		},
		Component{
			Name: "http",
			Run: func(context.Context) error {
				return runErr
			},
			Shutdown: func(context.Context) error {
				return nil
			},
		},
	)
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}

	err = runner.Run(context.Background())
	if !errors.Is(err, runErr) {
		t.Fatalf("Runner.Run() error = %v, want run error preserved", err)
	}
	if !errors.Is(err, shutdownErr) {
		t.Fatalf("Runner.Run() error = %v, want shutdown error preserved", err)
	}
}

func TestRunnerBoundsStuckShutdown(t *testing.T) {
	t.Parallel()

	releaseRun := make(chan struct{})
	releaseShutdown := make(chan struct{})
	t.Cleanup(func() {
		close(releaseRun)
		close(releaseShutdown)
	})
	runner, err := NewRunner(
		RunnerConfig{ShutdownTimeout: 20 * time.Millisecond},
		Component{
			Name: "stuck",
			Run: func(context.Context) error {
				<-releaseRun
				return nil
			},
			Shutdown: func(context.Context) error {
				<-releaseShutdown
				return nil
			},
		},
	)
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	started := time.Now()
	err = runner.Run(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Runner.Run() error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(started); elapsed > 500*time.Millisecond {
		t.Fatalf("Runner.Run() elapsed = %v, want bounded shutdown", elapsed)
	}
}

func TestRunnerTreatsUnexpectedNilResultAsError(t *testing.T) {
	t.Parallel()

	runner, err := NewRunner(
		RunnerConfig{ShutdownTimeout: time.Second},
		Component{
			Name: "worker",
			Run: func(context.Context) error {
				return nil
			},
			Shutdown: func(context.Context) error {
				return nil
			},
		},
	)
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}

	err = runner.Run(context.Background())
	if !errors.Is(err, ErrComponentStopped) {
		t.Fatalf("Runner.Run() error = %v, want ErrComponentStopped", err)
	}
	if !errors.Is(runner.Run(context.Background()), ErrRunnerUsed) {
		t.Fatal("second Runner.Run() did not return ErrRunnerUsed")
	}
}
