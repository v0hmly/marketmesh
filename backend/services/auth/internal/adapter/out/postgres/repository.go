// Package postgres persists Auth credentials in PostgreSQL.
package postgres

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	platformpostgres "github.com/v0hmly/marketmesh/platform/postgres"
	"github.com/v0hmly/marketmesh/services/auth/internal/application/login"
	"github.com/v0hmly/marketmesh/services/auth/internal/application/register"
	"github.com/v0hmly/marketmesh/services/auth/internal/domain/credential"
)

const (
	// #nosec G101 -- SQL contains a digest column name; every credential value is a placeholder.
	createCredentialSQL = `
INSERT INTO auth.credentials (subject_id, identifier, password_digest)
VALUES ($1, $2, $3)
ON CONFLICT (identifier) DO NOTHING`
	findCredentialSQL = `
SELECT subject_id, identifier, password_digest
FROM auth.credentials
WHERE identifier = $1`
	// #nosec G101 -- SQL contains a digest column name; every credential value is a placeholder.
	updatePasswordDigestSQL = `
UPDATE auth.credentials
SET password_digest = $3, updated_at = now()
WHERE subject_id = $1 AND password_digest = $2`
)

// Repository adapts a bounded platform PostgreSQL executor to application ports.
type Repository struct {
	executor platformpostgres.Executor
}

// New constructs a credentials repository.
func New(executor platformpostgres.Executor) (*Repository, error) {
	if executor == nil {
		return nil, errors.New("auth postgres: executor must not be nil")
	}
	return &Repository{executor: executor}, nil
}

// Create inserts one credential and atomically loses concurrent duplicate-identifier races.
func (repository *Repository) Create(ctx context.Context, value credential.Credential) (bool, error) {
	tag, err := repository.executor.Exec(
		ctx,
		createCredentialSQL,
		value.SubjectID().Bytes(),
		value.Identifier().String(),
		value.PasswordDigest().String(),
	)
	if err != nil {
		return false, safeError{"create credential", err}
	}

	return tag.RowsAffected() == 1, nil
}

// FindByIdentifier reads current security state from the configured executor.
func (repository *Repository) FindByIdentifier(ctx context.Context, identifier credential.Identifier) (credential.Credential, bool, error) {
	var subjectBytes []byte
	var identifierValue string
	var digestValue string
	err := repository.executor.QueryRow(ctx, findCredentialSQL, identifier.String()).Scan(&subjectBytes, &identifierValue, &digestValue)
	if errors.Is(err, pgx.ErrNoRows) {
		return credential.Credential{}, false, nil
	}
	if err != nil {
		return credential.Credential{}, false, safeError{"find credential", err}
	}
	subjectID, err := credential.NewSubjectID(subjectBytes)
	if err != nil {
		return credential.Credential{}, false, safeError{"decode credential", err}
	}
	storedIdentifier, err := credential.NewIdentifier(identifierValue)
	if err != nil {
		return credential.Credential{}, false, safeError{"decode credential", err}
	}
	digest, err := credential.NewPasswordDigest(digestValue)
	if err != nil {
		return credential.Credential{}, false, safeError{"decode credential", err}
	}

	return credential.New(subjectID, storedIdentifier, digest), true, nil
}

// UpdatePasswordDigest performs a compare-and-swap update after successful verification.
func (repository *Repository) UpdatePasswordDigest(ctx context.Context, subjectID credential.SubjectID, previous, next credential.PasswordDigest) error {
	_, err := repository.executor.Exec(ctx, updatePasswordDigestSQL, subjectID.Bytes(), previous.String(), next.String())
	if err != nil {
		return safeError{"update password digest", err}
	}
	return nil
}

type safeError struct {
	operation string
	cause     error
}

func (err safeError) Error() string {
	return "auth postgres: " + err.operation + " failed"
}

func (err safeError) Unwrap() error {
	return err.cause
}

var (
	_ register.CredentialWriter   = (*Repository)(nil)
	_ login.CredentialReader      = (*Repository)(nil)
	_ login.PasswordDigestUpdater = (*Repository)(nil)
)
