package postgresoutbox

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/v0hmly/marketmesh/services/auth/internal/application/publishregistration"
)

type fakeExecutor struct {
	calls int
	err   error
}

func (f *fakeExecutor) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	f.calls++
	return pgconn.NewCommandTag("UPDATE 0"), f.err
}
func (f *fakeExecutor) Query(context.Context, string, ...any) (pgx.Rows, error) {
	panic("unexpected rows query")
}
func (f *fakeExecutor) QueryRow(context.Context, string, ...any) pgx.Row {
	f.calls++
	return errorRow{f.err}
}

type errorRow struct{ err error }

func (r errorRow) Scan(...any) error { return r.err }
func TestNoIOForInvalidOrCancelledOperations(t *testing.T) {
	f := &fakeExecutor{}
	s, _ := New(f)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := s.Claim(ctx, time.Second); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := s.MarkPublished(ctx, publishregistration.Record{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := s.Retry(ctx, publishregistration.Record{}, time.Second); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, _, err := s.Pending(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, _, err := s.Claim(nil, time.Second); err == nil {
		t.Fatal("nil context accepted")
	}
	if _, _, err := s.Claim(context.Background(), 0); err == nil {
		t.Fatal("zero lease accepted")
	}
	if _, err := s.MarkPublished(context.Background(), publishregistration.Record{}); err == nil {
		t.Fatal("zero record accepted")
	}
	if f.calls != 0 {
		t.Fatal("invalid operation queried")
	}
	if _, err := New(nil); err == nil {
		t.Fatal("nil executor accepted")
	}
}
func TestSafeStorageErrorsAndEmptyQueue(t *testing.T) {
	cause := errors.New("private DSN SQL payload")
	f := &fakeExecutor{err: cause}
	s, _ := New(f)
	if _, _, err := s.Claim(context.Background(), time.Second); !errors.Is(err, cause) || strings.Contains(err.Error(), "private") {
		t.Fatal("unsafe error", err)
	}
	f.err = pgx.ErrNoRows
	if _, found, err := s.Claim(context.Background(), time.Second); err != nil || found {
		t.Fatal("empty queue", err)
	}
	f.err = nil
	r := publishregistration.Record{ID: [16]byte{1}, LeaseToken: [16]byte{2}}
	if marked, err := s.MarkPublished(context.Background(), r); err != nil || marked {
		t.Fatal("lost lease marked", err)
	}
	if retried, err := s.Retry(context.Background(), r, time.Second); err != nil || retried {
		t.Fatal("lost lease retried", err)
	}
}
