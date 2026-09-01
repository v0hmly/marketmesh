package postgres

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	serviceruntime "github.com/v0hmly/marketmesh/platform/runtime"
	"github.com/v0hmly/marketmesh/platform/telemetry"
	"go.opentelemetry.io/otel/trace"
)

const instrumentationName = "github.com/v0hmly/marketmesh/platform/postgres"

type poolRole string

const (
	roleRW poolRole = "rw"
	roleRO poolRole = "ro"
)

// Database владеет независимыми RW/RO-пулами и их instrumentation.
type Database struct {
	rw          *managedPool
	ro          *managedPool
	retry       retrySettings
	tracer      trace.Tracer
	instruments *instruments
	sleep       sleepFunc

	componentCreated atomic.Bool
	closeOnce        sync.Once
	closeErr         error
}

type managedPool struct {
	role     poolRole
	backend  poolBackend
	executor Executor
}

type poolBackend interface {
	queryBackend
	BeginTx(ctx context.Context, options pgx.TxOptions) (pgx.Tx, error)
	Ping(ctx context.Context) error
	Close()
	Stats() poolStats
}

type pgxPoolBackend struct {
	*pgxpool.Pool
}

func (pool *pgxPoolBackend) Stats() poolStats {
	stats := pool.Stat()

	return poolStats{
		acquireCount:          stats.AcquireCount(),
		acquireDuration:       stats.AcquireDuration(),
		acquiredConns:         stats.AcquiredConns(),
		canceledAcquireCount:  stats.CanceledAcquireCount(),
		constructingConns:     stats.ConstructingConns(),
		emptyAcquireCount:     stats.EmptyAcquireCount(),
		emptyAcquireWaitTime:  stats.EmptyAcquireWaitTime(),
		idleConns:             stats.IdleConns(),
		maxConns:              stats.MaxConns(),
		maxIdleDestroyCount:   stats.MaxIdleDestroyCount(),
		maxLifetimeDestroyCnt: stats.MaxLifetimeDestroyCount(),
		newConnsCount:         stats.NewConnsCount(),
		totalConns:            stats.TotalConns(),
	}
}

type poolStats struct {
	acquireCount          int64
	acquireDuration       time.Duration
	acquiredConns         int32
	canceledAcquireCount  int64
	constructingConns     int32
	emptyAcquireCount     int64
	emptyAcquireWaitTime  time.Duration
	idleConns             int32
	maxConns              int32
	maxIdleDestroyCount   int64
	maxLifetimeDestroyCnt int64
	newConnsCount         int64
	totalConns            int32
}

// New создаёт оба пула, проверяет их роли и регистрирует telemetry. При
// частичной ошибке уже созданные ресурсы освобождаются.
func New(
	ctx context.Context,
	config Config,
	pipeline *telemetry.Telemetry,
) (*Database, error) {
	if ctx == nil {
		return nil, errors.New("postgres: context must not be nil")
	}
	if pipeline == nil {
		return nil, errors.New("postgres: telemetry must not be nil")
	}

	settings, err := normalizeConfig(config)
	if err != nil {
		return nil, err
	}

	tracer := pipeline.Tracer(instrumentationName)
	rw, err := newManagedPool(ctx, roleRW, settings.rw, tracer)
	if err != nil {
		return nil, err
	}
	ro, err := newManagedPool(ctx, roleRO, settings.ro, tracer)
	if err != nil {
		rw.backend.Close()
		return nil, err
	}

	database := &Database{
		rw:     rw,
		ro:     ro,
		retry:  settings.retry,
		tracer: tracer,
		sleep:  sleepWithContext,
	}
	database.instruments, err = newInstruments(pipeline.Meter(instrumentationName), rw, ro)
	if err != nil {
		ro.backend.Close()
		rw.backend.Close()
		return nil, err
	}

	return database, nil
}

// RW возвращает executor primary-пула. Записи и все транзакции выполняются
// только через этот executor.
func (database *Database) RW() Executor {
	if database == nil || database.rw == nil {
		return nil
	}

	return database.rw.executor
}

// RO возвращает executor replica-пула только для явно eventual-consistent
// чтений. Read-after-write сценарии должны использовать RW.
func (database *Database) RO() Executor {
	if database == nil || database.ro == nil {
		return nil
	}

	return database.ro.executor
}

// ReadinessDependencies возвращает независимые проверки обоих пулов для
// platform/runtime.Health.
func (database *Database) ReadinessDependencies() []serviceruntime.CriticalDependency {
	if database == nil {
		return []serviceruntime.CriticalDependency{}
	}

	return []serviceruntime.CriticalDependency{
		{
			Name: "postgres-rw",
			Check: func(ctx context.Context) error {
				return database.ping(ctx, database.rw)
			},
		},
		{
			Name: "postgres-ro",
			Check: func(ctx context.Context) error {
				return database.ping(ctx, database.ro)
			},
		},
	}
}

// Component связывает оба пула с platform/runtime lifecycle. Метод можно
// вызвать только один раз; Run блокируется до cancellation.
func (database *Database) Component(name string) (serviceruntime.Component, error) {
	if database == nil || database.rw == nil || database.ro == nil {
		return serviceruntime.Component{}, errors.New("postgres: database must not be nil")
	}
	if !database.componentCreated.CompareAndSwap(false, true) {
		return serviceruntime.Component{}, errors.New("postgres: component has already been created")
	}

	return serviceruntime.Component{
		Name: name,
		Run: func(ctx context.Context) error {
			if ctx == nil {
				return errors.New("postgres: component context must not be nil")
			}
			<-ctx.Done()

			return ctx.Err()
		},
		Shutdown: database.Close,
	}, nil
}

// Close снимает metrics callback и независимо закрывает оба пула. Если
// pgxpool не вернул захваченные соединения до deadline, Close возвращает
// ошибку context, сохраняя ограниченность общего shutdown.
func (database *Database) Close(ctx context.Context) error {
	if database == nil {
		return nil
	}
	if ctx == nil {
		return errors.New("postgres: shutdown context must not be nil")
	}

	database.closeOnce.Do(func() {
		var instrumentationErr error
		if database.instruments != nil {
			instrumentationErr = database.instruments.unregister()
		}

		database.closeErr = errors.Join(
			wrapOperation("unregister metrics", "", instrumentationErr),
			closePools(ctx, database.rw, database.ro),
		)
	})

	return database.closeErr
}

func newManagedPool(
	ctx context.Context,
	role poolRole,
	settings poolSettings,
	tracer trace.Tracer,
) (*managedPool, error) {
	config, err := buildPoolConfig(role, settings, tracer)
	if err != nil {
		return nil, err
	}

	connectCtx, cancel := context.WithTimeout(ctx, settings.connectTimeout)
	defer cancel()

	rawPool, err := pgxpool.NewWithConfig(connectCtx, config)
	if err != nil {
		return nil, wrapOperation("create", role, err)
	}
	pool := &managedPool{
		role: role,
		backend: &pgxPoolBackend{
			Pool: rawPool,
		},
		executor: newExecutor(rawPool, settings.queryTimeout),
	}
	if err := pool.backend.Ping(connectCtx); err != nil {
		pool.backend.Close()
		return nil, wrapOperation("ping", role, err)
	}
	if err := verifyPoolRole(connectCtx, pool); err != nil {
		pool.backend.Close()
		return nil, err
	}

	return pool, nil
}

func buildPoolConfig(
	role poolRole,
	settings poolSettings,
	tracer trace.Tracer,
) (*pgxpool.Config, error) {
	config, err := pgxpool.ParseConfig(settings.dsn.Reveal())
	if err != nil {
		return nil, wrapOperation(
			"parse configuration for",
			role,
			errors.Join(ErrInvalidConfig, err),
		)
	}
	if config.ConnConfig.RuntimeParams == nil {
		config.ConnConfig.RuntimeParams = make(map[string]string, 1)
	}
	config.ConnConfig.RuntimeParams["application_name"] = settings.applicationName

	config.MaxConns = settings.maxConns
	config.MinConns = settings.minConns
	config.MinIdleConns = settings.minIdleConns
	config.MaxConnLifetime = settings.maxConnLifetime
	config.MaxConnLifetimeJitter = settings.maxConnLifetimeJitter
	config.MaxConnIdleTime = settings.maxConnIdleTime
	config.HealthCheckPeriod = settings.healthCheckPeriod
	config.PingTimeout = settings.pingTimeout
	config.ConnConfig.ConnectTimeout = settings.connectTimeout
	config.ConnConfig.Tracer = &queryTracer{
		pool:   role,
		tracer: tracer,
	}

	return config, nil
}

func verifyPoolRole(ctx context.Context, pool *managedPool) error {
	var inRecovery bool
	var transactionReadOnly string
	err := pool.backend.QueryRow(
		ctx,
		"SELECT pg_is_in_recovery(), current_setting('transaction_read_only')",
	).Scan(&inRecovery, &transactionReadOnly)
	if err != nil {
		return wrapOperation("verify role for", pool.role, err)
	}

	matchesRW := pool.role == roleRW && !inRecovery && transactionReadOnly == "off"
	matchesRO := pool.role == roleRO && inRecovery && transactionReadOnly == "on"
	if !matchesRW && !matchesRO {
		return wrapOperation(
			"verify role for",
			pool.role,
			errors.Join(
				ErrInvalidConfig,
				fmt.Errorf("postgres: %s endpoint does not match expected server role", pool.role),
			),
		)
	}

	return nil
}

func (database *Database) ping(ctx context.Context, pool *managedPool) error {
	if ctx == nil {
		return errors.New("postgres: readiness context must not be nil")
	}
	if pool == nil || pool.backend == nil {
		return errors.New("postgres: readiness pool is not initialized")
	}

	return wrapOperation("ping", pool.role, pool.backend.Ping(ctx))
}

func closePools(ctx context.Context, pools ...*managedPool) error {
	type closeResult struct {
		role poolRole
	}

	results := make(chan closeResult, len(pools))
	started := 0
	for _, pool := range pools {
		if pool == nil || pool.backend == nil {
			continue
		}
		started++
		go func() {
			pool.backend.Close()
			results <- closeResult{role: pool.role}
		}()
	}

	closed := make(map[poolRole]struct{}, started)
	for len(closed) < started {
		select {
		case result := <-results:
			closed[result.role] = struct{}{}
		case <-ctx.Done():
			pending := []error{}
			for _, pool := range pools {
				if pool == nil {
					continue
				}
				if _, found := closed[pool.role]; !found {
					pending = append(
						pending,
						wrapOperation("close", pool.role, ctx.Err()),
					)
				}
			}

			return errors.Join(pending...)
		}
	}

	return nil
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

var (
	_ queryBackend = (*pgxpool.Pool)(nil)
	_ queryBackend = (pgx.Tx)(nil)
	_ poolBackend  = (*pgxPoolBackend)(nil)
)
