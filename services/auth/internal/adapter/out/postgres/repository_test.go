package postgres

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/v0hmly/marketmesh/services/auth/internal/domain/credential"
)

func TestCreateUsesStaticParameterizedStatement(t *testing.T) {
	t.Parallel()

	executor := &executorStub{tag: pgconn.NewCommandTag("INSERT 0 1")}
	repository, err := New(executor)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	value := testCredential(t, "user+'@example.com")
	created, err := repository.Create(context.Background(), value)
	if err != nil || !created {
		t.Fatalf("Create() = created %v, error %v", created, err)
	}
	if executor.sql != createCredentialSQL || strings.Contains(executor.sql, value.Identifier().String()) {
		t.Fatalf("Create() SQL = %q", executor.sql)
	}
	if len(executor.arguments) != 3 || executor.arguments[1] != value.Identifier().String() {
		t.Fatalf("Create() arguments = %#v", executor.arguments)
	}
}

func TestCreateReportsDuplicateWithoutError(t *testing.T) {
	t.Parallel()

	repository, err := New(&executorStub{tag: pgconn.NewCommandTag("INSERT 0 0")})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	created, err := repository.Create(context.Background(), testCredential(t, "duplicate@example.com"))
	if err != nil || created {
		t.Fatalf("Create() = created %v, error %v", created, err)
	}
}

func TestFindMapsStoredCredentialAndNotFound(t *testing.T) {
	t.Parallel()

	identifier, err := credential.NewIdentifier("user@example.com")
	if err != nil {
		t.Fatalf("NewIdentifier() error = %v", err)
	}
	subjectID := make([]byte, credential.SubjectIDBytes)
	subjectID[0] = 4
	executor := &executorStub{row: rowStub{values: []any{subjectID, "user@example.com", "encoded-digest"}}}
	repository, err := New(executor)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	value, found, err := repository.FindByIdentifier(context.Background(), identifier)
	if err != nil || !found {
		t.Fatalf("FindByIdentifier() = found %v, error %v", found, err)
	}
	if value.SubjectID().Bytes()[0] != 4 || value.Identifier() != identifier || value.PasswordDigest().String() != "encoded-digest" {
		t.Fatal("FindByIdentifier() mapped wrong credential")
	}

	executor.row = rowStub{err: pgx.ErrNoRows}
	_, found, err = repository.FindByIdentifier(context.Background(), identifier)
	if err != nil || found {
		t.Fatalf("FindByIdentifier(missing) = found %v, error %v", found, err)
	}
}

func TestErrorsAreStableAndStillUnwrap(t *testing.T) {
	t.Parallel()

	cause := errors.New("driver details identifier=user@example.com password=secret")
	repository, err := New(&executorStub{err: cause})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_, err = repository.Create(context.Background(), testCredential(t, "user@example.com"))
	if err == nil || err.Error() != "auth postgres: create credential failed" || !errors.Is(err, cause) {
		t.Fatalf("Create() error = %v", err)
	}
}

func TestUpdatePasswordDigestUsesCompareAndSwap(t *testing.T) {
	t.Parallel()

	executor := &executorStub{tag: pgconn.NewCommandTag("UPDATE 1")}
	repository, err := New(executor)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	previous, err := credential.NewPasswordDigest("previous")
	if err != nil {
		t.Fatalf("NewPasswordDigest(previous) error = %v", err)
	}
	next, err := credential.NewPasswordDigest("next")
	if err != nil {
		t.Fatalf("NewPasswordDigest(next) error = %v", err)
	}
	var subjectID credential.SubjectID
	subjectID[0] = 8
	if err := repository.UpdatePasswordDigest(context.Background(), subjectID, previous, next); err != nil {
		t.Fatalf("UpdatePasswordDigest() error = %v", err)
	}
	if executor.sql != updatePasswordDigestSQL || len(executor.arguments) != 3 || executor.arguments[1] != "previous" || executor.arguments[2] != "next" {
		t.Fatalf("UpdatePasswordDigest() SQL/args = %q %#v", executor.sql, executor.arguments)
	}

	executor.err = errors.New("driver detail")
	if err := repository.UpdatePasswordDigest(context.Background(), subjectID, previous, next); err == nil || err.Error() != "auth postgres: update password digest failed" {
		t.Fatalf("UpdatePasswordDigest(error) = %v", err)
	}
}

func TestNewAndFindRejectInvalidDependenciesAndStoredData(t *testing.T) {
	t.Parallel()

	if _, err := New(nil); err == nil {
		t.Fatal("New(nil) error = nil")
	}
	identifier, err := credential.NewIdentifier("user@example.com")
	if err != nil {
		t.Fatalf("NewIdentifier() error = %v", err)
	}
	repository, err := New(&executorStub{row: rowStub{values: []any{[]byte{1}, "user@example.com", "digest"}}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	_, _, err = repository.FindByIdentifier(context.Background(), identifier)
	if err == nil || err.Error() != "auth postgres: decode credential failed" {
		t.Fatalf("FindByIdentifier(invalid stored data) error = %v", err)
	}
}

type executorStub struct {
	tag       pgconn.CommandTag
	err       error
	row       pgx.Row
	sql       string
	arguments []any
}

func (stub *executorStub) Exec(_ context.Context, sql string, arguments ...any) (pgconn.CommandTag, error) {
	stub.sql = sql
	stub.arguments = append([]any(nil), arguments...)
	return stub.tag, stub.err
}

func (stub *executorStub) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("unexpected Query call")
}

func (stub *executorStub) QueryRow(_ context.Context, sql string, arguments ...any) pgx.Row {
	stub.sql = sql
	stub.arguments = append([]any(nil), arguments...)
	return stub.row
}

type rowStub struct {
	values []any
	err    error
}

func (row rowStub) Scan(destinations ...any) error {
	if row.err != nil {
		return row.err
	}
	for index, destination := range destinations {
		switch target := destination.(type) {
		case *[]byte:
			*target = append([]byte(nil), row.values[index].([]byte)...)
		case *string:
			*target = row.values[index].(string)
		default:
			return errors.New("unsupported destination")
		}
	}
	return nil
}

func testCredential(t *testing.T, identifierValue string) credential.Credential {
	t.Helper()
	identifier, err := credential.NewIdentifier(identifierValue)
	if err != nil {
		t.Fatalf("NewIdentifier() error = %v", err)
	}
	digest, err := credential.NewPasswordDigest("encoded-digest")
	if err != nil {
		t.Fatalf("NewPasswordDigest() error = %v", err)
	}
	var subjectID credential.SubjectID
	subjectID[0] = 1
	return credential.New(subjectID, identifier, digest)
}
