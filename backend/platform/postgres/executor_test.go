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

func TestExecutorBoundsExec(t *testing.T) {
	t.Parallel()

	backend := &fakeQueryBackend{
		exec: func(ctx context.Context, _ string, _ ...any) (pgconn.CommandTag, error) {
			<-ctx.Done()
			return pgconn.CommandTag{}, ctx.Err()
		},
	}
	executor := newExecutor(backend, 20*time.Millisecond)

	started := time.Now()
	_, err := executor.Exec(t.Context(), "SELECT sensitive", "private-argument")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Exec() error = %v, want DeadlineExceeded", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("Exec() took %v, want bounded call", elapsed)
	}
}

func TestExecutorKeepsQueryContextUntilRowsClose(t *testing.T) {
	t.Parallel()

	rows := &fakeRows{}
	var queryCtx context.Context
	backend := &fakeQueryBackend{
		query: func(ctx context.Context, _ string, _ ...any) (pgx.Rows, error) {
			queryCtx = ctx
			return rows, nil
		},
	}
	executor := newExecutor(backend, time.Second)

	wrapped, err := executor.Query(t.Context(), "SELECT 1")
	if err != nil {
		t.Fatalf("Query() error = %v", err)
	}
	if queryCtx.Err() != nil {
		t.Fatalf("query context canceled before rows close: %v", queryCtx.Err())
	}

	wrapped.Close()
	if queryCtx.Err() == nil {
		t.Fatal("query context remains active after rows close")
	}
	if rows.closeCalls.Load() != 1 {
		t.Fatalf("underlying Close() calls = %d, want 1", rows.closeCalls.Load())
	}
}

func TestExecutorCancelsQueryRowContextAfterScan(t *testing.T) {
	t.Parallel()

	row := &fakeRow{}
	var queryCtx context.Context
	backend := &fakeQueryBackend{
		queryRow: func(ctx context.Context, _ string, _ ...any) pgx.Row {
			queryCtx = ctx
			return row
		},
	}
	executor := newExecutor(backend, time.Second)

	if err := executor.QueryRow(t.Context(), "SELECT 1").Scan(); err != nil {
		t.Fatalf("QueryRow().Scan() error = %v", err)
	}
	if queryCtx.Err() == nil {
		t.Fatal("query row context remains active after Scan")
	}
}

func TestExecutorRejectsNilContext(t *testing.T) {
	t.Parallel()

	executor := newExecutor(&fakeQueryBackend{}, time.Second)
	if _, err := executor.Exec(nil, "SELECT 1"); err == nil {
		t.Fatal("Exec(nil) error = nil")
	}
	if _, err := executor.Query(nil, "SELECT 1"); err == nil {
		t.Fatal("Query(nil) error = nil")
	}
	if err := executor.QueryRow(nil, "SELECT 1").Scan(); err == nil {
		t.Fatal("QueryRow(nil).Scan() error = nil")
	}
}

type fakeQueryBackend struct {
	exec     func(context.Context, string, ...any) (pgconn.CommandTag, error)
	query    func(context.Context, string, ...any) (pgx.Rows, error)
	queryRow func(context.Context, string, ...any) pgx.Row
}

func (backend *fakeQueryBackend) Exec(
	ctx context.Context,
	sql string,
	arguments ...any,
) (pgconn.CommandTag, error) {
	if backend.exec == nil {
		return pgconn.CommandTag{}, nil
	}

	return backend.exec(ctx, sql, arguments...)
}

func (backend *fakeQueryBackend) Query(
	ctx context.Context,
	sql string,
	arguments ...any,
) (pgx.Rows, error) {
	if backend.query == nil {
		return &fakeRows{}, nil
	}

	return backend.query(ctx, sql, arguments...)
}

func (backend *fakeQueryBackend) QueryRow(
	ctx context.Context,
	sql string,
	arguments ...any,
) pgx.Row {
	if backend.queryRow == nil {
		return &fakeRow{}
	}

	return backend.queryRow(ctx, sql, arguments...)
}

type fakeRow struct {
	err error
}

func (row *fakeRow) Scan(...any) error {
	return row.err
}

type fakeRows struct {
	closeCalls atomic.Int32
	err        error
}

func (rows *fakeRows) Close() {
	rows.closeCalls.Add(1)
}

func (rows *fakeRows) Err() error {
	return rows.err
}

func (*fakeRows) CommandTag() pgconn.CommandTag {
	return pgconn.CommandTag{}
}

func (*fakeRows) FieldDescriptions() []pgconn.FieldDescription {
	return []pgconn.FieldDescription{}
}

func (*fakeRows) Next() bool {
	return false
}

func (rows *fakeRows) Scan(...any) error {
	return rows.err
}

func (rows *fakeRows) Values() ([]any, error) {
	return []any{}, rows.err
}

func (*fakeRows) RawValues() [][]byte {
	return [][]byte{}
}

func (*fakeRows) Conn() *pgx.Conn {
	return nil
}

var (
	_ pgx.Row  = (*fakeRow)(nil)
	_ pgx.Rows = (*fakeRows)(nil)
)
