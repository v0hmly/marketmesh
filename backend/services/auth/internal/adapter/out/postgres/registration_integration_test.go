//go:build integration

package postgres_test

import (
	"bytes"
	"context"
	"errors"
	adapter "github.com/v0hmly/marketmesh/services/auth/internal/adapter/out/postgres"
	"github.com/v0hmly/marketmesh/services/auth/internal/adapter/out/registrationwire"
	"github.com/v0hmly/marketmesh/services/auth/internal/domain/registrationevent"
	"github.com/v0hmly/marketmesh/services/auth/migrations"
	"sync"
	"testing"
	"time"
)

func TestIntegrationAtomicRegistrationOutbox(t *testing.T) {
	pool := integrationPool(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := pool.Exec(ctx, migrations.RegistrationOutboxUp); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if _, err := pool.Exec(context.Background(), migrations.RegistrationOutboxDown); err != nil {
			t.Error(err)
		}
	})
	repo, err := adapter.New(pool)
	if err != nil {
		t.Fatal(err)
	}
	eventFor := func(first byte) registrationevent.Event {
		value := integrationCredential(t, first, "unused@example.com", "private-digest")
		return registrationevent.Event{ID: [16]byte{first}, SubjectID: value.SubjectID(), OccurredAt: time.Now().UTC().Truncate(time.Microsecond)}
	}
	value := integrationCredential(t, 1, "private@example.com", "private-digest")
	event := eventFor(1)
	created, err := repo.CreateRegistration(ctx, value, event)
	if err != nil || !created {
		t.Fatal("registration failed", err)
	}
	var payload []byte
	var storedTime time.Time
	if err := pool.QueryRow(ctx, `SELECT payload,occurred_at FROM auth.registration_outbox WHERE subject_id=$1`, value.SubjectID().Bytes()).Scan(&payload, &storedTime); err != nil {
		t.Fatal(err)
	}
	decoded, err := registrationwire.Unmarshal(payload)
	if err != nil || decoded != event || !storedTime.Equal(event.OccurredAt) {
		t.Fatal("persisted event differs", err)
	}
	if bytes.Contains(payload, []byte("private@example.com")) || bytes.Contains(payload, []byte("private-digest")) {
		t.Fatal("credentials in payload")
	}
	// Retrying after an unknown commit result does not alter the original event.
	for range 2 {
		created, err = repo.CreateRegistration(ctx, value, event)
		if err != nil || created {
			t.Fatal("replay created event", err)
		}
	}
	var after []byte
	if err := pool.QueryRow(ctx, `SELECT payload FROM auth.registration_outbox WHERE subject_id=$1`, value.SubjectID().Bytes()).Scan(&after); err != nil || !bytes.Equal(payload, after) {
		t.Fatal("event changed on replay", err)
	}
	// Outbox rejection must roll back the credential inserted by the same CTE.
	if _, err := pool.Exec(ctx, `ALTER TABLE auth.registration_outbox ADD CONSTRAINT fail_insert CHECK(false) NOT VALID`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateRegistration(ctx, integrationCredential(t, 2, "rollback@example.com", "digest"), eventFor(2)); err == nil {
		t.Fatal("outbox failure ignored")
	}
	if _, err := pool.Exec(ctx, `ALTER TABLE auth.registration_outbox DROP CONSTRAINT fail_insert`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM auth.credentials WHERE identifier='rollback@example.com'`).Scan(&n); err != nil || n != 0 {
		t.Fatal("credential survived outbox failure", err)
	}
	// Existing event ID cannot create a credential without its event.
	collision := eventFor(3)
	collision.ID = event.ID
	if _, err := repo.CreateRegistration(ctx, integrationCredential(t, 3, "collision@example.com", "digest"), collision); err == nil {
		t.Fatal("event ID collision accepted")
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM auth.credentials WHERE identifier='collision@example.com'`).Scan(&n); err != nil || n != 0 {
		t.Fatal("collision credential survived", err)
	}
	// Failed credential insertion cannot create an orphan registration event.
	sameSubject := eventFor(1)
	sameSubject.ID = [16]byte{4}
	if _, err := repo.CreateRegistration(ctx, integrationCredential(t, 1, "subjectcollision@example.com", "digest"), sameSubject); err == nil {
		t.Fatal("credential collision accepted")
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM auth.registration_outbox WHERE event_id=$1`, sameSubject.ID[:]).Scan(&n); err != nil || n != 0 {
		t.Fatal("orphan event exists", err)
	}

	// Table invariants reject malformed durable work even if an adapter is bypassed.
	for _, mutation := range []string{
		"event_id=decode(repeat('00',16),'hex')",
		"event_id=decode('01','hex')",
		"subject_id=decode(repeat('00',16),'hex')",
		"payload=decode('','hex')",
		"payload=decode(repeat('01',8193),'hex')",
		"attempts=-1",
		"lease_token=decode(repeat('01',16),'hex')",
		"lease_until=now()",
		"lease_token=decode(repeat('00',16),'hex'), lease_until=now()",
	} {
		if _, err := pool.Exec(ctx, "UPDATE auth.registration_outbox SET "+mutation+" WHERE event_id=$1", event.ID[:]); err == nil {
			t.Fatal("schema accepted malformed work")
		}
	}
	canceled, cancelNow := context.WithCancel(ctx)
	cancelNow()
	if _, err := repo.CreateRegistration(canceled, integrationCredential(t, 5, "cancelled@example.com", "digest"), eventFor(5)); !errors.Is(err, context.Canceled) {
		t.Fatal("cancellation ignored", err)
	}
	const workers = 32
	start := make(chan struct{})
	results := make(chan bool, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		v := integrationCredential(t, byte(i+20), "race-outbox@example.com", "digest")
		e := eventFor(byte(i + 20))
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			created, err := repo.CreateRegistration(ctx, v, e)
			results <- created
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	wins := 0
	for created := range results {
		if created {
			wins++
		}
	}
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if wins != 1 {
		t.Fatalf("winners=%d", wins)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM auth.credentials c JOIN auth.registration_outbox o USING(subject_id) WHERE identifier='race-outbox@example.com'`).Scan(&n); err != nil || n != 1 {
		t.Fatal("concurrent registrations not paired", err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM auth.registration_outbox`).Scan(&n); err != nil || n != 2 {
		t.Fatal("extra events after conflicts", err)
	}
	if _, err := pool.Exec(ctx, migrations.RegistrationOutboxDown); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, migrations.RegistrationOutboxUp); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM auth.credentials`).Scan(&n); err != nil || n != 2 {
		t.Fatal("outbox rollback changed credentials", err)
	}
}
