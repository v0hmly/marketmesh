package postgres

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func TestWithinTransactionCommitsAndPassesRWExecutor(t *testing.T) {
	t.Parallel()

	transaction := &fakeTx{}
	pool := &fakePool{transactions: []*fakeTx{transaction}}
	database := newTestDatabase(pool, nil, time.Second)

	var callbackExecutor Executor
	err := database.WithinTransaction(
		t.Context(),
		TransactionOptions{
			Isolation: IsolationSerializable,
			ReadOnly:  true,
		},
		func(_ context.Context, executor Executor) error {
			callbackExecutor = executor
			return nil
		},
	)
	if err != nil {
		t.Fatalf("WithinTransaction() error = %v", err)
	}
	if callbackExecutor == nil || callbackExecutor == database.RW() {
		t.Fatal("callback did not receive a transaction-scoped executor")
	}
	if transaction.commitCalls.Load() != 1 || transaction.rollbackCalls.Load() != 0 {
		t.Fatalf(
			"commit/rollback calls = %d/%d, want 1/0",
			transaction.commitCalls.Load(),
			transaction.rollbackCalls.Load(),
		)
	}
	options := pool.options()
	if len(options) != 1 ||
		options[0].IsoLevel != pgx.Serializable ||
		options[0].AccessMode != pgx.ReadOnly {
		t.Fatalf("BeginTx() options = %+v", options)
	}
}

func TestWithinTransactionRollsBackCallbackError(t *testing.T) {
	t.Parallel()

	callbackErr := errors.New("callback failed")
	rollbackErr := errors.New("rollback failed")
	transaction := &fakeTx{rollbackErr: rollbackErr}
	database := newTestDatabase(
		&fakePool{transactions: []*fakeTx{transaction}},
		nil,
		time.Second,
	)

	err := database.WithinTransaction(
		t.Context(),
		TransactionOptions{},
		func(context.Context, Executor) error { return callbackErr },
	)
	if !errors.Is(err, callbackErr) || !errors.Is(err, rollbackErr) {
		t.Fatalf("WithinTransaction() error = %v, want callback and rollback errors", err)
	}
	if transaction.commitCalls.Load() != 0 || transaction.rollbackCalls.Load() != 1 {
		t.Fatalf(
			"commit/rollback calls = %d/%d, want 0/1",
			transaction.commitCalls.Load(),
			transaction.rollbackCalls.Load(),
		)
	}
}

func TestWithinTransactionRollsBackAndPreservesPanic(t *testing.T) {
	t.Parallel()

	transaction := &fakeTx{}
	database := newTestDatabase(
		&fakePool{transactions: []*fakeTx{transaction}},
		nil,
		time.Second,
	)
	panicValue := "panic-value-must-not-be-logged"

	var recovered any
	func() {
		defer func() {
			recovered = recover()
		}()
		_ = database.WithinTransaction(
			t.Context(),
			TransactionOptions{},
			func(context.Context, Executor) error {
				panic(panicValue)
			},
		)
	}()

	if recovered != panicValue {
		t.Fatalf("recovered = %v, want original panic", recovered)
	}
	if transaction.rollbackCalls.Load() != 1 {
		t.Fatalf("rollback calls = %d, want 1", transaction.rollbackCalls.Load())
	}
}

func TestWithinTransactionRejectsNestedCall(t *testing.T) {
	t.Parallel()

	transaction := &fakeTx{}
	database := newTestDatabase(
		&fakePool{transactions: []*fakeTx{transaction}},
		nil,
		time.Second,
	)

	err := database.WithinTransaction(
		t.Context(),
		TransactionOptions{},
		func(ctx context.Context, _ Executor) error {
			return database.WithinTransaction(
				ctx,
				TransactionOptions{},
				func(context.Context, Executor) error { return nil },
			)
		},
	)
	if !errors.Is(err, ErrNestedTransaction) {
		t.Fatalf("WithinTransaction() error = %v, want ErrNestedTransaction", err)
	}
	if transaction.rollbackCalls.Load() != 1 {
		t.Fatalf("rollback calls = %d, want 1", transaction.rollbackCalls.Load())
	}
}

func TestWithinTransactionRollbackIgnoresCanceledRequestContext(t *testing.T) {
	t.Parallel()

	transaction := &fakeTx{}
	database := newTestDatabase(
		&fakePool{transactions: []*fakeTx{transaction}},
		nil,
		time.Second,
	)
	ctx, cancel := context.WithCancel(t.Context())

	err := database.WithinTransaction(
		ctx,
		TransactionOptions{},
		func(context.Context, Executor) error {
			cancel()
			return context.Canceled
		},
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("WithinTransaction() error = %v, want context.Canceled", err)
	}
	if transaction.rollbackCtxErr != nil {
		t.Fatalf("rollback context error = %v, want active cleanup context", transaction.rollbackCtxErr)
	}
}

func TestWithinTransactionRetriesOnlySafePostgresErrors(t *testing.T) {
	t.Parallel()

	for name, code := range map[string]string{
		"serialization failure": serializationFailureCode,
		"deadlock detected":     deadlockDetectedCode,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			first := &fakeTx{}
			second := &fakeTx{}
			pool := &fakePool{transactions: []*fakeTx{first, second}}
			database := newTestDatabase(pool, nil, time.Second)
			database.retry = retrySettings{
				enabled:           true,
				maxAttempts:       3,
				initialBackoff:    time.Millisecond,
				maxBackoff:        time.Second,
				backoffMultiplier: 2,
			}
			var sleepCalls atomic.Int32
			database.sleep = func(context.Context, time.Duration) error {
				sleepCalls.Add(1)
				return nil
			}
			var callbackCalls atomic.Int32

			err := database.WithinTransaction(
				t.Context(),
				TransactionOptions{Idempotent: true},
				func(context.Context, Executor) error {
					if callbackCalls.Add(1) == 1 {
						return &pgconn.PgError{Code: code}
					}
					return nil
				},
			)
			if err != nil {
				t.Fatalf("WithinTransaction() error = %v", err)
			}
			if callbackCalls.Load() != 2 || sleepCalls.Load() != 1 {
				t.Fatalf(
					"callback/sleep calls = %d/%d, want 2/1",
					callbackCalls.Load(),
					sleepCalls.Load(),
				)
			}
			if first.rollbackCalls.Load() != 1 || second.commitCalls.Load() != 1 {
				t.Fatalf(
					"first rollback / second commit = %d/%d, want 1/1",
					first.rollbackCalls.Load(),
					second.commitCalls.Load(),
				)
			}
		})
	}
}

func TestWithinTransactionDoesNotRetryWithoutExplicitIdempotency(t *testing.T) {
	t.Parallel()

	transaction := &fakeTx{}
	pool := &fakePool{transactions: []*fakeTx{transaction}}
	database := newTestDatabase(pool, nil, time.Second)
	database.retry = retrySettings{
		enabled:           true,
		maxAttempts:       3,
		initialBackoff:    time.Millisecond,
		maxBackoff:        time.Second,
		backoffMultiplier: 2,
	}
	postgresErr := &pgconn.PgError{Code: serializationFailureCode}

	err := database.WithinTransaction(
		t.Context(),
		TransactionOptions{},
		func(context.Context, Executor) error { return postgresErr },
	)
	if !errors.Is(err, postgresErr) {
		t.Fatalf("WithinTransaction() error = %v, want original PostgreSQL error", err)
	}
	if len(pool.options()) != 1 {
		t.Fatalf("transaction attempts = %d, want 1", len(pool.options()))
	}
}

func TestWithinTransactionReturnsRetryExhaustedAndPreservesPgError(t *testing.T) {
	t.Parallel()

	transactions := []*fakeTx{{}, {}, {}}
	database := newTestDatabase(
		&fakePool{transactions: transactions},
		nil,
		time.Second,
	)
	database.retry = retrySettings{
		enabled:           true,
		maxAttempts:       len(transactions),
		initialBackoff:    time.Millisecond,
		maxBackoff:        time.Second,
		backoffMultiplier: 2,
	}
	database.sleep = func(context.Context, time.Duration) error { return nil }
	postgresErr := &pgconn.PgError{Code: deadlockDetectedCode}

	err := database.WithinTransaction(
		t.Context(),
		TransactionOptions{Idempotent: true},
		func(context.Context, Executor) error { return postgresErr },
	)
	if !errors.Is(err, ErrRetryExhausted) {
		t.Fatalf("WithinTransaction() error = %v, want ErrRetryExhausted", err)
	}
	var preserved *pgconn.PgError
	if !errors.As(err, &preserved) || preserved.Code != deadlockDetectedCode {
		t.Fatalf("WithinTransaction() did not preserve PgError: %v", err)
	}
}

func TestWithinTransactionPreservesCommitError(t *testing.T) {
	t.Parallel()

	commitErr := &pgconn.PgError{Code: "23505"}
	transaction := &fakeTx{commitErr: commitErr}
	database := newTestDatabase(
		&fakePool{transactions: []*fakeTx{transaction}},
		nil,
		time.Second,
	)

	err := database.WithinTransaction(
		t.Context(),
		TransactionOptions{},
		func(context.Context, Executor) error { return nil },
	)
	var preserved *pgconn.PgError
	if !errors.As(err, &preserved) || preserved.Code != commitErr.Code {
		t.Fatalf("WithinTransaction() error = %v, want preserved commit PgError", err)
	}
}

func TestNormalizeTransactionOptions(t *testing.T) {
	t.Parallel()

	_, options, err := normalizeTransactionOptions(TransactionOptions{})
	if err != nil {
		t.Fatalf("normalizeTransactionOptions() error = %v", err)
	}
	if options.IsoLevel != pgx.ReadCommitted || options.AccessMode != pgx.ReadWrite {
		t.Fatalf("default options = %+v", options)
	}

	_, _, err = normalizeTransactionOptions(TransactionOptions{Isolation: "unsafe"})
	if !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("invalid isolation error = %v, want ErrInvalidConfig", err)
	}
}
