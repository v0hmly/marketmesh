//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	adapter "github.com/v0hmly/marketmesh/services/user/internal/adapter/out/postgres"
	"github.com/v0hmly/marketmesh/services/user/internal/domain/profile"
	"github.com/v0hmly/marketmesh/services/user/migrations"
)

func TestIntegrationProfileIsolationCASAndMigrations(t *testing.T) {
	dsn := os.Getenv("MARKETMESH_USER_POSTGRES_DSN")
	if dsn == "" {
		t.Fatal("MARKETMESH_USER_POSTGRES_DSN required; use testdata/integration.sh")
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
	defer func() {
		if _, err := pool.Exec(context.Background(), migrations.ProfilesDown); err != nil {
			t.Error(err)
		}
	}()
	a, b, missing := profile.SubjectID{1}, profile.SubjectID{2}, profile.SubjectID{3}
	// Provisioning belongs to a future creation-event consumer; only fixtures insert.
	exec(`INSERT INTO users.profiles(subject_id,display_name) VALUES($1,'Alice'),($2,'Bob')`, a.Bytes(), b.Bytes())
	for _, tc := range []struct {
		name string
		sql  string
		args []any
	}{
		{"short subject", `INSERT INTO users.profiles(subject_id) VALUES($1)`, []any{[]byte{9}}},
		{"zero subject", `INSERT INTO users.profiles(subject_id) VALUES($1)`, []any{make([]byte, 16)}},
		{"invalid version", `UPDATE users.profiles SET version=0 WHERE subject_id=$1`, []any{a.Bytes()}},
		{"long display", `UPDATE users.profiles SET display_name=repeat('x',81) WHERE subject_id=$1`, []any{a.Bytes()}},
		{"long bio", `UPDATE users.profiles SET bio=repeat('x',1001) WHERE subject_id=$1`, []any{a.Bytes()}},
	} {
		if _, err := pool.Exec(ctx, tc.sql, tc.args...); err == nil {
			t.Fatalf("schema accepted %s", tc.name)
		}
	}
	repo, err := adapter.New(pool)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Get(ctx, missing); !errors.Is(err, profile.ErrNotReady) {
		t.Fatalf("missing Get: %v", err)
	}
	if _, err := repo.Update(ctx, missing, profile.Fields{}, 1); !errors.Is(err, profile.ErrNotReady) {
		t.Fatalf("missing Update: %v", err)
	}
	const workers = 16
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := repo.Update(ctx, a, profile.Fields{DisplayName: "Changed", Bio: "line\n\ttwo"}, 1)
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	wins := 0
	for err := range errs {
		if err == nil {
			wins++
		} else if !errors.Is(err, profile.ErrConflict) {
			t.Fatal(err)
		}
	}
	if wins != 1 {
		t.Fatalf("CAS winners=%d", wins)
	}
	got, err := repo.Get(ctx, a)
	if err != nil || got.Version != 2 || got.Fields.DisplayName != "Changed" || got.UpdatedAt.Before(got.CreatedAt) {
		t.Fatalf("updated profile=%+v err=%v", got, err)
	}
	other, err := repo.Get(ctx, b)
	if err != nil || other.Version != 1 || other.Fields.DisplayName != "Bob" {
		t.Fatal("another subject changed", err)
	}
	cancelled, cancelNow := context.WithCancel(ctx)
	cancelNow()
	if _, err := repo.Get(cancelled, a); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled read: %v", err)
	}
	if _, err := repo.Update(cancelled, a, profile.Fields{}, 2); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled update: %v", err)
	}
	exec(`CREATE ROLE user_profile_ro NOLOGIN; CREATE ROLE user_profile_rw NOLOGIN; GRANT USAGE ON SCHEMA users TO user_profile_ro,user_profile_rw; GRANT SELECT ON users.profiles TO user_profile_ro; GRANT SELECT,UPDATE ON users.profiles TO user_profile_rw`)
	defer func() {
		_, err := pool.Exec(context.Background(), `DROP OWNED BY user_profile_ro,user_profile_rw; DROP ROLE user_profile_ro,user_profile_rw`)
		if err != nil {
			t.Error(err)
		}
	}()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `SET ROLE user_profile_ro`); err != nil {
		t.Fatal(err)
	}
	ro, _ := adapter.New(conn)
	if _, err := ro.Get(ctx, a); err != nil {
		t.Fatal(err)
	}
	if _, err := ro.Update(ctx, a, profile.Fields{}, 2); err == nil {
		t.Fatal("RO role updated profile")
	}
	if _, err := conn.Exec(ctx, `SET ROLE user_profile_rw`); err != nil {
		t.Fatal(err)
	}
	rw, _ := adapter.New(conn)
	if _, err := rw.Update(ctx, a, profile.Fields{DisplayName: "RW"}, 2); err != nil {
		t.Fatal(err)
	}
	if got, err := rw.Get(ctx, a); err != nil || got.Version != 3 || got.Fields.DisplayName != "RW" {
		t.Fatal("RW immediate read failed", err)
	}
	if _, err := conn.Exec(ctx, `RESET ROLE`); err != nil {
		t.Fatal(err)
	}
	// Down/up is reversible and removes fixture data.
	exec(migrations.ProfilesDown)
	exec(migrations.ProfilesUp)
	if _, err := repo.Get(ctx, a); !errors.Is(err, profile.ErrNotReady) {
		t.Fatal("migration recreated data", err)
	}
}
