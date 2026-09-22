//go:build integration

package postgres

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/v0hmly/marketmesh/services/files/internal/domain/file"
	"github.com/v0hmly/marketmesh/services/files/migrations"
)

// FILES_POSTGRES_TEST_DSN must point to an empty disposable database.
// Schema and fixtures are rolled back even when an assertion fails.
func TestClaimPrioritizesProcessingOverRecurringCleanup(t *testing.T) {
	dsn := os.Getenv("FILES_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("FILES_POSTGRES_TEST_DSN not configured")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal("test database unavailable")
	}
	defer conn.Close(context.Background())
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	var existing bool
	if err := tx.QueryRow(ctx, "SELECT to_regnamespace('files') IS NOT NULL").Scan(&existing); err != nil || existing {
		t.Fatal("test requires an empty database", err)
	}
	if _, err := tx.Exec(ctx, migrations.Up); err != nil {
		t.Fatal(err)
	}
	repo, err := New(tx)
	if err != nil {
		t.Fatal(err)
	}
	newID := func() file.ID {
		var id file.ID
		if _, err := rand.Read(id[:]); err != nil {
			t.Fatal(err)
		}
		return id
	}
	seed := func(state file.State, age time.Duration) file.Record {
		now := time.Now().UTC()
		digest := file.Digest{1}
		v := file.Record{ID: newID(), Owner: file.Owner{Tenant: newID(), Subject: newID()}, IdempotencyKey: newID(), ObjectKey: newID().String(), CreatedAt: now.Add(-age), ExpiresAt: now.Add(time.Hour), Manifest: file.Manifest{Format: file.PNG, Size: 1, SHA256: digest, Parts: []file.Part{{Size: 1, SHA256: digest}}}}
		r, err := repo.Create(ctx, v)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `UPDATE files.uploads SET state=$2,updated_at=$3,clean_format='image/png',clean_size=1,clean_sha256=$4 WHERE id=$1`, r.ID[:], state, now.Add(-age), digest[:]); err != nil {
			t.Fatal(err)
		}
		return r
	}
	const backlog = 128
	for i := range backlog {
		seed([]file.State{file.Ready, file.Deleted, file.Expired, file.Rejected}[i%4], 24*time.Hour)
	}
	leased := seed(file.Scanning, 3*time.Hour)
	if _, err := tx.Exec(ctx, `UPDATE files.uploads SET lease_until=clock_timestamp()+interval '10 minutes' WHERE id=$1`, leased.ID[:]); err != nil {
		t.Fatal(err)
	}
	pending := seed(file.Uploading, 4*time.Hour)
	deferred := seed(file.Ready, 25*time.Hour)
	if _, err := tx.Exec(ctx, `UPDATE files.uploads SET cleanup_after=clock_timestamp()+interval '1 hour' WHERE id=$1`, deferred.ID[:]); err != nil {
		t.Fatal(err)
	}
	replicating := seed(file.Replicating, 2*time.Hour)
	scanning := seed(file.Scanning, time.Hour)
	expiredUpload := seed(file.Uploading, 5*time.Hour)
	if _, err := tx.Exec(ctx, `UPDATE files.uploads SET expires_at=clock_timestamp()-interval '1 minute' WHERE id=$1`, expiredUpload.ID[:]); err != nil {
		t.Fatal(err)
	}
	for _, want := range []file.ID{replicating.ID, scanning.ID, expiredUpload.ID} {
		got, err := repo.Claim(ctx)
		if err != nil || got.ID != want || got.Version != 2 {
			t.Fatalf("claim before cleanup: id=%s version=%d err=%v; want %s", got.ID, got.Version, err, want)
		}
	}
	for range backlog {
		got, err := repo.Claim(ctx)
		if err != nil {
			t.Fatal("cleanup did not resume after foreground work", err)
		}
		if got.ID == pending.ID || got.ID == deferred.ID || got.ID == leased.ID || got.State == file.Scanning || got.State == file.Replicating || got.State == file.Uploading {
			t.Fatal("ineligible or leased row reclaimed")
		}
		if err := repo.DeferCleanup(ctx, got); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := repo.Claim(ctx); !errors.Is(err, file.ErrNotFound) {
		t.Fatal("deferred cleanup immediately reclaimed", err)
	}
	// Once the next hourly sweep is due, new foreground work still wins.
	if _, err := tx.Exec(ctx, `UPDATE files.uploads SET cleanup_after=clock_timestamp()-interval '1 second' WHERE state IN ('READY','DELETED','EXPIRED','REJECTED')`); err != nil {
		t.Fatal(err)
	}
	next := seed(file.Scanning, time.Minute)
	if got, err := repo.Claim(ctx); err != nil || got.ID != next.ID {
		t.Fatal("recurring sweep starved a new upload", err)
	}
}
