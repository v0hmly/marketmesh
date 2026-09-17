//go:build integration

package postgresaddresses_test

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	platformpostgres "github.com/v0hmly/marketmesh/platform/postgres"
	adapter "github.com/v0hmly/marketmesh/services/user/internal/adapter/out/postgresaddresses"
	"github.com/v0hmly/marketmesh/services/user/internal/application/addresses"
	"github.com/v0hmly/marketmesh/services/user/internal/domain/address"
	"github.com/v0hmly/marketmesh/services/user/internal/domain/profile"
	"github.com/v0hmly/marketmesh/services/user/migrations"
	"os"
	"sync"
	"testing"
	"time"
)

type transactionPool struct {
	pool *pgxpool.Pool
	fail bool
}

func (p transactionPool) WithinTransaction(ctx context.Context, _ platformpostgres.TransactionOptions, fn platformpostgres.TransactionFunc) error {
	tx, e := p.pool.BeginTx(ctx, pgx.TxOptions{})
	if e != nil {
		return e
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if e = fn(ctx, tx); e != nil {
		return e
	}
	if p.fail {
		return errors.New("injected precommit failure")
	}
	return tx.Commit(ctx)
}
func TestIntegrationAddressTransactionsAndMigrations(t *testing.T) {
	dsn := os.Getenv("MARKETMESH_USER_POSTGRES_DSN")
	if dsn == "" {
		t.Fatal("MARKETMESH_USER_POSTGRES_DSN required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
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
	defer func() { _, _ = pool.Exec(context.Background(), `DROP SCHEMA users CASCADE`) }()
	a, b := profile.SubjectID{1}, profile.SubjectID{2}
	exec(`INSERT INTO users.profiles(subject_id,display_name) VALUES($1,'old')`, a.Bytes())
	exec(migrations.AddressesUp)
	exec(`INSERT INTO users.profiles(subject_id) VALUES($1)`, b.Bytes())
	repo, _ := adapter.New(pool, transactionPool{pool: pool})
	book, e := repo.List(ctx, a)
	if e != nil || book.Version != 1 || len(book.Addresses) != 0 {
		t.Fatal(book, e)
	}
	if _, e = repo.List(ctx, profile.SubjectID{3}); !errors.Is(e, profile.ErrNotReady) {
		t.Fatal(e)
	}
	f := address.Fields{Recipient: "Recipient", Phone: "123456789", Country: "Country", City: "City", StreetHouse: "Street"}
	mutate := func(op addresses.Operation, id address.ID, v uint64) (address.Book, error) {
		return repo.Mutate(ctx, a, addresses.Command{Operation: op, ID: id, Fields: f, ExpectedVersion: v})
	}
	first, e := mutate(addresses.Create, address.ID{}, 1)
	if e != nil || len(first.Addresses) != 1 || !first.Addresses[0].IsDefault {
		t.Fatal(first, e)
	}
	id1 := first.Addresses[0].ID
	second, e := mutate(addresses.Create, address.ID{}, 2)
	if e != nil || len(second.Addresses) != 2 {
		t.Fatal(second, e)
	}
	var id2 address.ID
	for _, a := range second.Addresses {
		if a.ID != id1 {
			id2 = a.ID
			if a.IsDefault {
				t.Fatal("second default")
			}
		}
	}
	switched, e := mutate(addresses.SetDefault, id2, 3)
	if e != nil || switched.Version != 4 {
		t.Fatal(e)
	}
	for _, a := range switched.Addresses {
		if a.IsDefault != (a.ID == id2) {
			t.Fatal("incorrect default")
		}
	}
	remaining, e := mutate(addresses.Delete, id2, 4)
	if e != nil || len(remaining.Addresses) != 1 || remaining.Addresses[0].IsDefault {
		t.Fatal(remaining, e)
	}
	if _, e = repo.Mutate(ctx, b, addresses.Command{Operation: addresses.Delete, ID: id1, ExpectedVersion: 1}); !errors.Is(e, address.ErrNotFound) {
		t.Fatal("owner isolation", e)
	}
	if _, e = mutate(addresses.Delete, address.ID{99}, 5); !errors.Is(e, address.ErrNotFound) {
		t.Fatal(e)
	}
	failing, _ := adapter.New(pool, transactionPool{pool: pool, fail: true})
	if got, e := failing.Mutate(ctx, a, addresses.Command{Operation: addresses.Create, Fields: f, ExpectedVersion: 5}); e == nil || got.Version != 0 {
		t.Fatal("uncommitted snapshot returned")
	}
	after, e := repo.List(ctx, a)
	if e != nil || after.Version != 5 || len(after.Addresses) != 1 {
		t.Fatal("rollback failed", after, e)
	}
	const workers = 12
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	start := make(chan struct{})
	for range workers {
		wg.Add(1)
		go func() { defer wg.Done(); <-start; _, e := mutate(addresses.Create, address.ID{}, 5); errs <- e }()
	}
	close(start)
	wg.Wait()
	close(errs)
	wins := 0
	for e := range errs {
		if e == nil {
			wins++
		} else if !errors.Is(e, address.ErrConflict) {
			t.Fatal(e)
		}
	}
	if wins != 1 {
		t.Fatal("CAS winners", wins)
	}
	after, _ = repo.List(ctx, a)
	for len(after.Addresses) < 19 {
		after, e = mutate(addresses.Create, address.ID{}, after.Version)
		if e != nil {
			t.Fatal(e)
		}
	}
	// All contenders retry only after a fresh read; never exceed the hard limit.
	errs = make(chan error, workers)
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				snapshot, e := repo.List(ctx, a)
				if e != nil {
					errs <- e
					return
				}
				_, e = mutate(addresses.Create, address.ID{}, snapshot.Version)
				if errors.Is(e, address.ErrConflict) {
					continue
				}
				errs <- e
				return
			}
		}()
	}
	wg.Wait()
	close(errs)
	wins = 0
	for e := range errs {
		if e == nil {
			wins++
		} else if !errors.Is(e, address.ErrLimit) {
			t.Fatal(e)
		}
	}
	if wins != 1 {
		t.Fatal("limit winners", wins)
	}
	full, _ := repo.List(ctx, a)
	if len(full.Addresses) != 20 {
		t.Fatal("limit bypass")
	}
	v := full.Version
	if _, e = mutate(addresses.Create, address.ID{}, v); !errors.Is(e, address.ErrLimit) {
		t.Fatal(e)
	}
	updated, e := mutate(addresses.Update, id1, v)
	if e != nil || updated.Version != v+1 || len(updated.Addresses) != 20 {
		t.Fatal(e)
	}
	var profileVersion int64
	var display string
	if e = pool.QueryRow(ctx, `SELECT version,display_name FROM users.profiles WHERE subject_id=$1`, a.Bytes()).Scan(&profileVersion, &display); e != nil || profileVersion != 1 || display != "old" {
		t.Fatal("profile changed", e)
	}
	// Concurrent default mutations use the same CAS and must have exactly one winner.
	errs = make(chan error, 2)
	for _, entry := range updated.Addresses[:2] {
		wg.Add(1)
		go func(id address.ID) {
			defer wg.Done()
			_, e := mutate(addresses.SetDefault, id, updated.Version)
			errs <- e
		}(entry.ID)
	}
	wg.Wait()
	close(errs)
	wins = 0
	for e := range errs {
		if e == nil {
			wins++
		} else if !errors.Is(e, address.ErrConflict) {
			t.Fatal(e)
		}
	}
	if wins != 1 {
		t.Fatal("default CAS winners", wins)
	}
	final, _ := repo.List(ctx, a)
	defaults := 0
	for _, entry := range final.Addresses {
		if entry.IsDefault {
			defaults++
		}
	}
	if defaults != 1 {
		t.Fatal("defaults", defaults)
	}
	// Drain and recreate: an empty book again gets a first default.
	for _, entry := range final.Addresses {
		final, e = mutate(addresses.Delete, entry.ID, final.Version)
		if e != nil {
			t.Fatal(e)
		}
	}
	final, e = mutate(addresses.Create, address.ID{}, final.Version)
	if e != nil || len(final.Addresses) != 1 || !final.Addresses[0].IsDefault {
		t.Fatal(final, e)
	}
	exec(migrations.AddressesDown)
	exec(`UPDATE users.profiles SET display_name='after rollback' WHERE subject_id=$1`, a.Bytes())
	exec(migrations.AddressesUp)
	reinstalled, e := repo.List(ctx, a)
	if e != nil || reinstalled.Version != 1 || len(reinstalled.Addresses) != 0 {
		t.Fatal(reinstalled, e)
	}
}
