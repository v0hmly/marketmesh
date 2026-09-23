package postgres

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

const (
	serializationFailureCode = "40001"
	deadlockDetectedCode     = "40P01"
)

// IsolationLevel задаёт поддерживаемый PostgreSQL isolation level.
type IsolationLevel string

const (
	// IsolationReadCommitted — уровень PostgreSQL по умолчанию.
	IsolationReadCommitted IsolationLevel = "read_committed"

	// IsolationRepeatableRead сохраняет стабильный snapshot транзакции.
	IsolationRepeatableRead IsolationLevel = "repeatable_read"

	// IsolationSerializable требует сериализуемого результата.
	IsolationSerializable IsolationLevel = "serializable"
)

// TransactionOptions задаёт режим транзакции. Idempotent разрешает bounded
// retry всей callback только для serialization failure и deadlock.
type TransactionOptions struct {
	Isolation  IsolationLevel
	ReadOnly   bool
	Idempotent bool
}

// TransactionFunc получает единственный executor текущей транзакции. Callback
// должна передавать полученный ctx и executor во все PostgreSQL adapters.
type TransactionFunc func(ctx context.Context, executor Executor) error

type transactionMarker struct{}

// WithinTransaction выполняет callback только на RW-пуле. Вложенные вызовы с
// переданным transaction context запрещены. Panic вызывает rollback и
// пробрасывается без преобразования.
func (database *Database) WithinTransaction(
	ctx context.Context,
	options TransactionOptions,
	callback TransactionFunc,
) error {
	if database == nil || database.rw == nil {
		return errors.New("postgres: database must not be nil")
	}
	if ctx == nil {
		return errors.New("postgres: transaction context must not be nil")
	}
	if callback == nil {
		return errors.New("postgres: transaction callback must not be nil")
	}
	if ctx.Value(transactionMarker{}) != nil {
		return ErrNestedTransaction
	}

	isolation, pgxOptions, err := normalizeTransactionOptions(options)
	if err != nil {
		return err
	}

	ctx, span := database.tracer.Start(
		ctx,
		"postgres.transaction",
		trace.WithAttributes(transactionAttributes(isolation, options.ReadOnly)...),
	)
	defer span.End()

	started := time.Now()
	attempts := 1
	if options.Idempotent && database.retry.enabled {
		attempts = database.retry.maxAttempts
	}

	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		database.instruments.recordTransactionAttempt(ctx, isolation, options.ReadOnly)
		lastErr = database.runTransactionAttempt(ctx, pgxOptions, callback)
		if lastErr == nil {
			database.instruments.recordTransactionDuration(
				ctx,
				time.Since(started),
				isolation,
				options.ReadOnly,
				"committed",
			)
			return nil
		}

		retryReason, retryable := transactionRetryReason(lastErr)
		if !options.Idempotent || !database.retry.enabled || !retryable {
			span.SetStatus(codes.Error, "transaction failed")
			database.instruments.recordTransactionDuration(
				ctx,
				time.Since(started),
				isolation,
				options.ReadOnly,
				"failed",
			)
			return lastErr
		}
		if attempt == attempts {
			break
		}

		database.instruments.recordTransactionRetry(ctx, retryReason)
		span.AddEvent(
			"postgres.transaction.retry",
			trace.WithAttributes(attribute.String("postgres.retry.reason", retryReason)),
		)
		if err := database.sleep(ctx, database.retry.backoff(attempt)); err != nil {
			span.SetStatus(codes.Error, "transaction retry canceled")
			return errors.Join(lastErr, err)
		}
	}

	span.SetStatus(codes.Error, "transaction retries exhausted")
	database.instruments.recordTransactionDuration(
		ctx,
		time.Since(started),
		isolation,
		options.ReadOnly,
		"exhausted",
	)

	return errors.Join(ErrRetryExhausted, lastErr)
}

func (database *Database) runTransactionAttempt(
	ctx context.Context,
	options pgx.TxOptions,
	callback TransactionFunc,
) (resultErr error) {
	beginCtx, cancelBegin := context.WithTimeout(ctx, database.rwQueryTimeout())
	tx, err := database.rw.backend.BeginTx(beginCtx, options)
	cancelBegin()
	if err != nil {
		return wrapOperation("begin transaction on", roleRW, err)
	}

	defer func() {
		if recovered := recover(); recovered != nil {
			_ = database.rollback(ctx, tx)
			panic(recovered)
		}
	}()

	txCtx := context.WithValue(ctx, transactionMarker{}, struct{}{})
	callbackErr := callback(txCtx, newExecutor(tx, database.rwQueryTimeout()))
	if callbackErr != nil {
		rollbackErr := database.rollback(ctx, tx)
		return errors.Join(
			wrapOperation("transaction callback on", roleRW, callbackErr),
			rollbackErr,
		)
	}

	commitCtx, cancelCommit := context.WithTimeout(ctx, database.rwQueryTimeout())
	commitErr := tx.Commit(commitCtx)
	cancelCommit()
	if commitErr != nil {
		return errors.Join(
			wrapOperation("commit transaction on", roleRW, commitErr),
			database.rollback(ctx, tx),
		)
	}

	return nil
}

func (database *Database) rollback(ctx context.Context, tx pgx.Tx) error {
	rollbackCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		database.rwQueryTimeout(),
	)
	defer cancel()

	err := tx.Rollback(rollbackCtx)
	if errors.Is(err, pgx.ErrTxClosed) {
		return nil
	}

	return wrapOperation("rollback transaction on", roleRW, err)
}

func (database *Database) rwQueryTimeout() time.Duration {
	if executor, ok := database.rw.executor.(*executor); ok {
		return executor.queryTimeout
	}

	return time.Second
}

func normalizeTransactionOptions(
	options TransactionOptions,
) (IsolationLevel, pgx.TxOptions, error) {
	isolation := options.Isolation
	if isolation == "" {
		isolation = IsolationReadCommitted
	}

	pgxIsolation := pgx.ReadCommitted
	switch isolation {
	case IsolationReadCommitted:
	case IsolationRepeatableRead:
		pgxIsolation = pgx.RepeatableRead
	case IsolationSerializable:
		pgxIsolation = pgx.Serializable
	default:
		return "", pgx.TxOptions{}, errors.Join(
			ErrInvalidConfig,
			errors.New("postgres: unsupported transaction isolation level"),
		)
	}

	accessMode := pgx.ReadWrite
	if options.ReadOnly {
		accessMode = pgx.ReadOnly
	}

	return isolation, pgx.TxOptions{
		IsoLevel:   pgxIsolation,
		AccessMode: accessMode,
	}, nil
}

func transactionRetryReason(err error) (string, bool) {
	var postgresErr *pgconn.PgError
	if !errors.As(err, &postgresErr) {
		return "", false
	}

	switch postgresErr.Code {
	case serializationFailureCode:
		return "serialization_failure", true
	case deadlockDetectedCode:
		return "deadlock_detected", true
	default:
		return "", false
	}
}

func (settings retrySettings) backoff(completedAttempt int) time.Duration {
	backoff := float64(settings.initialBackoff)
	for range completedAttempt - 1 {
		backoff *= settings.backoffMultiplier
		if math.IsInf(backoff, 1) || backoff >= float64(settings.maxBackoff) {
			return settings.maxBackoff
		}
	}

	return min(time.Duration(backoff), settings.maxBackoff)
}

func transactionAttributes(
	isolation IsolationLevel,
	readOnly bool,
) []attribute.KeyValue {
	return []attribute.KeyValue{
		attribute.String("postgres.pool", string(roleRW)),
		attribute.String("postgres.transaction.isolation", string(isolation)),
		attribute.Bool("postgres.transaction.read_only", readOnly),
	}
}

func transactionMetricOptions(
	isolation IsolationLevel,
	readOnly bool,
	additional ...attribute.KeyValue,
) metric.MeasurementOption {
	attributes := append(transactionAttributes(isolation, readOnly), additional...)

	return metric.WithAttributes(attributes...)
}
