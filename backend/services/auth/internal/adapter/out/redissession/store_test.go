package redissession

import (
	"context"
	"errors"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"
	platformredis "github.com/v0hmly/marketmesh/platform/redis"
	domain "github.com/v0hmly/marketmesh/services/auth/internal/domain/session"
)

type fakeClient struct {
	commands goredis.Cmdable
	failure  error
	late     bool
	finished chan struct{}
}

func (f *fakeClient) Execute(ctx context.Context, op platformredis.Operation) error {
	if f.late {
		go func() { _ = op(ctx, f.commands); close(f.finished) }()
		return context.DeadlineExceeded
	}
	if f.failure != nil {
		return f.failure
	}
	return op(ctx, f.commands)
}

type fakeCommands struct {
	goredis.Cmdable
	fields     map[string]string
	evalResult int64
}

func (f *fakeCommands) HGetAll(ctx context.Context, _ string) *goredis.MapStringStringCmd {
	return goredis.NewMapStringStringResult(f.fields, nil)
}
func (f *fakeCommands) Eval(ctx context.Context, _ string, _ []string, _ ...any) *goredis.Cmd {
	return goredis.NewCmdResult(f.evalResult, nil)
}
func TestGetRejectsMissingMalformedAndBackendFailures(t *testing.T) {
	for _, fields := range []map[string]string{nil, {"version": "1"}, {"version": "0", "digest": "bad", "expires_at": "bad"}} {
		store, _ := New(&fakeClient{commands: &fakeCommands{fields: fields}})
		_, err := store.Get(t.Context(), domain.ID{1})
		expected := domain.ErrUnavailable
		if len(fields) == 0 {
			expected = domain.ErrInvalidSession
		}
		if err != expected {
			t.Fatalf("Get error=%v want=%v", err, expected)
		}
	}
	store, _ := New(&fakeClient{failure: errors.New("redis://secret-password")})
	if _, err := store.Get(t.Context(), domain.ID{1}); err != domain.ErrUnavailable {
		t.Fatalf("Get leaked error: %v", err)
	}
}
func TestPutRejectsExpiredAndStaleVersions(t *testing.T) {
	now := time.Now()
	record := domain.Record{ID: domain.ID{1}, Version: 1, CreatedAt: now.Add(-time.Minute), AccessExpiresAt: now, ExpiresAt: now.Add(time.Hour)}
	record.SubjectID[0] = 1
	store, _ := New(&fakeClient{commands: &fakeCommands{evalResult: 0}})
	if err := store.Put(t.Context(), record, domain.Digest{1}, now); err != domain.ErrInvalidSession {
		t.Fatalf("expired Put=%v", err)
	}
	record.AccessExpiresAt = now.Add(time.Minute)
	if err := store.Put(t.Context(), record, domain.Digest{1}, now); err != domain.ErrInvalidSession {
		t.Fatalf("stale Put=%v", err)
	}
}
func TestLateCallbackDoesNotRaceOrBlockAfterTimeout(t *testing.T) {
	client := &fakeClient{commands: &fakeCommands{fields: map[string]string{"version": "1"}}, late: true, finished: make(chan struct{})}
	store, _ := New(client)
	if _, err := store.Get(t.Context(), domain.ID{1}); err != domain.ErrUnavailable {
		t.Fatalf("Get timeout=%v", err)
	}
	select {
	case <-client.finished:
	case <-time.After(time.Second):
		t.Fatal("late callback blocked")
	}
}
