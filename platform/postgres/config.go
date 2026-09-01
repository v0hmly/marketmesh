package postgres

import (
	"errors"
	"fmt"
	"math"
	"time"

	serviceruntime "github.com/v0hmly/marketmesh/platform/runtime"
)

const maxRetryAttempts = 5

// Config задаёт независимые RW/RO-пулы и необязательные повторы транзакций.
type Config struct {
	RW    PoolConfig
	RO    PoolConfig
	Retry *RetryPolicy
}

// PoolConfig задаёт безопасные пределы одного pgxpool.Pool. DSN должен
// поступать как runtime.Secret и никогда не попадать в errors или telemetry.
type PoolConfig struct {
	DSN                   serviceruntime.Secret
	MaxConns              int32
	MinConns              int32
	MinIdleConns          int32
	ConnectTimeout        time.Duration
	QueryTimeout          time.Duration
	MaxConnLifetime       time.Duration
	MaxConnLifetimeJitter time.Duration
	MaxConnIdleTime       time.Duration
	HealthCheckPeriod     time.Duration
	PingTimeout           time.Duration
}

// RetryPolicy ограничивает повторы всей явно идемпотентной транзакции.
// Повторяются только SQLSTATE 40001 и 40P01.
type RetryPolicy struct {
	MaxAttempts       int
	InitialBackoff    time.Duration
	MaxBackoff        time.Duration
	BackoffMultiplier float64
}

type settings struct {
	rw    poolSettings
	ro    poolSettings
	retry retrySettings
}

type poolSettings struct {
	dsn                   serviceruntime.Secret
	maxConns              int32
	minConns              int32
	minIdleConns          int32
	connectTimeout        time.Duration
	queryTimeout          time.Duration
	maxConnLifetime       time.Duration
	maxConnLifetimeJitter time.Duration
	maxConnIdleTime       time.Duration
	healthCheckPeriod     time.Duration
	pingTimeout           time.Duration
}

type retrySettings struct {
	enabled           bool
	maxAttempts       int
	initialBackoff    time.Duration
	maxBackoff        time.Duration
	backoffMultiplier float64
}

func normalizeConfig(config Config) (settings, error) {
	rw, err := normalizePoolConfig(roleRW, config.RW)
	if err != nil {
		return settings{}, err
	}
	ro, err := normalizePoolConfig(roleRO, config.RO)
	if err != nil {
		return settings{}, err
	}
	retry, err := normalizeRetryPolicy(config.Retry)
	if err != nil {
		return settings{}, err
	}

	return settings{
		rw:    rw,
		ro:    ro,
		retry: retry,
	}, nil
}

func normalizePoolConfig(role poolRole, config PoolConfig) (poolSettings, error) {
	if !config.DSN.Present() {
		return poolSettings{}, invalidConfig(role, "DSN is required")
	}
	if config.MaxConns <= 0 {
		return poolSettings{}, invalidConfig(role, "max connections must be positive")
	}
	if config.MinConns < 0 || config.MinConns > config.MaxConns {
		return poolSettings{}, invalidConfig(role, "min connections must be between zero and max connections")
	}
	if config.MinIdleConns < 0 || config.MinIdleConns > config.MaxConns {
		return poolSettings{}, invalidConfig(role, "min idle connections must be between zero and max connections")
	}
	if config.ConnectTimeout <= 0 {
		return poolSettings{}, invalidConfig(role, "connect timeout must be positive")
	}
	if config.QueryTimeout <= 0 {
		return poolSettings{}, invalidConfig(role, "query timeout must be positive")
	}
	if config.MaxConnLifetime <= 0 {
		return poolSettings{}, invalidConfig(role, "max connection lifetime must be positive")
	}
	if config.MaxConnLifetimeJitter < 0 || config.MaxConnLifetimeJitter > config.MaxConnLifetime {
		return poolSettings{}, invalidConfig(
			role,
			"max connection lifetime jitter must be between zero and max connection lifetime",
		)
	}
	if config.MaxConnIdleTime <= 0 || config.MaxConnIdleTime > config.MaxConnLifetime {
		return poolSettings{}, invalidConfig(
			role,
			"max connection idle time must be positive and not exceed max connection lifetime",
		)
	}
	if config.HealthCheckPeriod <= 0 {
		return poolSettings{}, invalidConfig(role, "health check period must be positive")
	}
	if config.PingTimeout <= 0 {
		return poolSettings{}, invalidConfig(role, "ping timeout must be positive")
	}

	return poolSettings{
		dsn:                   config.DSN,
		maxConns:              config.MaxConns,
		minConns:              config.MinConns,
		minIdleConns:          config.MinIdleConns,
		connectTimeout:        config.ConnectTimeout,
		queryTimeout:          config.QueryTimeout,
		maxConnLifetime:       config.MaxConnLifetime,
		maxConnLifetimeJitter: config.MaxConnLifetimeJitter,
		maxConnIdleTime:       config.MaxConnIdleTime,
		healthCheckPeriod:     config.HealthCheckPeriod,
		pingTimeout:           config.PingTimeout,
	}, nil
}

func normalizeRetryPolicy(policy *RetryPolicy) (retrySettings, error) {
	if policy == nil {
		return retrySettings{}, nil
	}
	if policy.MaxAttempts < 2 || policy.MaxAttempts > maxRetryAttempts {
		return retrySettings{}, invalidConfig(
			"",
			fmt.Sprintf("retry max attempts must be between 2 and %d", maxRetryAttempts),
		)
	}
	if policy.InitialBackoff <= 0 {
		return retrySettings{}, invalidConfig("", "retry initial backoff must be positive")
	}
	if policy.MaxBackoff < policy.InitialBackoff {
		return retrySettings{}, invalidConfig(
			"",
			"retry max backoff must not be less than initial backoff",
		)
	}
	invalidMultiplier := math.IsNaN(policy.BackoffMultiplier) ||
		math.IsInf(policy.BackoffMultiplier, 0) ||
		policy.BackoffMultiplier < 1
	if invalidMultiplier {
		return retrySettings{}, invalidConfig(
			"",
			"retry backoff multiplier must be finite and at least one",
		)
	}

	return retrySettings{
		enabled:           true,
		maxAttempts:       policy.MaxAttempts,
		initialBackoff:    policy.InitialBackoff,
		maxBackoff:        policy.MaxBackoff,
		backoffMultiplier: policy.BackoffMultiplier,
	}, nil
}

func invalidConfig(role poolRole, message string) error {
	if role == "" {
		return errors.Join(ErrInvalidConfig, errors.New("postgres: "+message))
	}

	return errors.Join(
		ErrInvalidConfig,
		fmt.Errorf("postgres: %s pool %s", role, message),
	)
}
