//go:build integration

package postgressettings_test

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
	adapter "github.com/v0hmly/marketmesh/services/user/internal/adapter/out/postgressettings"
	"github.com/v0hmly/marketmesh/services/user/internal/domain/profile"
	"github.com/v0hmly/marketmesh/services/user/internal/domain/settings"
	"github.com/v0hmly/marketmesh/services/user/migrations"
	"math"
	"os"
	"sync"
	"testing"
	"time"
)

func TestIntegrationSettingsCASIsolationAndMigrations(t *testing.T) {
	dsn := os.Getenv("MARKETMESH_USER_POSTGRES_DSN")
	if dsn == "" {
		t.Fatal("MARKETMESH_USER_POSTGRES_DSN required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pool, e := pgxpool.New(ctx, dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer pool.Close()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, e := pool.Exec(ctx, sql, args...); e != nil {
			t.Fatal(e)
		}
	}
	exec(migrations.ProfilesUp)
	defer func() {
		if _, e := pool.Exec(context.Background(), `DROP SCHEMA users CASCADE`); e != nil {
			t.Error(e)
		}
	}()
	a, b, missing := profile.SubjectID{1}, profile.SubjectID{2}, profile.SubjectID{3}
	exec(`INSERT INTO users.profiles(subject_id,display_name) VALUES($1,'existing')`, a.Bytes())
	exec(migrations.SettingsUp)
	exec(`INSERT INTO users.profiles(subject_id) VALUES($1)`, b.Bytes())
	repo, _ := adapter.New(pool)
	for _, id := range []profile.SubjectID{a, b} {
		s, e := repo.Get(ctx, id)
		if e != nil || s.Theme != settings.System || s.Version != 1 || s.SubjectID != id {
			t.Fatal(s, e)
		}
	}
	if _, e = repo.Get(ctx, missing); !errors.Is(e, profile.ErrNotReady) {
		t.Fatal(e)
	}
	if _, e = repo.Update(ctx, missing, settings.Dark, 1); !errors.Is(e, profile.ErrNotReady) {
		t.Fatal(e)
	}
	// Settings migration and operations do not require address-book deployment.
	first, e := repo.Update(ctx, a, settings.Light, 1)
	if e != nil || first.Theme != settings.Light || first.Version != 2 {
		t.Fatal(first, e)
	}
	exec(migrations.AddressesUp)
	const workers = 16
	errs := make(chan error, workers)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; _, e := repo.Update(ctx, a, settings.Dark, 2); errs <- e }()
	}
	close(start)
	wg.Wait()
	close(errs)
	wins := 0
	for e := range errs {
		if e == nil {
			wins++
		} else if !errors.Is(e, settings.ErrConflict) {
			t.Fatal(e)
		}
	}
	if wins != 1 {
		t.Fatal("CAS winners", wins)
	}
	s, e := repo.Get(ctx, a)
	if e != nil || s.Version != 3 || s.Theme != settings.Dark {
		t.Fatal(s, e)
	}
	other, e := repo.Get(ctx, b)
	if e != nil || other.Version != 1 || other.Theme != settings.System {
		t.Fatal("owner isolation", other, e)
	}
	var pv, bv int64
	var name string
	if e = pool.QueryRow(ctx, `SELECT version,address_book_version,display_name FROM users.profiles WHERE subject_id=$1`, a.Bytes()).Scan(&pv, &bv, &name); e != nil || pv != 1 || bv != 1 || name != "existing" {
		t.Fatal("settings changed profile/book", e)
	}
	exec(`UPDATE users.profiles SET version=version+1,address_book_version=address_book_version+1 WHERE subject_id=$1`, a.Bytes())
	s, e = repo.Update(ctx, a, settings.System, 3)
	if e != nil || s.Version != 4 {
		t.Fatal("other version changes conflicted", e)
	}
	for _, tc := range []struct {
		theme   settings.Theme
		version uint64
	}{{"", 4}, {"unknown", 4}, {settings.Light, 0}, {settings.Light, math.MaxInt64}, {settings.Light, math.MaxUint64}} {
		if _, e = repo.Update(ctx, a, tc.theme, tc.version); !errors.Is(e, settings.ErrInvalid) {
			t.Fatal(e)
		}
	}
	for _, sql := range []string{`UPDATE users.profiles SET theme='unknown'`, `UPDATE users.profiles SET settings_version=0`} {
		if _, e = pool.Exec(ctx, sql); e == nil {
			t.Fatal("schema accepted invalid settings")
		}
	}
	exec(`UPDATE users.profiles SET settings_version=$2 WHERE subject_id=$1`, b.Bytes(), int64(math.MaxInt64-1))
	terminal, e := repo.Update(ctx, b, settings.Dark, math.MaxInt64-1)
	if e != nil || terminal.Version != math.MaxInt64 {
		t.Fatal(terminal, e)
	}
	if _, e = repo.Update(ctx, b, settings.Light, math.MaxInt64); !errors.Is(e, settings.ErrInvalid) {
		t.Fatal(e)
	}
	cancelled, stop := context.WithCancel(ctx)
	stop()
	if _, e = repo.Update(cancelled, a, settings.Light, 4); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	s, e = repo.Get(ctx, a)
	if e != nil || s.Version != 4 || s.Theme != settings.System {
		t.Fatal("invalid write changed state", s, e)
	}
	exec(migrations.SettingsDown)
	exec(`UPDATE users.profiles SET display_name='old runtime',version=version+1 WHERE subject_id=$1`, a.Bytes())
	exec(migrations.SettingsUp)
	s, e = repo.Get(ctx, a)
	if e != nil || s.Version != 1 || s.Theme != settings.System {
		t.Fatal("migration roundtrip", s, e)
	}
}
