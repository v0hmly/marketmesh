package avatars

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/v0hmly/marketmesh/services/user/internal/application/identity"
	"github.com/v0hmly/marketmesh/services/user/internal/domain/avatar"
	"github.com/v0hmly/marketmesh/services/user/internal/domain/profile"
)

type fakeRepo struct {
	calls   int
	owner   profile.SubjectID
	id      avatar.FileID
	version uint64
}

func (r *fakeRepo) Get(_ context.Context, owner profile.SubjectID) (avatar.Avatar, error) {
	r.calls++
	r.owner = owner
	return avatar.Avatar{SubjectID: owner, Version: 1}, nil
}
func (r *fakeRepo) Set(_ context.Context, owner profile.SubjectID, id avatar.FileID, version uint64) (avatar.Avatar, error) {
	r.calls++
	r.owner = owner
	r.id = id
	r.version = version
	return avatar.Avatar{SubjectID: owner, FileID: id, Version: version + 1}, nil
}

type fakeFiles struct {
	calls   int
	owner   profile.SubjectID
	id      avatar.FileID
	session string
	err     error
}

func (f *fakeFiles) Inspect(_ context.Context, owner profile.SubjectID, id avatar.FileID, session string) error {
	f.calls++
	f.owner = owner
	f.id = id
	f.session = session
	return f.err
}
func (f *fakeFiles) Retire(_ context.Context, owner profile.SubjectID, id avatar.FileID) error {
	f.calls++
	f.owner = owner
	f.id = id
	return f.err
}
func TestAuthorizationAndFilesValidationBeforeCAS(t *testing.T) {
	ctx := context.Background()
	r := new(fakeRepo)
	f := new(fakeFiles)
	s, _ := New(r, f)
	p := identity.Principal{SubjectID: profile.SubjectID{1}, SessionID: "audit-session", CanRead: true, CanWrite: true}
	id := avatar.FileID{9}
	for _, bad := range []identity.Principal{{}, {SubjectID: p.SubjectID, CanRead: true}} {
		if _, err := s.Set(ctx, bad, id, 1); err == nil {
			t.Fatal("unauthorized mutation")
		}
	}
	if f.calls != 0 || r.calls != 0 {
		t.Fatal("dependency called before authorization")
	}
	f.err = avatar.ErrUnavailable
	if _, err := s.Set(ctx, p, id, 1); !errors.Is(err, avatar.ErrUnavailable) || r.calls != 0 {
		t.Fatal("unready or foreign file persisted", err)
	}
	f.err = nil
	a, err := s.Set(ctx, p, id, 7)
	if err != nil || a.Version != 8 || f.owner != p.SubjectID || f.id != id || f.session != p.SessionID || r.owner != p.SubjectID || r.id != id || r.version != 7 {
		t.Fatal("principal/CAS mapping", a, err)
	}
	calls := f.calls
	f.err = errors.New("offline")
	a, err = s.Clear(ctx, p, 8)
	if err != nil || a.FileID != (avatar.FileID{}) || a.Version != 9 || f.calls != calls {
		t.Fatal("clear requires Files online", a, err)
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err = s.Get(cancelled, p); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

type fakeQueue struct {
	record                        Retirement
	found                         bool
	claimErr, finishErr, retryErr error
	finishCalls, retryCalls       int
	delay                         time.Duration
}

func (q *fakeQueue) Claim(context.Context, time.Duration) (Retirement, bool, error) {
	return q.record, q.found, q.claimErr
}
func (q *fakeQueue) Finish(context.Context, Retirement) (bool, error) {
	q.finishCalls++
	return true, q.finishErr
}
func (q *fakeQueue) Retry(_ context.Context, _ Retirement, d time.Duration) (bool, error) {
	q.retryCalls++
	q.delay = d
	return true, q.retryErr
}
func TestCleanupRetiresOnlyClaimedOwnerAndKeepsAmbiguousFailure(t *testing.T) {
	q := &fakeQueue{record: Retirement{SubjectID: profile.SubjectID{1}, FileID: avatar.FileID{2}, Attempts: 999}, found: true}
	f := &fakeFiles{err: errors.New("private upstream information")}
	w, _ := NewWorker(q, f)
	err := w.Step(context.Background())
	var diagnostic CleanupError
	if !errors.As(err, &diagnostic) || diagnostic.Class != "files_unavailable" || diagnostic.FileID != q.record.FileID || q.finishCalls != 0 || q.retryCalls != 1 || q.delay != 256*time.Second {
		t.Fatal(err, q)
	}
	f.err = nil
	if err = w.Step(context.Background()); err != nil || q.finishCalls != 1 || f.owner != q.record.SubjectID || f.id != q.record.FileID {
		t.Fatal(err)
	}
	q.found = false
	calls := f.calls
	if err = w.Step(context.Background()); err != nil || f.calls != calls {
		t.Fatal("empty queue called Files", err)
	}
}
