//go:build integration

package redis

import (
	"context"
	"errors"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	serviceruntime "github.com/v0hmly/marketmesh/platform/runtime"
	"github.com/v0hmly/marketmesh/platform/testkit"
	integrationtest "github.com/v0hmly/marketmesh/platform/testkit/integration"
)

func TestIntegrationIndependentRedisClient(t *testing.T) {
	values := integrationtest.EnvOrSkip(
		t,
		"MARKETMESH_REDIS_ROLE",
		"MARKETMESH_REDIS_ADDRESS",
		"MARKETMESH_REDIS_PASSWORD",
	)
	role := Role(values["MARKETMESH_REDIS_ROLE"])
	environment := serviceruntime.MapEnv(map[string]string{
		"ADDRESS":  values["MARKETMESH_REDIS_ADDRESS"],
		"PASSWORD": values["MARKETMESH_REDIS_PASSWORD"],
	})
	address, err := environment.Secret("ADDRESS", true)
	if err != nil {
		t.Fatal(err)
	}
	password, err := environment.Secret("PASSWORD", true)
	if err != nil {
		t.Fatal(err)
	}
	config := integrationConfig(role, address, password)
	client, err := New(t.Context(), config, testkit.NoopTelemetry(t))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := client.Close(shutdownCtx); err != nil {
			t.Errorf("Close() error = %v", err)
		}
	})

	key := "mm20:integration:" + string(role)
	value := "value-" + string(role)
	if err := client.Execute(t.Context(), func(ctx context.Context, commands goredis.Cmdable) error {
		return commands.Set(ctx, key, value, time.Minute).Err()
	}); err != nil {
		t.Fatalf("SET error = %v", err)
	}
	if err := client.ExecuteIdempotent(
		t.Context(),
		func(ctx context.Context, commands goredis.Cmdable) error {
			actual, err := commands.Get(ctx, key).Result()
			if err != nil {
				return err
			}
			if actual != value {
				return errors.New("unexpected Redis value")
			}

			return nil
		},
	); err != nil {
		t.Fatalf("GET error = %v", err)
	}
	if err := client.ReadinessDependencies()[0].Check(t.Context()); err != nil {
		t.Fatalf("readiness error = %v", err)
	}

	cancelCtx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	err = client.Execute(cancelCtx, func(ctx context.Context, commands goredis.Cmdable) error {
		return commands.BLPop(ctx, 0, key+":cancel").Err()
	})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("canceled BLPOP error = %v, want DeadlineExceeded", err)
	}

	blockCtx, stopBlock := context.WithCancel(t.Context())
	blocked := make(chan error, 1)
	go func() {
		blocked <- client.Execute(blockCtx, func(ctx context.Context, commands goredis.Cmdable) error {
			return commands.BLPop(ctx, 0, key+":pool").Err()
		})
	}()
	testkit.Eventually(t, time.Second, 10*time.Millisecond, func() bool {
		err = client.Execute(t.Context(), func(ctx context.Context, commands goredis.Cmdable) error {
			return commands.Get(ctx, key).Err()
		})
		return errors.Is(err, goredis.ErrPoolTimeout) || errors.Is(err, goredis.ErrPoolExhausted)
	})
	if !errors.Is(err, goredis.ErrPoolTimeout) && !errors.Is(err, goredis.ErrPoolExhausted) {
		t.Fatalf("pool exhaustion error = %v", err)
	}
	stopBlock()
	select {
	case err := <-blocked:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("blocked command error = %v, want Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("blocked command did not observe cancellation")
	}
	testkit.Eventually(t, 3*time.Second, 25*time.Millisecond, func() bool {
		err = client.ReadinessDependencies()[0].Check(t.Context())
		return err == nil
	})

	if err := client.Execute(t.Context(), func(ctx context.Context, commands goredis.Cmdable) error {
		return commands.Del(ctx, key).Err()
	}); err != nil {
		t.Fatalf("DEL error = %v", err)
	}
}

func integrationConfig(
	role Role,
	address serviceruntime.Secret,
	password serviceruntime.Secret,
) Config {
	return Config{
		Role:    role,
		Address: address,
		Authentication: AuthenticationConfig{
			Password: password,
		},
		Transport: TransportConfig{PlaintextException: &PlaintextException{
			Reason: "MM-9 isolated internal Docker network",
		}},
		Pool: PoolConfig{
			Size:                  1,
			MaxIdleConns:          1,
			MaxActiveConns:        1,
			MaxConcurrentDials:    1,
			ConnMaxIdleTime:       time.Minute,
			ConnMaxLifetime:       time.Hour,
			ConnMaxLifetimeJitter: time.Minute,
		},
		Timeouts: TimeoutConfig{
			Connect:   2 * time.Second,
			Command:   2 * time.Second,
			Pool:      75 * time.Millisecond,
			Read:      time.Second,
			Write:     time.Second,
			Readiness: 500 * time.Millisecond,
			Shutdown:  time.Second,
		},
		Retry: &RetryPolicy{
			MaxAttempts:       2,
			InitialBackoff:    10 * time.Millisecond,
			MaxBackoff:        20 * time.Millisecond,
			BackoffMultiplier: 2,
		},
	}
}
