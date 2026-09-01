//go:build integration

package postgres

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	serviceruntime "github.com/v0hmly/marketmesh/platform/runtime"
	"github.com/v0hmly/marketmesh/platform/telemetry"
)

func TestIntegrationRWROTransactionsAndReadiness(t *testing.T) {
	database := newIntegrationDatabase(t)

	marker := fmt.Sprintf("mm18-commit-%d", time.Now().UnixNano())
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := database.RW().Exec(
			cleanupCtx,
			"DELETE FROM public.infra_smoke WHERE marker = $1",
			marker,
		); err != nil {
			t.Errorf("cleanup integration marker: %v", err)
		}
	})

	err := database.WithinTransaction(
		t.Context(),
		TransactionOptions{Isolation: IsolationSerializable},
		func(ctx context.Context, executor Executor) error {
			_, err := executor.Exec(
				ctx,
				"INSERT INTO public.infra_smoke (marker) VALUES ($1)",
				marker,
			)
			return err
		},
	)
	if err != nil {
		t.Fatalf("commit transaction: %v", err)
	}

	for name, executor := range map[string]Executor{
		"rw primary": database.RW(),
		"ro replica": database.RO(),
	} {
		t.Run(name, func(t *testing.T) {
			var count int
			err := executor.QueryRow(
				t.Context(),
				"SELECT count(*) FROM public.infra_smoke WHERE marker = $1",
				marker,
			).Scan(&count)
			if err != nil {
				t.Fatalf("query committed marker: %v", err)
			}
			if count != 1 {
				t.Fatalf("committed marker count = %d, want 1", count)
			}
		})
	}

	for _, dependency := range database.ReadinessDependencies() {
		if err := dependency.Check(t.Context()); err != nil {
			t.Errorf("readiness %s: %v", dependency.Name, err)
		}
	}
}

func TestIntegrationApplicationName(t *testing.T) {
	database := newIntegrationDatabase(t)

	for name, executor := range map[string]Executor{
		"rw primary": database.RW(),
		"ro replica": database.RO(),
	} {
		t.Run(name, func(t *testing.T) {
			var applicationName string
			if err := executor.QueryRow(t.Context(), "SHOW application_name").Scan(
				&applicationName,
			); err != nil {
				t.Fatalf("show application_name: %v", err)
			}
			if applicationName != "marketmesh-postgres-integration" {
				t.Fatalf(
					"application_name = %q, want marketmesh-postgres-integration",
					applicationName,
				)
			}
		})
	}
}

func TestIntegrationRollbackReadOnlyAndCancellation(t *testing.T) {
	database := newIntegrationDatabase(t)

	rollbackMarker := fmt.Sprintf("mm18-rollback-%d", time.Now().UnixNano())
	rollbackCause := errors.New("force rollback")
	err := database.WithinTransaction(
		t.Context(),
		TransactionOptions{},
		func(ctx context.Context, executor Executor) error {
			if _, err := executor.Exec(
				ctx,
				"INSERT INTO public.infra_smoke (marker) VALUES ($1)",
				rollbackMarker,
			); err != nil {
				return err
			}
			return rollbackCause
		},
	)
	if !errors.Is(err, rollbackCause) {
		t.Fatalf("rollback transaction error = %v, want rollback cause", err)
	}
	var rollbackCount int
	if err := database.RW().QueryRow(
		t.Context(),
		"SELECT count(*) FROM public.infra_smoke WHERE marker = $1",
		rollbackMarker,
	).Scan(&rollbackCount); err != nil {
		t.Fatalf("query rolled back marker: %v", err)
	}
	if rollbackCount != 0 {
		t.Fatalf("rolled back marker count = %d, want 0", rollbackCount)
	}

	readOnlyMarker := fmt.Sprintf("mm18-readonly-%d", time.Now().UnixNano())
	err = database.WithinTransaction(
		t.Context(),
		TransactionOptions{ReadOnly: true},
		func(ctx context.Context, executor Executor) error {
			_, err := executor.Exec(
				ctx,
				"INSERT INTO public.infra_smoke (marker) VALUES ($1)",
				readOnlyMarker,
			)
			return err
		},
	)
	assertPostgresCode(t, err, "25006")

	_, err = database.RO().Exec(
		t.Context(),
		"INSERT INTO public.infra_smoke (marker) VALUES ($1)",
		readOnlyMarker,
	)
	assertPostgresCode(t, err, "25006")

	queryCtx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	_, err = database.RW().Exec(queryCtx, "SELECT pg_sleep(10)")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("canceled query error = %v, want DeadlineExceeded", err)
	}
}

func newIntegrationDatabase(t *testing.T) *Database {
	t.Helper()

	rwDSN, rwFound := os.LookupEnv("MARKETMESH_POSTGRES_RW_DSN")
	roDSN, roFound := os.LookupEnv("MARKETMESH_POSTGRES_RO_DSN")
	if !rwFound || !roFound {
		t.Skip("integration DSNs are not configured; run task postgres:integration")
	}
	secrets := serviceruntime.MapEnv(map[string]string{
		"RW_DSN": rwDSN,
		"RO_DSN": roDSN,
	})
	rwSecret, err := secrets.Secret("RW_DSN", true)
	if err != nil {
		t.Fatalf("read RW test DSN: %v", err)
	}
	roSecret, err := secrets.Secret("RO_DSN", true)
	if err != nil {
		t.Fatalf("read RO test DSN: %v", err)
	}

	poolConfig := func(dsn serviceruntime.Secret) PoolConfig {
		return PoolConfig{
			DSN:                   dsn,
			MaxConns:              4,
			MinConns:              1,
			MinIdleConns:          1,
			ConnectTimeout:        5 * time.Second,
			QueryTimeout:          2 * time.Second,
			MaxConnLifetime:       10 * time.Minute,
			MaxConnLifetimeJitter: time.Minute,
			MaxConnIdleTime:       2 * time.Minute,
			HealthCheckPeriod:     30 * time.Second,
			PingTimeout:           time.Second,
		}
	}
	database, err := New(
		t.Context(),
		Config{
			ApplicationName: "marketmesh-postgres-integration",
			RW:              poolConfig(rwSecret),
			RO:              poolConfig(roSecret),
			Retry: &RetryPolicy{
				MaxAttempts:       3,
				InitialBackoff:    10 * time.Millisecond,
				MaxBackoff:        100 * time.Millisecond,
				BackoffMultiplier: 2,
			},
		},
		telemetry.NewNoop(),
	)
	if err != nil {
		t.Fatalf("New() integration database: %v", err)
	}
	t.Cleanup(func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := database.Close(shutdownCtx); err != nil {
			t.Errorf("close integration database: %v", err)
		}
	})

	return database
}

func assertPostgresCode(t *testing.T, err error, expected string) {
	t.Helper()

	var postgresErr *pgconn.PgError
	if !errors.As(err, &postgresErr) || postgresErr.Code != expected {
		t.Fatalf("PostgreSQL error = %v, want SQLSTATE %s", err, expected)
	}
}
