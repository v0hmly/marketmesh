package redis

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	serviceruntime "github.com/v0hmly/marketmesh/platform/runtime"
)

type fakeBackend struct {
	ping         func(context.Context) error
	stats        *goredis.PoolStats
	closeErr     error
	closeStarted chan struct{}
	closeRelease chan struct{}
	closeCalls   atomic.Int32
	hookMutex    sync.Mutex
	hooks        []goredis.Hook
}

func (backend *fakeBackend) AddHook(hook goredis.Hook) {
	backend.hookMutex.Lock()
	defer backend.hookMutex.Unlock()
	backend.hooks = append(backend.hooks, hook)
}

func (backend *fakeBackend) Ping(ctx context.Context) *goredis.StatusCmd {
	var err error
	if backend.ping != nil {
		err = backend.ping(ctx)
	}

	return goredis.NewStatusResult("PONG", err)
}

func (backend *fakeBackend) PoolStats() *goredis.PoolStats {
	return backend.stats
}

func (backend *fakeBackend) Close() error {
	backend.closeCalls.Add(1)
	if backend.closeStarted != nil {
		select {
		case backend.closeStarted <- struct{}{}:
		default:
		}
	}
	if backend.closeRelease != nil {
		<-backend.closeRelease
	}

	return backend.closeErr
}

func newTestClient(t *testing.T, backend clientBackend) *Client {
	t.Helper()
	raw := goredis.NewClient(&goredis.Options{Addr: "127.0.0.1:1"})
	t.Cleanup(func() { _ = raw.Close() })

	return &Client{
		role:     RoleEdge,
		commands: &commandFacade{Cmdable: raw},
		backend:  backend,
		timeouts: TimeoutConfig{
			Command:   time.Second,
			Readiness: time.Second,
			Shutdown:  time.Second,
		},
		retry: retrySettings{
			enabled:           true,
			maxAttempts:       3,
			initialBackoff:    time.Millisecond,
			maxBackoff:        10 * time.Millisecond,
			backoffMultiplier: 2,
		},
		instruments: &instruments{},
		sleep:       sleepWithContext,
		closeDone:   make(chan struct{}),
	}
}

func testSecret(t *testing.T, value string) serviceruntime.Secret {
	t.Helper()
	secret, err := serviceruntime.MapEnv(map[string]string{"VALUE": value}).Secret("VALUE", false)
	if err != nil {
		t.Fatalf("create test secret: %v", err)
	}

	return secret
}

func validConfig(t *testing.T) Config {
	t.Helper()

	return Config{
		Role:    RoleEdge,
		Address: testSecret(t, "edge-state:6379"),
		Authentication: AuthenticationConfig{
			Password: testSecret(t, "private-password"),
		},
		Transport: TransportConfig{
			PlaintextException: &PlaintextException{
				Reason: "isolated internal test network",
			},
		},
		Pool: PoolConfig{
			Size:                  4,
			MinIdleConns:          1,
			MaxIdleConns:          2,
			MaxActiveConns:        4,
			MaxConcurrentDials:    2,
			ConnMaxIdleTime:       time.Minute,
			ConnMaxLifetime:       time.Hour,
			ConnMaxLifetimeJitter: time.Minute,
		},
		Timeouts: TimeoutConfig{
			Connect:   time.Second,
			Command:   2 * time.Second,
			Pool:      100 * time.Millisecond,
			Read:      time.Second,
			Write:     time.Second,
			Readiness: 500 * time.Millisecond,
			Shutdown:  time.Second,
		},
		Retry: &RetryPolicy{
			MaxAttempts:       3,
			InitialBackoff:    10 * time.Millisecond,
			MaxBackoff:        100 * time.Millisecond,
			BackoffMultiplier: 2,
		},
	}
}
