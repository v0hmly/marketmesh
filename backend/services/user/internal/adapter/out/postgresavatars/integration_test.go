//go:build integration

package postgresavatars_test

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	platformpostgres "github.com/v0hmly/marketmesh/platform/postgres"
	adapter "github.com/v0hmly/marketmesh/services/user/internal/adapter/out/postgresavatars"
	"github.com/v0hmly/marketmesh/services/user/internal/domain/avatar"
	"github.com/v0hmly/marketmesh/services/user/internal/domain/profile"
	"github.com/v0hmly/marketmesh/services/user/migrations"
)

type transactionPool struct {
	pool *pgxpool.Pool
	fail bool
}

func (p transactionPool) WithinTransaction(ctx context.Context, _ platformpostgres.TransactionOptions, fn platformpostgres.TransactionFunc) error {
	tx, err := p.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err = fn(ctx, tx); err != nil {
		return err
	}
	if p.fail {
		return errors.New("injected before commit")
	}
	return tx.Commit(ctx)
}
func TestIntegrationAvatarCASRetirementLeasesAndMigrations(t *testing.T) {
	dsn := os.Getenv("MARKETMESH_USER_POSTGRES_DSN")
	if dsn == "" {
		t.Fatal("MARKETMESH_USER_POSTGRES_DSN required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
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
		if _, err := pool.Exec(context.Background(), `DROP SCHEMA users CASCADE`); err != nil {
			t.Error(err)
		}
	}()
	a, b, missing := profile.SubjectID{1}, profile.SubjectID{2}, profile.SubjectID{3}
	exec(`INSERT INTO users.profiles(subject_id,display_name) VALUES($1,'existing')`, a.Bytes())
	exec(migrations.AvatarUp)
	exec(`INSERT INTO users.profiles(subject_id) VALUES($1)`, b.Bytes())
	repo, _ := adapter.New(pool, transactionPool{pool: pool})
	for _, owner := range []profile.SubjectID{a, b} {
		s, err := repo.Get(ctx, owner)
		if err != nil || s.SubjectID != owner || s.Version != 1 || s.FileID != (avatar.FileID{}) {
			t.Fatal(s, err)
		}
	}
	if _, err := repo.Set(ctx, missing, avatar.FileID{1}, 1); !errors.Is(err, profile.ErrNotReady) {
		t.Fatal(err)
	}
	if _, err := repo.Set(ctx, a, avatar.FileID{1}, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Set(ctx, a, avatar.FileID{2}, 2); err != nil {
		t.Fatal(err)
	}
	failed, _ := adapter.New(pool, transactionPool{pool: pool, fail: true})
	if _, err := failed.Set(ctx, a, avatar.FileID{3}, 3); err == nil {
		t.Fatal("injected failure lost")
	}
	var count int
	if err = pool.QueryRow(ctx, `SELECT count(*) FROM users.avatar_retirements`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	state, err := repo.Get(ctx, a)
	if err != nil || state.Version != 3 || state.FileID != (avatar.FileID{2}) || count != 1 {
		t.Fatal("non atomic rollback", state, count, err)
	}
	const n = 16
	start := make(chan struct{})
	results := make(chan error, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := repo.Set(ctx, a, avatar.FileID{byte(100 + i)}, 3)
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	wins := 0
	for err := range results {
		if err == nil {
			wins++
		} else if !errors.Is(err, avatar.ErrConflict) {
			t.Fatal(err)
		}
	}
	if wins != 1 {
		t.Fatal("CAS winners", wins)
	}
	state, err = repo.Get(ctx, a)
	if err != nil || state.Version != 4 {
		t.Fatal(state, err)
	}
	var pv int64
	var name string
	if err = pool.QueryRow(ctx, `SELECT version,display_name FROM users.profiles WHERE subject_id=$1`, a.Bytes()).Scan(&pv, &name); err != nil || pv != 1 || name != "existing" {
		t.Fatal("profile changed", err)
	}
	other, err := repo.Get(ctx, b)
	if err != nil || other.Version != 1 || other.FileID != (avatar.FileID{}) {
		t.Fatal("other owner changed", err)
	}
	first, ok, err := repo.Claim(ctx, time.Minute)
	if err != nil || !ok || first.SubjectID != a || first.FileID == state.FileID {
		t.Fatal("claim", first, ok, err)
	}
	second, ok, err := repo.Claim(ctx, time.Minute)
	if err != nil || !ok || second.FileID == first.FileID {
		t.Fatal("second claim", err)
	}
	if _, ok, err := repo.Claim(ctx, time.Minute); err != nil || ok {
		t.Fatal("leased row reclaimed", err)
	}
	if ok, err = repo.Finish(ctx, second); err != nil || !ok {
		t.Fatal(err)
	}
	exec(`UPDATE users.avatar_retirements SET lease_until=clock_timestamp()-interval '1 second' WHERE subject_id=$1 AND file_id=$2`, a.Bytes(), first.FileID[:])
	takeover, ok, err := repo.Claim(ctx, time.Minute)
	if err != nil || !ok || takeover.FileID != first.FileID || takeover.LeaseToken == first.LeaseToken || takeover.Attempts != 2 {
		t.Fatal("takeover", takeover, err)
	}
	if ok, err = repo.Finish(ctx, first); err != nil || ok {
		t.Fatal("stale finish accepted", err)
	}
	if ok, err = repo.Retry(ctx, first, time.Second); err != nil || ok {
		t.Fatal("stale retry accepted", err)
	}
	if ok, err = repo.Retry(ctx, takeover, time.Minute); err != nil || !ok {
		t.Fatal(err)
	}
	if _, ok, err := repo.Claim(ctx, time.Minute); err != nil || ok {
		t.Fatal("backoff ignored", err)
	}
	exec(`UPDATE users.avatar_retirements SET next_attempt_at=clock_timestamp()-interval '1 second' WHERE subject_id=$1 AND file_id=$2`, a.Bytes(), first.FileID[:])
	last, ok, err := repo.Claim(ctx, time.Minute)
	if err != nil || !ok {
		t.Fatal(err)
	}
	if ok, err = repo.Finish(ctx, last); err != nil || !ok {
		t.Fatal(err)
	}
	if _, err = repo.Set(ctx, a, first.FileID, 4); !errors.Is(err, avatar.ErrUnavailable) {
		t.Fatal("retired ID reattached", err)
	}
	if _, err = repo.Set(ctx, a, avatar.FileID{}, 4); err != nil {
		t.Fatal(err)
	}
	current, ok, err := repo.Claim(ctx, time.Minute)
	if err != nil || !ok || current.FileID != state.FileID {
		t.Fatal("clear did not enqueue", err)
	}
	if ok, err = repo.Finish(ctx, current); err != nil || !ok {
		t.Fatal(err)
	}
	for _, sql := range []string{`UPDATE users.profiles SET avatar_version=0`, `UPDATE users.profiles SET avatar_file_id='\x00'::bytea`} {
		if _, err = pool.Exec(ctx, sql); err == nil {
			t.Fatal("invalid avatar persisted")
		}
	}
	exec(migrations.AvatarDown)
	exec(migrations.AvatarUp)
	state, err = repo.Get(ctx, a)
	if err != nil || state.Version != 1 || state.FileID != (avatar.FileID{}) {
		t.Fatal("migration roundtrip", state, err)
	}
}
