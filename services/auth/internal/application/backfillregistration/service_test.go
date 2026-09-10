package backfillregistration

import (
	"context"
	"errors"
	"github.com/v0hmly/marketmesh/services/auth/internal/domain/credential"
	"github.com/v0hmly/marketmesh/services/auth/internal/domain/registrationevent"
	"testing"
	"time"
)

type fake struct {
	rows   []Candidate
	writes int
	fail   bool
}

func (f *fake) Scan(context.Context, []byte, int) ([]Candidate, error) { return f.rows, nil }
func (f *fake) Ensure(context.Context, registrationevent.Event) (bool, error) {
	f.writes++
	if f.fail {
		return false, errors.New("failed")
	}
	return true, nil
}
func (f *fake) Replay(context.Context, credential.SubjectID, time.Time) (bool, error) {
	f.writes++
	return true, nil
}
func (f *fake) New(_ context.Context, s credential.SubjectID) (registrationevent.Event, error) {
	return registrationevent.Event{ID: [16]byte{1}, SubjectID: s, OccurredAt: time.Now()}, nil
}
func TestDryRunApplyReplayAndResume(t *testing.T) {
	now := time.Now()
	for _, tt := range []struct {
		apply, replay bool
		writes        int
	}{{false, false, 0}, {false, true, 0}, {true, false, 1}, {true, true, 2}} {
		f := &fake{rows: []Candidate{{Subject: credential.SubjectID{1}, Missing: true}, {Subject: credential.SubjectID{2}, PublishedAt: &now}, {Subject: credential.SubjectID{3}}}}
		s, _ := New(f, f)
		r, err := s.Page(context.Background(), Options{Limit: 10, Apply: tt.apply, ReplayPublished: tt.replay})
		if err != nil || f.writes != tt.writes || r.Scanned != 3 || r.Missing != 1 || r.Published != 1 || !r.Done {
			t.Fatalf("result %#v writes %d error %v", r, f.writes, err)
		}
		raw, err := (Options{Limit: 1, After: r.NextCursor}).Validate()
		if err != nil || raw[0] != 3 {
			t.Fatal("cursor")
		}
	}
}
func TestCanceledInvalidAndFailure(t *testing.T) {
	f := &fake{rows: []Candidate{{Subject: credential.SubjectID{1}, Missing: true}}, fail: true}
	s, _ := New(f, f)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Page(ctx, Options{Limit: 1, Apply: true}); !errors.Is(err, context.Canceled) || f.writes != 0 {
		t.Fatal("cancellation")
	}
	for _, o := range []Options{{Limit: 0}, {Limit: 1001}, {Limit: 1, After: "secret"}} {
		if _, err := s.Page(context.Background(), o); err == nil {
			t.Fatal("invalid accepted")
		}
	}
	r, err := s.Page(context.Background(), Options{Limit: 1, Apply: true})
	if err == nil || r.NextCursor != "" || r.Scanned != 0 {
		t.Fatal("failed row cursor advanced")
	}
}
