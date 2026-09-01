package redis

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	goredis "github.com/redis/go-redis/v9"
	"github.com/redis/go-redis/v9/maintnotifications"
	serviceruntime "github.com/v0hmly/marketmesh/platform/runtime"
	"github.com/v0hmly/marketmesh/platform/telemetry"
)

const instrumentationName = "github.com/v0hmly/marketmesh/platform/redis"

// Operation выполняет одну логическую операцию через типизированный API
// go-redis. Все команды должны использовать переданный ctx.
type Operation func(ctx context.Context, commands goredis.Cmdable) error

// Client владеет одним независимым Redis client, его пулом и instrumentation.
// Экземпляр нельзя совместно использовать между edge и auth trust zones.
type Client struct {
	role             Role
	commands         goredis.Cmdable
	backend          clientBackend
	timeouts         TimeoutConfig
	retry            retrySettings
	instruments      *instruments
	sleep            sleepFunc
	componentCreated atomic.Bool
	closeStarted     sync.Once
	closeDone        chan struct{}
	closeErr         error
}

type clientBackend interface {
	AddHook(goredis.Hook)
	Ping(ctx context.Context) *goredis.StatusCmd
	PoolStats() *goredis.PoolStats
	Close() error
}

type commandFacade struct {
	goredis.Cmdable
}

// New создаёт отдельный client, регистрирует безопасную telemetry и проверяет
// соединение в пределах Connect timeout.
func New(
	ctx context.Context,
	config Config,
	pipeline *telemetry.Telemetry,
) (*Client, error) {
	if ctx == nil {
		return nil, errors.New("redis: context must not be nil")
	}
	if pipeline == nil {
		return nil, errors.New("redis: telemetry must not be nil")
	}

	settings, err := normalizeConfig(config)
	if err != nil {
		return nil, err
	}

	dialer := safeDialer{
		address: settings.address,
		timeout: settings.timeouts.Connect,
		tls:     settings.transport.tls,
	}
	raw := goredis.NewClient(&goredis.Options{
		Network:  "tcp",
		Addr:     redactedRedisAddress,
		Dialer:   dialer.dial,
		Protocol: 3,
		CredentialsProviderContext: func(context.Context) (string, string, error) {
			return settings.authentication.username.Reveal(),
				settings.authentication.password.Reveal(), nil
		},
		DB:                    settings.database,
		MaxRetries:            -1,
		MinRetryBackoff:       -1,
		MaxRetryBackoff:       -1,
		DialTimeout:           settings.timeouts.Connect,
		DialerRetries:         1,
		ReadTimeout:           settings.timeouts.Read,
		WriteTimeout:          settings.timeouts.Write,
		ContextTimeoutEnabled: true,
		PoolSize:              settings.pool.Size,
		MaxConcurrentDials:    settings.pool.MaxConcurrentDials,
		PoolTimeout:           settings.timeouts.Pool,
		MinIdleConns:          settings.pool.MinIdleConns,
		MaxIdleConns:          settings.pool.MaxIdleConns,
		MaxActiveConns:        settings.pool.MaxActiveConns,
		ConnMaxIdleTime:       settings.pool.ConnMaxIdleTime,
		ConnMaxLifetime:       settings.pool.ConnMaxLifetime,
		ConnMaxLifetimeJitter: settings.pool.ConnMaxLifetimeJitter,
		TLSConfig:             nil,
		DisableIdentity:       false,
		MaintNotificationsConfig: &maintnotifications.Config{
			Mode: maintnotifications.ModeDisabled,
		},
	})

	client := &Client{
		role:      settings.role,
		commands:  &commandFacade{Cmdable: raw},
		backend:   raw,
		timeouts:  settings.timeouts,
		retry:     settings.retry,
		sleep:     sleepWithContext,
		closeDone: make(chan struct{}),
	}
	client.instruments, err = newInstruments(
		pipeline.Meter(instrumentationName),
		settings.role,
		raw,
		settings.pool.MaxActiveConns,
	)
	if err != nil {
		_ = raw.Close()
		return nil, err
	}
	raw.AddHook(&operationHook{
		role:        settings.role,
		tracer:      pipeline.Tracer(instrumentationName),
		instruments: client.instruments,
	})

	connectCtx, cancel := context.WithTimeout(ctx, settings.timeouts.Connect)
	defer cancel()
	if err := raw.Ping(connectCtx).Err(); err != nil {
		_ = client.instruments.unregister()
		_ = raw.Close()
		return nil, wrapOperation("connect", settings.role, err)
	}

	return client, nil
}

// Execute выполняет операцию ровно один раз. Этот метод следует использовать
// для записей и всех операций, идемпотентность которых не доказана.
func (client *Client) Execute(ctx context.Context, operation Operation) error {
	return client.execute(ctx, operation, false)
}

// ExecuteIdempotent разрешает ограниченные повторы только после явного
// решения вызывающей стороны, что вся операция идемпотентна.
func (client *Client) ExecuteIdempotent(ctx context.Context, operation Operation) error {
	return client.execute(ctx, operation, true)
}

func (client *Client) execute(ctx context.Context, operation Operation, allowRetry bool) error {
	if client == nil || client.backend == nil || client.commands == nil {
		return errors.New("redis: client must not be nil")
	}
	if ctx == nil {
		return errors.New("redis: operation context must not be nil")
	}
	if operation == nil {
		return errors.New("redis: operation must not be nil")
	}

	operationCtx, cancel := context.WithTimeout(ctx, client.timeouts.Command)
	defer cancel()

	maxAttempts := 1
	if allowRetry && client.retry.enabled {
		maxAttempts = client.retry.maxAttempts
	}
	backoff := client.retry.initialBackoff
	var operationErr error
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		operationErr = client.runAttempt(operationCtx, operation)
		if operationErr == nil {
			return nil
		}
		if !allowRetry || attempt == maxAttempts || !isRetryable(operationErr) {
			break
		}

		reason := retryReason(operationErr)
		client.instruments.recordRetry(operationCtx, reason)
		if err := client.sleep(operationCtx, backoff); err != nil {
			operationErr = err
			break
		}
		backoff = nextBackoff(backoff, client.retry)
	}

	if allowRetry && maxAttempts > 1 && isRetryable(operationErr) {
		operationErr = errors.Join(ErrRetryExhausted, operationErr)
	}

	return wrapOperation("execute operation for", client.role, operationErr)
}

func (client *Client) runAttempt(ctx context.Context, operation Operation) error {
	result := make(chan error, 1)
	go func() {
		result <- operation(ctx, client.commands)
	}()

	select {
	case err := <-result:
		if contextErr := ctx.Err(); contextErr != nil {
			return contextErr
		}

		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// ReadinessDependencies возвращает проверку конкретного Redis client.
func (client *Client) ReadinessDependencies() []serviceruntime.CriticalDependency {
	if client == nil || client.backend == nil {
		return []serviceruntime.CriticalDependency{}
	}

	return []serviceruntime.CriticalDependency{{
		Name: "redis-" + string(client.role),
		Check: func(ctx context.Context) error {
			if ctx == nil {
				return errors.New("redis: readiness context must not be nil")
			}
			readinessCtx, cancel := context.WithTimeout(ctx, client.timeouts.Readiness)
			defer cancel()

			return wrapOperation("readiness ping for", client.role, client.backend.Ping(readinessCtx).Err())
		},
	}}
}

// Component связывает client с platform/runtime lifecycle. Метод можно
// вызвать только один раз; Run блокируется до cancellation.
func (client *Client) Component(name string) (serviceruntime.Component, error) {
	if client == nil || client.backend == nil {
		return serviceruntime.Component{}, errors.New("redis: client must not be nil")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return serviceruntime.Component{}, errors.New("redis: component name must not be empty")
	}
	if !client.componentCreated.CompareAndSwap(false, true) {
		return serviceruntime.Component{}, errors.New("redis: component has already been created")
	}

	return serviceruntime.Component{
		Name: name,
		Run: func(ctx context.Context) error {
			if ctx == nil {
				return errors.New("redis: component context must not be nil")
			}
			<-ctx.Done()

			return ctx.Err()
		},
		Shutdown: client.Close,
	}, nil
}

// Close снимает metrics callback и закрывает client не более одного раза.
// Вызов ограничен меньшим из caller deadline и Shutdown timeout.
func (client *Client) Close(ctx context.Context) error {
	if client == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("redis: shutdown context must not be nil")
	}

	client.closeStarted.Do(func() {
		go func() {
			client.closeErr = errors.Join(
				wrapOperation("unregister metrics for", client.role, client.instruments.unregister()),
				wrapOperation("close", client.role, client.backend.Close()),
			)
			close(client.closeDone)
		}()
	})

	shutdownCtx, cancel := context.WithTimeout(ctx, client.timeouts.Shutdown)
	defer cancel()
	select {
	case <-client.closeDone:
		return client.closeErr
	case <-shutdownCtx.Done():
		return wrapOperation("close", client.role, shutdownCtx.Err())
	}
}

func isRetryable(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	if errors.Is(err, goredis.ErrPoolTimeout) || errors.Is(err, goredis.ErrPoolExhausted) ||
		errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var networkErr net.Error
	if errors.As(err, &networkErr) {
		return true
	}
	for _, prefix := range []string{
		"MAXCLIENTS", "LOADING", "READONLY", "MASTERDOWN", "CLUSTERDOWN", "TRYAGAIN", "NOREPLICAS",
	} {
		if goredis.HasErrorPrefix(err, prefix) {
			return true
		}
	}

	return false
}

func retryReason(err error) string {
	switch {
	case errors.Is(err, goredis.ErrPoolTimeout), errors.Is(err, goredis.ErrPoolExhausted):
		return "pool"
	case goredis.HasErrorPrefix(err, "LOADING"),
		goredis.HasErrorPrefix(err, "READONLY"),
		goredis.HasErrorPrefix(err, "MASTERDOWN"),
		goredis.HasErrorPrefix(err, "CLUSTERDOWN"),
		goredis.HasErrorPrefix(err, "TRYAGAIN"),
		goredis.HasErrorPrefix(err, "NOREPLICAS"),
		goredis.HasErrorPrefix(err, "MAXCLIENTS"):
		return "server"
	}
	var networkErr net.Error
	if errors.As(err, &networkErr) && networkErr.Timeout() {
		return "timeout"
	}
	if errors.As(err, &networkErr) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return "network"
	}

	return "connection"
}

func nextBackoff(current time.Duration, settings retrySettings) time.Duration {
	next := time.Duration(float64(current) * settings.backoffMultiplier)
	if next < current || next > settings.maxBackoff {
		return settings.maxBackoff
	}

	return next
}

func sleepWithContext(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

type sleepFunc func(ctx context.Context, duration time.Duration) error

func instrumentError(operation string, err error) error {
	if err == nil {
		return nil
	}

	return fmt.Errorf("redis: %s: %w", operation, err)
}

var _ clientBackend = (*goredis.Client)(nil)
var _ goredis.Cmdable = (*commandFacade)(nil)
