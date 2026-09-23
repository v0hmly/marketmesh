package postgres

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"go.opentelemetry.io/otel/trace/noop"
)

type fakePool struct {
	fakeQueryBackend

	mutex        sync.Mutex
	transactions []*fakeTx
	beginOptions []pgx.TxOptions
	beginErr     error
	pingErr      error
	poolStats    poolStats

	closeCalls   atomic.Int32
	closeStarted chan struct{}
	closeRelease <-chan struct{}
}

func (pool *fakePool) BeginTx(
	_ context.Context,
	options pgx.TxOptions,
) (pgx.Tx, error) {
	pool.mutex.Lock()
	defer pool.mutex.Unlock()

	pool.beginOptions = append(pool.beginOptions, options)
	if pool.beginErr != nil {
		return nil, pool.beginErr
	}
	if len(pool.transactions) == 0 {
		transaction := &fakeTx{}
		pool.transactions = append(pool.transactions, transaction)
	}

	transaction := pool.transactions[0]
	pool.transactions = pool.transactions[1:]

	return transaction, nil
}

func (pool *fakePool) Ping(context.Context) error {
	return pool.pingErr
}

func (pool *fakePool) Close() {
	pool.closeCalls.Add(1)
	if pool.closeStarted != nil {
		select {
		case <-pool.closeStarted:
		default:
			close(pool.closeStarted)
		}
	}
	if pool.closeRelease != nil {
		<-pool.closeRelease
	}
}

func (pool *fakePool) Stats() poolStats {
	return pool.poolStats
}

func (pool *fakePool) options() []pgx.TxOptions {
	pool.mutex.Lock()
	defer pool.mutex.Unlock()

	return append([]pgx.TxOptions{}, pool.beginOptions...)
}

type fakeTx struct {
	fakeQueryBackend

	commitErr      error
	rollbackErr    error
	commitCalls    atomic.Int32
	rollbackCalls  atomic.Int32
	rollbackCtxErr error
}

func (*fakeTx) Begin(context.Context) (pgx.Tx, error) {
	return nil, ErrNestedTransaction
}

func (transaction *fakeTx) Commit(context.Context) error {
	transaction.commitCalls.Add(1)

	return transaction.commitErr
}

func (transaction *fakeTx) Rollback(ctx context.Context) error {
	transaction.rollbackCalls.Add(1)
	transaction.rollbackCtxErr = ctx.Err()

	return transaction.rollbackErr
}

func (*fakeTx) CopyFrom(
	context.Context,
	pgx.Identifier,
	[]string,
	pgx.CopyFromSource,
) (int64, error) {
	return 0, nil
}

func (*fakeTx) SendBatch(context.Context, *pgx.Batch) pgx.BatchResults {
	return nil
}

func (*fakeTx) LargeObjects() pgx.LargeObjects {
	return pgx.LargeObjects{}
}

func (*fakeTx) Prepare(
	context.Context,
	string,
	string,
) (*pgconn.StatementDescription, error) {
	return nil, nil
}

func (*fakeTx) Conn() *pgx.Conn {
	return nil
}

func newTestDatabase(rw *fakePool, ro *fakePool, queryTimeout time.Duration) *Database {
	if rw == nil {
		rw = &fakePool{}
	}
	if ro == nil {
		ro = &fakePool{}
	}

	rwExecutor := newExecutor(rw, queryTimeout)
	roExecutor := newExecutor(ro, queryTimeout)

	return &Database{
		rw: &managedPool{
			role:     roleRW,
			backend:  rw,
			executor: rwExecutor,
		},
		ro: &managedPool{
			role:     roleRO,
			backend:  ro,
			executor: roExecutor,
		},
		tracer: noop.NewTracerProvider().Tracer(instrumentationName),
		sleep:  sleepWithContext,
	}
}

var (
	_ poolBackend = (*fakePool)(nil)
	_ pgx.Tx      = (*fakeTx)(nil)
)
