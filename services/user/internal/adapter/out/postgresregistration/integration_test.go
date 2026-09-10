//go:build integration

package postgresregistration

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/v0hmly/marketmesh/services/user/internal/application/consumeregistration"
	"github.com/v0hmly/marketmesh/services/user/internal/domain/profile"
	"github.com/v0hmly/marketmesh/services/user/internal/domain/registrationevent"
	"github.com/v0hmly/marketmesh/services/user/migrations"
	"os"
	"sync"
	"testing"
	"time"
)

func TestIntegrationAtomicInboxAndProfile(t *testing.T) {
	dsn := os.Getenv("MARKETMESH_USER_POSTGRES_DSN")
	if dsn == "" {
		t.Fatal("MARKETMESH_USER_POSTGRES_DSN required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(migrations.ProfilesUp)
	exec(migrations.RegistrationInboxUp)
	defer func() {
		_, err := pool.Exec(context.Background(), migrations.RegistrationInboxDown+migrations.ProfilesDown)
		if err != nil {
			t.Error(err)
		}
	}()
	store, _ := New(pool)
	event := registrationevent.Event{ID: [16]byte{1}, SubjectID: profile.SubjectID{2}, OccurredAt: time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)}
	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			if _, err := store.Apply(ctx, event); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	exec(`UPDATE users.profiles SET display_name='Alice',bio='kept',version=7 WHERE subject_id=$1`, event.SubjectID.Bytes())
	if out, err := store.Apply(ctx, event); err != nil || out != consumeregistration.Duplicate {
		t.Fatal(out, err)
	}
	next := event
	next.ID[0] = 3
	if _, err := store.Apply(ctx, next); err != nil {
		t.Fatal(err)
	}
	conflict := event
	conflict.SubjectID[0] = 4
	if _, err := store.Apply(ctx, conflict); !errors.Is(err, consumeregistration.ErrConflict) {
		t.Fatal(err)
	}
	conflict = event
	conflict.TraceID[0] = 4
	if _, err := store.Apply(ctx, conflict); !errors.Is(err, consumeregistration.ErrConflict) {
		t.Fatal(err)
	}
	var display, bio string
	var version, count int
	if err := pool.QueryRow(ctx, `SELECT display_name,bio,version FROM users.profiles WHERE subject_id=$1`, event.SubjectID.Bytes()).Scan(&display, &bio, &version); err != nil {
		t.Fatal(err)
	}
	if display != "Alice" || bio != "kept" || version != 7 {
		t.Fatal("profile overwritten")
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM users.registration_inbox`).Scan(&count); err != nil || count != 2 {
		t.Fatal(count, err)
	}
	exec(`ALTER TABLE users.profiles ADD CONSTRAINT reject_new CHECK (subject_id <> decode('05000000000000000000000000000000','hex'))`)
	failed := event
	failed.ID[0] = 5
	failed.SubjectID[0] = 5
	if _, err := store.Apply(ctx, failed); err == nil {
		t.Fatal("constraint bypassed")
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM users.registration_inbox WHERE event_id=$1`, failed.ID[:]).Scan(&count); err != nil || count != 0 {
		t.Fatal("partial inbox commit", count, err)
	}
}
