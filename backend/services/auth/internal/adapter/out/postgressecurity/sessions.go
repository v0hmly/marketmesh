package postgressecurity

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	domain "github.com/v0hmly/marketmesh/services/auth/internal/domain/security"
	"github.com/v0hmly/marketmesh/services/auth/internal/domain/session"
)

func (u *unit) CheckSession(ctx context.Context, record session.Record, now time.Time) error {
	if u.account == nil || u.account.Subject != record.SubjectID || u.account.DeletionAt != nil || u.account.DeletedAt != nil {
		return domain.InvalidCredentials
	}
	var version int64
	err := u.executor.QueryRow(ctx, `SELECT version FROM auth.sessions WHERE session_id=$1 AND subject_id=$2 AND revoked_at IS NULL AND access_expires_at>$3 AND expires_at>$3 FOR UPDATE`, record.ID.Bytes(), record.SubjectID.Bytes(), now).Scan(&version)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && version != record.Version {
		return domain.InvalidCredentials
	}
	return err
}

func (u *unit) CreateSession(ctx context.Context, r session.Record, digest session.Digest) error {
	if u.account == nil || r.SubjectID != u.account.Subject || u.account.DeletionAt != nil || u.account.DeletedAt != nil || !u.account.Verified {
		return domain.InvalidCredentials
	}
	_, err := u.executor.Exec(ctx, `INSERT INTO auth.sessions(session_id,subject_id,version,refresh_digest,created_at,access_expires_at,refresh_expires_at,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, r.ID.Bytes(), r.SubjectID.Bytes(), r.Version, digest[:], r.CreatedAt, r.AccessExpiresAt, r.RefreshExpiresAt, r.ExpiresAt)
	return err
}
func (u *unit) RevokeSessions(ctx context.Context, now time.Time) error {
	if u.account == nil {
		return domain.InvalidCredentials
	}
	_, err := u.executor.Exec(ctx, `WITH revoked AS (UPDATE auth.sessions SET revoked_at=$2 WHERE subject_id=$1 AND revoked_at IS NULL RETURNING session_id,subject_id,version) INSERT INTO auth.session_revocation_outbox(session_id,subject_id,version,occurred_at,reason) SELECT session_id,subject_id,version,$2,'credential_change' FROM revoked`, u.account.Subject.Bytes(), now)
	return err
}
func (u *unit) RevokeSession(ctx context.Context, id session.ID, now time.Time) error {
	if u.account == nil {
		return domain.InvalidCredentials
	}
	_, err := u.executor.Exec(ctx, `WITH revoked AS (UPDATE auth.sessions SET revoked_at=$3 WHERE session_id=$1 AND subject_id=$2 AND revoked_at IS NULL RETURNING session_id,subject_id,version) INSERT INTO auth.session_revocation_outbox(session_id,subject_id,version,occurred_at,reason) SELECT session_id,subject_id,version,$3,'administrative' FROM revoked`, id.Bytes(), u.account.Subject.Bytes(), now)
	return err
}
func (u *unit) Sessions(ctx context.Context, now time.Time) ([]session.Record, error) {
	if u.account == nil {
		return nil, domain.InvalidCredentials
	}
	rows, err := u.executor.Query(ctx, `SELECT session_id,version,created_at,access_expires_at,refresh_expires_at,expires_at FROM auth.sessions WHERE subject_id=$1 AND revoked_at IS NULL AND expires_at>$2 AND refresh_expires_at>$2 ORDER BY created_at DESC LIMIT 100`, u.account.Subject.Bytes(), now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]session.Record, 0)
	for rows.Next() {
		var record session.Record
		var id []byte
		if err := rows.Scan(&id, &record.Version, &record.CreatedAt, &record.AccessExpiresAt, &record.RefreshExpiresAt, &record.ExpiresAt); err != nil {
			return nil, err
		}
		if len(id) != 16 {
			return nil, domain.Unavailable
		}
		copy(record.ID[:], id)
		record.SubjectID = u.account.Subject
		result = append(result, record)
	}
	return result, rows.Err()
}
