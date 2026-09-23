package postgressession

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	pg "github.com/v0hmly/marketmesh/platform/postgres"
	domain "github.com/v0hmly/marketmesh/services/auth/internal/domain/session"
)

type fakeRow func(...any) error

func (r fakeRow) Scan(values ...any) error { return r(values...) }

type fakeExecutor struct {
	row  func(string, ...any) pgx.Row
	exec func(string, ...any) (pgconn.CommandTag, error)
}

func (f *fakeExecutor) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	return f.row(sql, args...)
}
func (f *fakeExecutor) Exec(_ context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	return f.exec(sql, args...)
}
func (*fakeExecutor) Query(context.Context, string, ...any) (pgx.Rows, error) {
	panic("unexpected Query")
}

type fakeDatabase struct {
	executor  *fakeExecutor
	committed bool
	failure   error
}

func (f *fakeDatabase) RW() pg.Executor { return f.executor }
func (f *fakeDatabase) WithinTransaction(ctx context.Context, options pg.TransactionOptions, callback pg.TransactionFunc) error {
	if options.Idempotent {
		panic("mutations must not retry")
	}
	if f.failure != nil {
		return f.failure
	}
	err := callback(ctx, f.executor)
	f.committed = err == nil
	return err
}
func fixture() domain.Record {
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	record := domain.Record{Version: 1, CreatedAt: now, AccessExpiresAt: now.Add(time.Minute), RefreshExpiresAt: now.Add(time.Hour), ExpiresAt: now.Add(24 * time.Hour)}
	record.ID[0] = 1
	record.SubjectID[0] = 1
	return record
}
func recordRow(record domain.Record) pgx.Row {
	return fakeRow(func(values ...any) error {
		*values[0].(*[]byte) = record.ID.Bytes()
		*values[1].(*[]byte) = record.SubjectID.Bytes()
		*values[2].(*int64) = record.Version
		*values[3].(*time.Time) = record.CreatedAt
		*values[4].(*time.Time) = record.AccessExpiresAt
		*values[5].(*time.Time) = record.RefreshExpiresAt
		*values[6].(*time.Time) = record.ExpiresAt
		*values[7].(**time.Time) = record.RevokedAt
		return nil
	})
}
func TestReplayCommitsRevocationAndUnknownDigestDoesNotRevoke(t *testing.T) {
	for _, consumed := range []bool{false, true} {
		t.Run(map[bool]string{false: "unknown", true: "consumed"}[consumed], func(t *testing.T) {
			record := fixture()
			revocations := 0
			executor := &fakeExecutor{row: func(sql string, _ ...any) pgx.Row {
				switch {
				case strings.Contains(sql, "FOR UPDATE"):
					return recordRow(record)
				case strings.Contains(sql, "SELECT refresh_digest"):
					return fakeRow(func(values ...any) error { *values[0].(*[]byte) = make([]byte, 32); return nil })
				case strings.Contains(sql, "SELECT EXISTS"):
					return fakeRow(func(values ...any) error { *values[0].(*bool) = consumed; return nil })
				}
				panic("unexpected SQL")
			}, exec: func(sql string, _ ...any) (pgconn.CommandTag, error) {
				if !strings.Contains(sql, "session_revocation_outbox") {
					t.Fatal("revocation missing transactional outbox")
				}
				revocations++
				return pgconn.NewCommandTag("INSERT 0 1"), nil
			}}
			database := &fakeDatabase{executor: executor}
			repository, _ := New(database)
			_, err := repository.Rotate(t.Context(), record.ID, domain.Digest{1}, domain.Digest{2}, record.CreatedAt.Add(time.Second), time.Minute, time.Hour)
			expected := domain.ErrInvalidSession
			if consumed {
				expected = domain.ErrRefreshReuse
			}
			if !errors.Is(err, expected) || !database.committed {
				t.Fatalf("Rotate error=%v committed=%v", err, database.committed)
			}
			want := 0
			if consumed {
				want = 1
			}
			if revocations != want {
				t.Fatalf("revocations=%d want=%d", revocations, want)
			}
		})
	}
}
func TestRotationExpiryAndBackendFailureAreClosed(t *testing.T) {
	record := fixture()
	for _, now := range []time.Time{record.ExpiresAt, record.RefreshExpiresAt} {
		executor := &fakeExecutor{row: func(sql string, _ ...any) pgx.Row {
			if strings.Contains(sql, "FOR UPDATE") {
				return recordRow(record)
			}
			return fakeRow(func(values ...any) error { *values[0].(*[]byte) = make([]byte, 32); return nil })
		}, exec: func(string, ...any) (pgconn.CommandTag, error) {
			t.Fatal("expired session mutated")
			return pgconn.CommandTag{}, nil
		}}
		repository, _ := New(&fakeDatabase{executor: executor})
		if _, err := repository.Rotate(t.Context(), record.ID, domain.Digest{}, domain.Digest{1}, now, time.Minute, time.Hour); !errors.Is(err, domain.ErrInvalidSession) {
			t.Fatalf("boundary error=%v", err)
		}
	}
	repository, _ := New(&fakeDatabase{failure: errors.New("secret SQL credentials digest")})
	if _, err := repository.Rotate(t.Context(), record.ID, domain.Digest{}, domain.Digest{1}, record.CreatedAt, time.Minute, time.Hour); err != domain.ErrUnavailable {
		t.Fatalf("backend error leaked: %v", err)
	}
}
func TestFindSanitizesCorruptionAndErrors(t *testing.T) {
	for _, row := range []pgx.Row{fakeRow(func(...any) error { return errors.New("private connection details") }), recordRow(domain.Record{})} {
		repository, _ := New(&fakeDatabase{executor: &fakeExecutor{row: func(string, ...any) pgx.Row { return row }}})
		if _, err := repository.Find(t.Context(), domain.ID{1}); err != domain.ErrUnavailable {
			t.Fatalf("Find error=%v", err)
		}
	}
}

func TestRotationPersistsConsumedDigestAndClampsExpiry(t *testing.T) {
	record := fixture()
	previous, next := domain.Digest{1}, domain.Digest{2}
	writes := 0
	executor := &fakeExecutor{row: func(sql string, _ ...any) pgx.Row {
		switch {
		case strings.Contains(sql, "FOR UPDATE"):
			return recordRow(record)
		case strings.Contains(sql, "SELECT refresh_digest"):
			return fakeRow(func(values ...any) error { *values[0].(*[]byte) = previous[:]; return nil })
		case strings.Contains(sql, "SELECT EXISTS"):
			return fakeRow(func(values ...any) error { *values[0].(*bool) = false; return nil })
		}
		panic("unexpected SQL")
	}, exec: func(sql string, args ...any) (pgconn.CommandTag, error) {
		writes++
		if writes == 1 && !strings.Contains(sql, "INSERT INTO auth.consumed_refresh_digests") {
			t.Fatal("rotation did not preserve consumed digest first")
		}
		if writes == 2 {
			if !strings.Contains(sql, "UPDATE auth.sessions") || args[1].(int64) != 2 {
				t.Fatal("rotation did not advance canonical version")
			}
		}
		return pgconn.NewCommandTag("UPDATE 1"), nil
	}}
	database := &fakeDatabase{executor: executor}
	repository, _ := New(database)
	result, err := repository.Rotate(t.Context(), record.ID, previous, next, record.CreatedAt.Add(time.Second), 48*time.Hour, 48*time.Hour)
	if err != nil || !database.committed || writes != 2 || result.Version != 2 || !result.AccessExpiresAt.Equal(record.ExpiresAt) || !result.RefreshExpiresAt.Equal(record.ExpiresAt) {
		t.Fatalf("rotation result=%+v writes=%d error=%v", result, writes, err)
	}
}

func TestRevocationReasonIsBoundedAndOutboxFailureIsUnavailable(t *testing.T) {
	repository, _ := New(&fakeDatabase{failure: errors.New("database statement with private value")})
	if err := repository.Revoke(t.Context(), domain.ID{1}, time.Now(), "user supplied sensitive reason"); err != domain.ErrInvalidSession {
		t.Fatalf("unbounded reason=%v", err)
	}
	if err := repository.Revoke(t.Context(), domain.ID{1}, time.Now(), "logout"); err != domain.ErrUnavailable {
		t.Fatalf("backend error=%v", err)
	}
}
