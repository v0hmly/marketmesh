package postgres

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Executor — минимальная pgx-совместимая граница для PostgreSQL adapters.
// Application и domain объявляют собственные предметные порты и не должны
// импортировать этот интерфейс либо pgx.
type Executor interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, arguments ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, arguments ...any) pgx.Row
}

type queryBackend interface {
	Exec(ctx context.Context, sql string, arguments ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, arguments ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, arguments ...any) pgx.Row
}

type executor struct {
	backend      queryBackend
	queryTimeout time.Duration
}

func newExecutor(backend queryBackend, queryTimeout time.Duration) *executor {
	return &executor{
		backend:      backend,
		queryTimeout: queryTimeout,
	}
}

func (executor *executor) Exec(
	ctx context.Context,
	sql string,
	arguments ...any,
) (pgconn.CommandTag, error) {
	if ctx == nil {
		return pgconn.CommandTag{}, errors.New("postgres: query context must not be nil")
	}

	queryCtx, cancel := context.WithTimeout(ctx, executor.queryTimeout)
	defer cancel()

	return executor.backend.Exec(queryCtx, sql, arguments...)
}

func (executor *executor) Query(
	ctx context.Context,
	sql string,
	arguments ...any,
) (pgx.Rows, error) {
	if ctx == nil {
		return nil, errors.New("postgres: query context must not be nil")
	}

	queryCtx, cancel := context.WithTimeout(ctx, executor.queryTimeout)
	rows, err := executor.backend.Query(queryCtx, sql, arguments...)
	if err != nil {
		cancel()
		return nil, err
	}

	return &cancelRows{
		Rows:   rows,
		cancel: cancel,
	}, nil
}

func (executor *executor) QueryRow(
	ctx context.Context,
	sql string,
	arguments ...any,
) pgx.Row {
	if ctx == nil {
		return errorRow{err: errors.New("postgres: query context must not be nil")}
	}

	queryCtx, cancel := context.WithTimeout(ctx, executor.queryTimeout)

	return &cancelRow{
		Row:    executor.backend.QueryRow(queryCtx, sql, arguments...),
		cancel: cancel,
	}
}

type cancelRows struct {
	pgx.Rows
	cancel context.CancelFunc
	once   sync.Once
}

func (rows *cancelRows) Close() {
	rows.Rows.Close()
	rows.stopTimeout()
}

func (rows *cancelRows) Next() bool {
	next := rows.Rows.Next()
	if !next {
		rows.stopTimeout()
	}

	return next
}

func (rows *cancelRows) Scan(destinations ...any) error {
	err := rows.Rows.Scan(destinations...)
	if err != nil {
		rows.stopTimeout()
	}

	return err
}

func (rows *cancelRows) Values() ([]any, error) {
	values, err := rows.Rows.Values()
	if err != nil {
		rows.stopTimeout()
	}

	return values, err
}

func (rows *cancelRows) stopTimeout() {
	rows.once.Do(rows.cancel)
}

type cancelRow struct {
	pgx.Row
	cancel context.CancelFunc
	once   sync.Once
}

func (row *cancelRow) Scan(destinations ...any) error {
	defer row.once.Do(row.cancel)

	return row.Row.Scan(destinations...)
}

type errorRow struct {
	err error
}

func (row errorRow) Scan(...any) error {
	return row.err
}

var _ Executor = (*executor)(nil)
