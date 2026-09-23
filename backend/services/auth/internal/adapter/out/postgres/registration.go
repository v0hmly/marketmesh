package postgres

import (
	"context"
	"errors"
	"github.com/v0hmly/marketmesh/services/auth/internal/adapter/out/registrationwire"
	"github.com/v0hmly/marketmesh/services/auth/internal/application/register"
	"github.com/v0hmly/marketmesh/services/auth/internal/domain/credential"
	"github.com/v0hmly/marketmesh/services/auth/internal/domain/registrationevent"
)

// #nosec G101 -- The digest column is a schema identifier; all values are bound parameters.
const createRegistrationSQL = `WITH inserted AS (
 INSERT INTO auth.credentials(subject_id,identifier,password_digest)
 VALUES($1,$2,$3) ON CONFLICT(identifier) DO NOTHING RETURNING subject_id
) INSERT INTO auth.registration_outbox(event_id,subject_id,occurred_at,payload)
 SELECT $4,subject_id,$5,$6 FROM inserted`

// CreateRegistration commits both records in one PostgreSQL statement or neither.
func (r *Repository) CreateRegistration(ctx context.Context, value credential.Credential, event registrationevent.Event) (bool, error) {
	if ctx == nil {
		return false, errors.New("auth postgres: context required")
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	if event.Validate() != nil || event.SubjectID != value.SubjectID() {
		return false, registrationevent.ErrInvalidEvent
	}
	payload, err := registrationwire.Marshal(event)
	if err != nil {
		return false, safeError{"encode registration event", err}
	}
	tag, err := r.executor.Exec(ctx, createRegistrationSQL, value.SubjectID().Bytes(), value.Identifier().String(), value.PasswordDigest().String(), event.ID[:], event.OccurredAt, payload)
	if err != nil {
		return false, safeError{"create registration", err}
	}
	return tag.RowsAffected() == 1, nil
}

var _ register.RegistrationWriter = (*Repository)(nil)
