package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/v0hmly/marketmesh/services/user/internal/domain/profile"
)

type fakeExecutor struct {
	calls int
	row   pgx.Row
}

func (f *fakeExecutor) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	panic("unexpected Exec")
}
func (f *fakeExecutor) Query(context.Context, string, ...any) (pgx.Rows, error) {
	panic("unexpected Query")
}
func (f *fakeExecutor) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	f.calls++
	if strings.Contains(sql, "INSERT") {
		panic("unexpected provisioning")
	}
	return f.row
}

type errorRow struct{ err error }

func (r errorRow) Scan(...any) error { return r.err }
func TestErrorsDoNotDiscloseStorageDetails(t *testing.T) {
	cause := errors.New("SQL private bio and password")
	f := &fakeExecutor{row: errorRow{cause}}
	r, _ := New(f)
	_, err := r.Get(context.Background(), profile.SubjectID{1})
	if !errors.Is(err, cause) || strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "SQL") {
		t.Fatalf("unsafe error: %v", err)
	}
	if _, err := New(nil); err == nil {
		t.Fatal("nil executor accepted")
	}
}
func TestMissingUpdateDoesNotCreate(t *testing.T) {
	f := &fakeExecutor{row: errorRow{pgx.ErrNoRows}}
	r, _ := New(f)
	if _, err := r.Update(context.Background(), profile.SubjectID{1}, profile.Fields{}, 1); !errors.Is(err, profile.ErrNotReady) {
		t.Fatal(err)
	}
	if f.calls != 2 {
		t.Fatalf("calls=%d", f.calls)
	}
}
func TestInvalidStorageArgumentsDoNotQuery(t *testing.T) {
	f := &fakeExecutor{}
	r, _ := New(f)
	if _, err := r.Get(context.Background(), profile.SubjectID{}); !errors.Is(err, profile.ErrInvalidProfile) {
		t.Fatal(err)
	}
	if _, err := r.Update(context.Background(), profile.SubjectID{1}, profile.Fields{DisplayName: " untrimmed "}, 1); !errors.Is(err, profile.ErrInvalidProfile) {
		t.Fatal(err)
	}
	if f.calls != 0 {
		t.Fatal("invalid input queried")
	}
}

func TestContextStopsBeforeExecutor(t *testing.T) {
	f := &fakeExecutor{}
	r, _ := New(f)
	id := profile.SubjectID{1}
	if _, err := r.Get(nil, id); err == nil {
		t.Fatal("nil context accepted")
	}
	if _, err := r.Update(nil, id, profile.Fields{}, 1); err == nil {
		t.Fatal("nil context accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := r.Get(ctx, id); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := r.Update(ctx, id, profile.Fields{}, 1); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if f.calls != 0 {
		t.Fatal("cancelled operation queried")
	}
}

// Corrupt persisted data is a backend fault, never a caller validation error.
type corruptRow struct {
	subject []byte
	name    string
	version int64
}

func (r corruptRow) Scan(dest ...any) error {
	*dest[0].(*[]byte) = r.subject
	*dest[1].(*string) = r.name
	*dest[3].(*int64) = r.version
	return nil
}
func TestCorruptStoredProfileIsBackendError(t *testing.T) {
	for _, row := range []corruptRow{{subject: []byte{1}, version: 1}, {subject: profile.SubjectID{1}.Bytes(), name: "bad\x00", version: 1}, {subject: profile.SubjectID{1}.Bytes(), version: 0}} {
		_, err := decode(row)
		if err == nil || errors.Is(err, profile.ErrInvalidProfile) || errors.Is(err, profile.ErrNotReady) || errors.Is(err, profile.ErrConflict) {
			t.Fatalf("corrupt row classified as caller error: %v", err)
		}
		if strings.Contains(err.Error(), row.name) && row.name != "" {
			t.Fatal("stored value disclosed")
		}
	}
}
