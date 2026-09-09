//go:build integration

package postgresoutbox_test

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	adapter "github.com/v0hmly/marketmesh/services/auth/internal/adapter/out/postgresoutbox"
	"github.com/v0hmly/marketmesh/services/auth/internal/application/publishregistration"
	"github.com/v0hmly/marketmesh/services/auth/migrations"
)

func TestIntegrationRegistrationOutboxLeases(t *testing.T) {
	dsn := os.Getenv("MARKETMESH_AUTH_POSTGRES_DSN")
	if dsn == "" {
		t.Fatal("MARKETMESH_AUTH_POSTGRES_DSN required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(migrations.CredentialsUp)
	exec(migrations.RegistrationOutboxUp)
	defer func() {
		if _, err := db.Exec(context.Background(), migrations.RegistrationOutboxDown+migrations.CredentialsDown); err != nil {
			t.Error(err)
		}
	}()
	store, _ := adapter.New(db)
	id := [16]byte{1}
	exec(`INSERT INTO auth.registration_outbox(event_id,subject_id,occurred_at,payload)VALUES($1,$1,clock_timestamp()-interval '1 minute',$2)`, id[:], []byte{1})
	count, oldest, err := store.Pending(ctx)
	if err != nil || count != 1 || time.Since(oldest) < 50*time.Second {
		t.Fatal("backlog", count, err)
	}
	const workers = 16
	start := make(chan struct{})
	claims := make(chan publishregistration.Record, workers)
	errs := make(chan error, workers)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			r, found, err := store.Claim(ctx, time.Second)
			if err != nil {
				errs <- err
			}
			if found {
				claims <- r
			}
		}()
	}
	close(start)
	group.Wait()
	close(claims)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	var claimed publishregistration.Record
	wins := 0
	for r := range claims {
		claimed = r
		wins++
	}
	if wins != 1 || claimed.ID != id || claimed.Attempts != 1 {
		t.Fatal("exclusive lease failed", wins)
	}
	// Active leases count toward durable backlog and cannot be stolen.
	if _, found, err := store.Claim(ctx, time.Second); err != nil || found {
		t.Fatal("active lease reclaimed", err)
	}
	wrong := claimed
	wrong.LeaseToken[0] ^= 1
	if marked, err := store.MarkPublished(ctx, wrong); err != nil || marked {
		t.Fatal("foreign lease marked", err)
	}
	// Simulate process death and expired lease deterministically using the DB clock.
	exec(`UPDATE auth.registration_outbox SET lease_until=clock_timestamp()-interval '1 second' WHERE event_id=$1`, id[:])
	if marked, err := store.MarkPublished(ctx, claimed); err != nil || marked {
		t.Fatal("expired owner marked", err)
	}
	next, found, err := store.Claim(ctx, time.Second)
	if err != nil || !found || next.LeaseToken == claimed.LeaseToken || next.Attempts != 2 {
		t.Fatal("takeover failed", err)
	}
	if retried, err := store.Retry(ctx, claimed, time.Second); err != nil || retried {
		t.Fatal("old owner rescheduled new lease", err)
	}
	if retried, err := store.Retry(ctx, next, time.Second); err != nil || !retried {
		t.Fatal("retry failed", err)
	}
	if _, found, err := store.Claim(ctx, time.Second); err != nil || found {
		t.Fatal("retry delay ignored", err)
	}
	exec(`UPDATE auth.registration_outbox SET next_attempt_at=clock_timestamp()-interval '1 second' WHERE event_id=$1`, id[:])
	final, found, err := store.Claim(ctx, time.Second)
	if err != nil || !found || final.Attempts != 3 {
		t.Fatal("retry claim", err)
	}
	if marked, err := store.MarkPublished(ctx, final); err != nil || !marked {
		t.Fatal("mark publish", err)
	}
	if marked, err := store.MarkPublished(ctx, final); err != nil || marked {
		t.Fatal("duplicate completion mutated state", err)
	}
	count, oldest, err = store.Pending(ctx)
	if err != nil || count != 0 || !oldest.IsZero() {
		t.Fatal("published event remains pending", err)
	}
	if _, found, err := store.Claim(ctx, time.Second); err != nil || found {
		t.Fatal("published row reclaimed", err)
	}
	// A locked earliest row must not block another due row.
	firstID, secondID := [16]byte{2}, [16]byte{3}
	for _, eventID := range [][16]byte{firstID, secondID} {
		exec(`INSERT INTO auth.registration_outbox(event_id,subject_id,occurred_at,payload)VALUES($1,$1,clock_timestamp(),$2)`, eventID[:], []byte{1})
	}
	tx, err := db.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `SELECT event_id FROM auth.registration_outbox WHERE event_id=$1 FOR UPDATE`, firstID[:]); err != nil {
		t.Fatal(err)
	}
	short, cancelShort := context.WithTimeout(ctx, 500*time.Millisecond)
	unlocked, found, err := store.Claim(short, time.Second)
	cancelShort()
	if err != nil || !found || unlocked.ID != secondID {
		t.Fatal("SKIP LOCKED blocked on another owner", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	cancelled, cancelNow := context.WithCancel(ctx)
	cancelNow()
	if _, _, err := store.Claim(cancelled, time.Second); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled query", err)
	}
}
