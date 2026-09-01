//go:build integration

package postgres_test

import (
	"context"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	postgresadapter "github.com/v0hmly/marketmesh/services/auth/internal/adapter/out/postgres"
	"github.com/v0hmly/marketmesh/services/auth/internal/domain/credential"
	"github.com/v0hmly/marketmesh/services/auth/migrations"
)

func TestIntegrationCredentialLifecycle(t *testing.T) {
	pool := integrationPool(t)
	repository, err := postgresadapter.New(pool)
	if err != nil {
		t.Fatalf("postgres.New() error = %v", err)
	}
	value := integrationCredential(t, 1, "user@example.com", "digest-v1")
	created, err := repository.Create(context.Background(), value)
	if err != nil || !created {
		t.Fatalf("Create() = created %v, error %v", created, err)
	}
	created, err = repository.Create(context.Background(), integrationCredential(t, 2, "user@example.com", "other-digest"))
	if err != nil || created {
		t.Fatalf("Create(duplicate) = created %v, error %v", created, err)
	}

	stored, found, err := repository.FindByIdentifier(context.Background(), value.Identifier())
	if err != nil || !found {
		t.Fatalf("FindByIdentifier() = found %v, error %v", found, err)
	}
	if stored.SubjectID() != value.SubjectID() || stored.PasswordDigest() != value.PasswordDigest() {
		t.Fatal("FindByIdentifier() returned a different credential")
	}
	nextDigest := mustIntegrationDigest(t, "digest-v2")
	if err := repository.UpdatePasswordDigest(context.Background(), stored.SubjectID(), stored.PasswordDigest(), nextDigest); err != nil {
		t.Fatalf("UpdatePasswordDigest() error = %v", err)
	}
	updated, found, err := repository.FindByIdentifier(context.Background(), value.Identifier())
	if err != nil || !found || updated.PasswordDigest() != nextDigest {
		t.Fatalf("FindByIdentifier(updated) = found %v, digest %q, error %v", found, updated.PasswordDigest().String(), err)
	}
}

func TestIntegrationConcurrentRegistrationCreatesOneCredential(t *testing.T) {
	pool := integrationPool(t)
	repository, err := postgresadapter.New(pool)
	if err != nil {
		t.Fatalf("postgres.New() error = %v", err)
	}

	const workers = 32
	values := make([]credential.Credential, workers)
	for index := range workers {
		values[index] = integrationCredential(t, byte(index+1), "race@example.com", "digest")
	}
	start := make(chan struct{})
	results := make(chan bool, workers)
	errorsChannel := make(chan error, workers)
	var group sync.WaitGroup
	group.Add(workers)
	for _, value := range values {
		value := value
		go func() {
			defer group.Done()
			<-start
			created, err := repository.Create(
				context.Background(),
				value,
			)
			results <- created
			errorsChannel <- err
		}()
	}
	close(start)
	group.Wait()
	close(results)
	close(errorsChannel)

	createdCount := 0
	for created := range results {
		if created {
			createdCount++
		}
	}
	for err := range errorsChannel {
		if err != nil {
			t.Fatalf("Create() error = %v", err)
		}
	}
	if createdCount != 1 {
		t.Fatalf("successful inserts = %d, want 1", createdCount)
	}
	var rows int
	if err := pool.QueryRow(context.Background(), `SELECT count(*) FROM auth.credentials WHERE identifier = $1`, "race@example.com").Scan(&rows); err != nil {
		t.Fatalf("count credentials: %v", err)
	}
	if rows != 1 {
		t.Fatalf("stored rows = %d, want 1", rows)
	}
}

func integrationPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("MARKETMESH_AUTH_POSTGRES_DSN")
	if dsn == "" {
		t.Fatal("MARKETMESH_AUTH_POSTGRES_DSN is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool.New() error = %v", err)
	}
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, migrations.CredentialsDown); err != nil {
		t.Fatalf("reset schema: %v", err)
	}
	if _, err := pool.Exec(ctx, migrations.CredentialsUp); err != nil {
		t.Fatalf("apply migration: %v", err)
	}
	return pool
}

func integrationCredential(t *testing.T, firstByte byte, identifierValue string, digestValue string) credential.Credential {
	t.Helper()
	identifier, err := credential.NewIdentifier(identifierValue)
	if err != nil {
		t.Fatalf("NewIdentifier() error = %v", err)
	}
	var subjectID credential.SubjectID
	subjectID[0] = firstByte
	return credential.New(subjectID, identifier, mustIntegrationDigest(t, digestValue))
}

func mustIntegrationDigest(t *testing.T, value string) credential.PasswordDigest {
	t.Helper()
	digest, err := credential.NewPasswordDigest(value)
	if err != nil {
		t.Fatalf("NewPasswordDigest() error = %v", err)
	}
	return digest
}
