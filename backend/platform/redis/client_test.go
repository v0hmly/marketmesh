package redis

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/v0hmly/marketmesh/platform/telemetry"
)

func TestNewValidatesInputsBeforeConnecting(t *testing.T) {
	t.Parallel()

	config := validConfig(t)
	var nilContext context.Context
	if _, err := New(nilContext, config, telemetry.NewNoop()); err == nil {
		t.Fatal("New(nil context) error = nil")
	}
	if _, err := New(t.Context(), config, nil); err == nil {
		t.Fatal("New(nil telemetry) error = nil")
	}
	config.Role = "shared"
	if _, err := New(t.Context(), config, telemetry.NewNoop()); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("New(invalid config) error = %v, want ErrInvalidConfig", err)
	}
}

func TestExecuteNeverRetries(t *testing.T) {
	t.Parallel()

	client := newTestClient(t, &fakeBackend{})
	var attempts atomic.Int32
	err := client.Execute(t.Context(), func(context.Context, goredis.Cmdable) error {
		attempts.Add(1)

		return io.EOF
	})
	if !errors.Is(err, io.EOF) {
		t.Fatalf("Execute() error = %v, want EOF", err)
	}
	if attempts.Load() != 1 {
		t.Fatalf("Execute() attempts = %d, want 1", attempts.Load())
	}
}

func TestCommandFacadeDoesNotExposeRawClient(t *testing.T) {
	t.Parallel()

	raw := goredis.NewClient(&goredis.Options{Addr: redactedRedisAddress})
	t.Cleanup(func() { _ = raw.Close() })
	var commands goredis.Cmdable = &commandFacade{Cmdable: raw}
	if _, exposed := commands.(*goredis.Client); exposed {
		t.Fatal("command facade exposed raw go-redis client")
	}
	if _, exposed := commands.(interface{ Options() *goredis.Options }); exposed {
		t.Fatal("command facade exposed raw client options")
	}
}

func TestExecuteIdempotentRetriesOnlyTransientErrors(t *testing.T) {
	t.Parallel()

	client := newTestClient(t, &fakeBackend{})
	client.sleep = func(context.Context, time.Duration) error { return nil }
	var attempts atomic.Int32
	err := client.ExecuteIdempotent(
		t.Context(),
		func(context.Context, goredis.Cmdable) error {
			if attempts.Add(1) < 3 {
				return io.ErrUnexpectedEOF
			}

			return nil
		},
	)
	if err != nil {
		t.Fatalf("ExecuteIdempotent() error = %v", err)
	}
	if attempts.Load() != 3 {
		t.Fatalf("ExecuteIdempotent() attempts = %d, want 3", attempts.Load())
	}

	permanent := errors.New("permanent failure")
	attempts.Store(0)
	err = client.ExecuteIdempotent(
		t.Context(),
		func(context.Context, goredis.Cmdable) error {
			attempts.Add(1)

			return permanent
		},
	)
	if !errors.Is(err, permanent) || errors.Is(err, ErrRetryExhausted) {
		t.Fatalf("permanent error = %v", err)
	}
	if attempts.Load() != 1 {
		t.Fatalf("permanent attempts = %d, want 1", attempts.Load())
	}
}

func TestExecuteIdempotentReportsExhaustion(t *testing.T) {
	t.Parallel()

	client := newTestClient(t, &fakeBackend{})
	client.sleep = func(context.Context, time.Duration) error { return nil }
	err := client.ExecuteIdempotent(
		t.Context(),
		func(context.Context, goredis.Cmdable) error { return goredis.ErrPoolTimeout },
	)
	if !errors.Is(err, ErrRetryExhausted) || !errors.Is(err, goredis.ErrPoolTimeout) {
		t.Fatalf("ExecuteIdempotent() error = %v", err)
	}
}

func TestExecutePropagatesCommandContextAndCancellation(t *testing.T) {
	t.Parallel()

	client := newTestClient(t, &fakeBackend{})
	client.timeouts.Command = 25 * time.Millisecond
	err := client.Execute(
		t.Context(),
		func(ctx context.Context, _ goredis.Cmdable) error {
			<-ctx.Done()

			return ctx.Err()
		},
	)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Execute() error = %v, want DeadlineExceeded", err)
	}
}

func TestExecutePrefersContextErrorOverTransportError(t *testing.T) {
	t.Parallel()

	client := newTestClient(t, &fakeBackend{})
	client.timeouts.Command = 25 * time.Millisecond
	transportErr := errors.New("socket timeout")
	err := client.Execute(
		t.Context(),
		func(ctx context.Context, _ goredis.Cmdable) error {
			<-ctx.Done()

			return transportErr
		},
	)
	if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, transportErr) {
		t.Fatalf("Execute() error = %v, want normalized DeadlineExceeded", err)
	}
}

func TestExecuteReturnsPromptlyWhenCallbackIsStillUnwinding(t *testing.T) {
	t.Parallel()

	client := newTestClient(t, &fakeBackend{})
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	ctx, cancel := context.WithCancel(t.Context())
	started := make(chan struct{})
	result := make(chan error, 1)
	go func() {
		result <- client.Execute(ctx, func(context.Context, goredis.Cmdable) error {
			close(started)
			<-release

			return errors.New("late transport error")
		})
	}()
	<-started
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Execute() error = %v, want Canceled", err)
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Execute() did not return promptly after cancellation")
	}
}

func TestReadinessDependencyUsesBoundedContext(t *testing.T) {
	t.Parallel()

	backend := &fakeBackend{ping: func(ctx context.Context) error {
		<-ctx.Done()

		return ctx.Err()
	}}
	client := newTestClient(t, backend)
	client.timeouts.Readiness = 20 * time.Millisecond
	dependencies := client.ReadinessDependencies()
	if len(dependencies) != 1 || dependencies[0].Name != "redis-edge" {
		t.Fatalf("ReadinessDependencies() = %+v", dependencies)
	}
	if err := dependencies[0].Check(t.Context()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("readiness error = %v, want DeadlineExceeded", err)
	}
}

func TestComponentBlocksUntilCancellationAndCanOnlyBeCreatedOnce(t *testing.T) {
	t.Parallel()

	client := newTestClient(t, &fakeBackend{})
	component, err := client.Component("redis-edge")
	if err != nil {
		t.Fatalf("Component() error = %v", err)
	}
	if _, err := client.Component("duplicate"); err == nil {
		t.Fatal("second Component() error = nil")
	}

	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() { result <- component.Run(ctx) }()
	select {
	case err := <-result:
		t.Fatalf("Component.Run() returned before cancellation: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("Component.Run() error = %v, want Canceled", err)
	}
}

func TestCloseIsConcurrentSafeAndBounded(t *testing.T) {
	t.Parallel()

	backend := &fakeBackend{}
	client := newTestClient(t, backend)
	var waitGroup sync.WaitGroup
	errorsChannel := make(chan error, 8)
	for range 8 {
		waitGroup.Go(func() { errorsChannel <- client.Close(t.Context()) })
	}
	waitGroup.Wait()
	close(errorsChannel)
	for err := range errorsChannel {
		if err != nil {
			t.Errorf("Close() error = %v", err)
		}
	}
	if backend.closeCalls.Load() != 1 {
		t.Fatalf("Close() calls = %d, want 1", backend.closeCalls.Load())
	}

	release := make(chan struct{})
	started := make(chan struct{}, 1)
	blockedBackend := &fakeBackend{closeStarted: started, closeRelease: release}
	blocked := newTestClient(t, blockedBackend)
	blocked.timeouts.Shutdown = 20 * time.Millisecond
	t.Cleanup(func() { close(release) })
	err := blocked.Close(t.Context())
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("bounded Close() error = %v, want DeadlineExceeded", err)
	}
	select {
	case <-started:
	default:
		t.Fatal("backend Close() did not start")
	}
}

func TestErrorsHideSensitiveDetailsAndPreserveCause(t *testing.T) {
	t.Parallel()

	cause := errors.New("redis://user:private-password@edge-state:6379/private-key")
	err := wrapOperation("connect", RoleAuth, cause)
	if !errors.Is(err, cause) {
		t.Fatalf("wrapped error = %v, want preserved cause", err)
	}
	for _, sensitive := range []string{"private-password", "edge-state", "private-key"} {
		if strings.Contains(err.Error(), sensitive) {
			t.Fatalf("wrapped error exposed %q: %v", sensitive, err)
		}
	}
}

func TestNilClientAccessorsAreSafe(t *testing.T) {
	t.Parallel()

	var client *Client
	if dependencies := client.ReadinessDependencies(); len(dependencies) != 0 {
		t.Fatalf("nil readiness dependencies = %+v", dependencies)
	}
	if err := client.Close(t.Context()); err != nil {
		t.Fatalf("nil Close() error = %v", err)
	}
}
