package postgres

import (
	"errors"
	"math"
	"strings"
	"testing"
	"time"

	serviceruntime "github.com/v0hmly/marketmesh/platform/runtime"
	"go.opentelemetry.io/otel/trace"
)

func TestNormalizeConfigValidatesApplicationName(t *testing.T) {
	t.Parallel()

	testCases := map[string]struct {
		applicationName string
		expected        string
		errorPart       string
	}{
		"empty": {
			errorPart: "required",
		},
		"whitespace": {
			applicationName: " \t\n ",
			errorPart:       "required",
		},
		"64 bytes": {
			applicationName: strings.Repeat("a", 64),
			errorPart:       "63 bytes",
		},
		"newline": {
			applicationName: "user\nworker",
			errorPart:       "printable ASCII",
		},
		"tab": {
			applicationName: "user\tworker",
			errorPart:       "printable ASCII",
		},
		"non-ASCII": {
			applicationName: "пользователь",
			errorPart:       "printable ASCII",
		},
		"one byte": {
			applicationName: "u",
			expected:        "u",
		},
		"63 bytes": {
			applicationName: strings.Repeat("a", 63),
			expected:        strings.Repeat("a", 63),
		},
		"trim": {
			applicationName: "  user  ",
			expected:        "user",
		},
	}

	for name, testCase := range testCases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			config := validConfig(t, testCase.applicationName)
			settings, err := normalizeConfig(config)
			if testCase.errorPart != "" {
				if !errors.Is(err, ErrInvalidConfig) {
					t.Fatalf("normalizeConfig() error = %v, want ErrInvalidConfig", err)
				}
				if !strings.Contains(err.Error(), testCase.errorPart) {
					t.Fatalf("normalizeConfig() error = %v, want %q", err, testCase.errorPart)
				}
				if strings.Contains(err.Error(), "private-password") {
					t.Fatalf("normalizeConfig() exposed DSN: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeConfig() error = %v", err)
			}
			if settings.rw.applicationName != testCase.expected ||
				settings.ro.applicationName != testCase.expected {
				t.Fatalf(
					"RW/RO application names = %q/%q, want %q",
					settings.rw.applicationName,
					settings.ro.applicationName,
					testCase.expected,
				)
			}
		})
	}
}

func TestBuildPoolConfigOverridesApplicationNameSources(t *testing.T) {
	t.Setenv("PGAPPNAME", "ambient-client")

	for name, dsn := range map[string]string{
		"ambient environment": "postgres://user:private-password@localhost/database",
		"DSN parameter":       "postgres://user:private-password@localhost/database?application_name=dsn-client",
	} {
		t.Run(name, func(t *testing.T) {
			settings, err := normalizeConfig(validConfig(t, "  canonical-client  "))
			if err != nil {
				t.Fatalf("normalizeConfig() error = %v", err)
			}
			settings.rw.dsn = validPoolConfig(t, dsn).DSN

			config, err := buildPoolConfig(
				roleRW,
				settings.rw,
				trace.NewNoopTracerProvider().Tracer(instrumentationName),
			)
			if err != nil {
				t.Fatalf("buildPoolConfig() error = %v", err)
			}
			if actual := config.ConnConfig.RuntimeParams["application_name"]; actual != "canonical-client" {
				t.Fatalf("application_name = %q, want canonical-client", actual)
			}
		})
	}
}

func TestNormalizeConfigValidatesPools(t *testing.T) {
	t.Parallel()

	testCases := map[string]struct {
		change    func(*PoolConfig)
		errorPart string
	}{
		"missing DSN": {
			change:    func(config *PoolConfig) { config.DSN = serviceruntime.Secret{} },
			errorPart: "DSN",
		},
		"max connections": {
			change:    func(config *PoolConfig) { config.MaxConns = 0 },
			errorPart: "max connections",
		},
		"negative min connections": {
			change:    func(config *PoolConfig) { config.MinConns = -1 },
			errorPart: "min connections",
		},
		"min connections over max": {
			change:    func(config *PoolConfig) { config.MinConns = config.MaxConns + 1 },
			errorPart: "min connections",
		},
		"negative min idle connections": {
			change:    func(config *PoolConfig) { config.MinIdleConns = -1 },
			errorPart: "min idle connections",
		},
		"connect timeout": {
			change:    func(config *PoolConfig) { config.ConnectTimeout = 0 },
			errorPart: "connect timeout",
		},
		"query timeout": {
			change:    func(config *PoolConfig) { config.QueryTimeout = -time.Second },
			errorPart: "query timeout",
		},
		"lifetime": {
			change:    func(config *PoolConfig) { config.MaxConnLifetime = 0 },
			errorPart: "lifetime",
		},
		"negative lifetime jitter": {
			change:    func(config *PoolConfig) { config.MaxConnLifetimeJitter = -time.Second },
			errorPart: "lifetime jitter",
		},
		"idle time over lifetime": {
			change: func(config *PoolConfig) {
				config.MaxConnIdleTime = config.MaxConnLifetime + time.Second
			},
			errorPart: "idle time",
		},
		"health check period": {
			change:    func(config *PoolConfig) { config.HealthCheckPeriod = 0 },
			errorPart: "health check period",
		},
		"ping timeout": {
			change:    func(config *PoolConfig) { config.PingTimeout = 0 },
			errorPart: "ping timeout",
		},
	}

	for name, testCase := range testCases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			config := validPoolConfig(t, "postgres://user:private-password@rw/database")
			testCase.change(&config)

			_, err := normalizePoolConfig(roleRW, config)
			if !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("normalizePoolConfig() error = %v, want ErrInvalidConfig", err)
			}
			if !strings.Contains(err.Error(), testCase.errorPart) {
				t.Fatalf("normalizePoolConfig() error = %v, want %q", err, testCase.errorPart)
			}
			if strings.Contains(err.Error(), "private-password") {
				t.Fatalf("normalizePoolConfig() exposed DSN: %v", err)
			}
		})
	}
}

func TestNormalizeRetryPolicy(t *testing.T) {
	t.Parallel()

	testCases := map[string]struct {
		policy    *RetryPolicy
		errorPart string
		enabled   bool
	}{
		"disabled": {},
		"valid": {
			policy: &RetryPolicy{
				MaxAttempts:       3,
				InitialBackoff:    10 * time.Millisecond,
				MaxBackoff:        100 * time.Millisecond,
				BackoffMultiplier: 2,
			},
			enabled: true,
		},
		"attempts": {
			policy: &RetryPolicy{
				MaxAttempts:       1,
				InitialBackoff:    time.Millisecond,
				MaxBackoff:        time.Second,
				BackoffMultiplier: 2,
			},
			errorPart: "attempts",
		},
		"initial backoff": {
			policy: &RetryPolicy{
				MaxAttempts:       2,
				MaxBackoff:        time.Second,
				BackoffMultiplier: 2,
			},
			errorPart: "initial backoff",
		},
		"max backoff": {
			policy: &RetryPolicy{
				MaxAttempts:       2,
				InitialBackoff:    time.Second,
				MaxBackoff:        time.Millisecond,
				BackoffMultiplier: 2,
			},
			errorPart: "max backoff",
		},
		"NaN multiplier": {
			policy: &RetryPolicy{
				MaxAttempts:       2,
				InitialBackoff:    time.Millisecond,
				MaxBackoff:        time.Second,
				BackoffMultiplier: math.NaN(),
			},
			errorPart: "multiplier",
		},
	}

	for name, testCase := range testCases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			settings, err := normalizeRetryPolicy(testCase.policy)
			if testCase.errorPart != "" {
				if !errors.Is(err, ErrInvalidConfig) ||
					!strings.Contains(err.Error(), testCase.errorPart) {
					t.Fatalf("normalizeRetryPolicy() error = %v, want %q", err, testCase.errorPart)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeRetryPolicy() error = %v", err)
			}
			if settings.enabled != testCase.enabled {
				t.Fatalf("enabled = %t, want %t", settings.enabled, testCase.enabled)
			}
		})
	}
}

func TestRetryBackoffIsBounded(t *testing.T) {
	t.Parallel()

	settings := retrySettings{
		initialBackoff:    10 * time.Millisecond,
		maxBackoff:        25 * time.Millisecond,
		backoffMultiplier: 2,
	}

	for attempt, expected := range map[int]time.Duration{
		1: 10 * time.Millisecond,
		2: 20 * time.Millisecond,
		3: 25 * time.Millisecond,
		8: 25 * time.Millisecond,
	} {
		if actual := settings.backoff(attempt); actual != expected {
			t.Errorf("backoff(%d) = %v, want %v", attempt, actual, expected)
		}
	}
}

func validPoolConfig(t *testing.T, dsn string) PoolConfig {
	t.Helper()

	secret, err := serviceruntime.MapEnv(map[string]string{
		"DATABASE_DSN": dsn,
	}).Secret("DATABASE_DSN", true)
	if err != nil {
		t.Fatalf("create test DSN: %v", err)
	}

	return PoolConfig{
		DSN:                   secret,
		MaxConns:              8,
		MinConns:              1,
		MinIdleConns:          1,
		ConnectTimeout:        time.Second,
		QueryTimeout:          time.Second,
		MaxConnLifetime:       30 * time.Minute,
		MaxConnLifetimeJitter: time.Minute,
		MaxConnIdleTime:       5 * time.Minute,
		HealthCheckPeriod:     30 * time.Second,
		PingTimeout:           time.Second,
	}
}

func validConfig(t *testing.T, applicationName string) Config {
	t.Helper()

	return Config{
		ApplicationName: applicationName,
		RW: validPoolConfig(
			t,
			"postgres://user:private-password@rw/database",
		),
		RO: validPoolConfig(
			t,
			"postgres://user:private-password@ro/database",
		),
	}
}
