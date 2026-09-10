//go:build integration

package postgresbackfill_test

import (
	"bytes"
	"context"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/v0hmly/marketmesh/services/auth/internal/adapter/out/postgresbackfill"
	"github.com/v0hmly/marketmesh/services/auth/internal/adapter/out/postgresoutbox"
	"github.com/v0hmly/marketmesh/services/auth/internal/adapter/out/registrationevent"
	app "github.com/v0hmly/marketmesh/services/auth/internal/application/backfillregistration"
	"github.com/v0hmly/marketmesh/services/auth/internal/domain/credential"
	"github.com/v0hmly/marketmesh/services/auth/migrations"
	"os"
	"sync"
	"testing"
	"time"
)

func TestIntegrationBackfillConcurrentReplayAndRollback(t *testing.T) {
	dsn := os.Getenv("MARKETMESH_AUTH_POSTGRES_DSN")
	if dsn == "" {
		t.Fatal("MARKETMESH_AUTH_POSTGRES_DSN required (disposable database)")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
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
	exec(migrations.CredentialsUp + migrations.RegistrationOutboxUp)
	defer func() {
		if _, err := db.Exec(context.Background(), migrations.RegistrationOutboxDown+migrations.CredentialsDown); err != nil {
			t.Error(err)
		}
	}()
	id := credential.SubjectID{1}
	exec(`INSERT INTO auth.credentials(subject_id,identifier,password_digest) VALUES($1,'test@example.invalid','fixture')`, id.Bytes())
	store, _ := postgresbackfill.New(db)
	factory := registrationevent.New()
	service, _ := app.New(store, factory)
	r, err := service.Page(ctx, app.Options{Limit: 1})
	if err != nil || r.Missing != 1 || r.Inserted != 0 {
		t.Fatal(r, err)
	}
	const n = 8
	var wg sync.WaitGroup
	errs := make(chan error, n)
	for range n {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := service.Page(ctx, app.Options{Limit: 10, Apply: true}); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var count int
	var payload, eventID []byte
	if err := db.QueryRow(ctx, `SELECT count(*) FROM auth.registration_outbox`).Scan(&count); err != nil || count != 1 {
		t.Fatal(count, err)
	}
	if err := db.QueryRow(ctx, `SELECT event_id,payload FROM auth.registration_outbox`).Scan(&eventID, &payload); err != nil {
		t.Fatal(err)
	}
	publisher, _ := postgresoutbox.New(db)
	record, found, err := publisher.Claim(ctx, time.Minute)
	if err != nil || !found {
		t.Fatal(err)
	}
	if changed, err := store.Replay(ctx, id, time.Now()); err != nil || changed {
		t.Fatal("lease reset", err)
	}
	if r, err = service.Page(ctx, app.Options{Limit: 10, Apply: true, ReplayPublished: true}); err != nil || r.Requeued != 0 {
		t.Fatal(r, err)
	}
	if ok, err := publisher.MarkPublished(ctx, record); err != nil || !ok {
		t.Fatal("lease lost", err)
	}
	rows, err := store.Scan(ctx, []byte{}, 1)
	if err != nil || len(rows) != 1 || rows[0].PublishedAt == nil {
		t.Fatal(err)
	}
	stamp := *rows[0].PublishedAt
	var wins int
	var mu sync.Mutex
	for range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ok, err := store.Replay(ctx, id, stamp)
			if err != nil {
				t.Error(err)
			}
			if ok {
				mu.Lock()
				wins++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if wins != 1 {
		t.Fatal("concurrent reset", wins)
	}
	record, found, err = publisher.Claim(ctx, time.Minute)
	if err != nil || !found || !bytes.Equal(record.Payload, payload) || !bytes.Equal(record.ID[:], eventID) {
		t.Fatal("identity changed", err)
	}
	if ok, err := publisher.MarkPublished(ctx, record); err != nil || !ok {
		t.Fatal(err)
	}
	if ok, err := store.Replay(ctx, id, stamp); err != nil || ok {
		t.Fatal("stale snapshot reset", err)
	}
	absent, _ := factory.New(ctx, credential.SubjectID{2})
	if ok, err := store.Ensure(ctx, absent); err != nil || ok {
		t.Fatal("created without credential", err)
	}
	canceled, stop := context.WithCancel(ctx)
	stop()
	if _, err := store.Ensure(canceled, absent); err == nil {
		t.Fatal("canceled write")
	}
	exec(migrations.RegistrationOutboxDown)
	exec(migrations.RegistrationOutboxUp)
	if err := db.QueryRow(ctx, `SELECT count(*) FROM auth.credentials`).Scan(&count); err != nil || count != 1 {
		t.Fatal("rollback damaged credentials", err)
	}
}
