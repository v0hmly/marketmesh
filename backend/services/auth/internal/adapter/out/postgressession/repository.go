// Package postgressession хранит каноническое состояние сессий в PostgreSQL RW.
package postgressession

import (
	"context"
	"crypto/subtle"
	"errors"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	pg "github.com/v0hmly/marketmesh/platform/postgres"
	application "github.com/v0hmly/marketmesh/services/auth/internal/application/session"
	"github.com/v0hmly/marketmesh/services/auth/internal/domain/credential"
	domain "github.com/v0hmly/marketmesh/services/auth/internal/domain/session"
)

// Database ограничивает адаптер RW-пулом и транзакциями платформы.
type Database interface {
	RW() pg.Executor
	WithinTransaction(context.Context, pg.TransactionOptions, pg.TransactionFunc) error
}

type Repository struct{ database Database }

func New(database Database) (*Repository, error) {
	if database == nil {
		return nil, domain.ErrUnavailable
	}
	return &Repository{database: database}, nil
}

const columns = `session_id, subject_id, version, created_at, access_expires_at, refresh_expires_at, expires_at, revoked_at`

func (r *Repository) Create(ctx context.Context, record domain.Record, digest domain.Digest) error {
	if !validRecord(record) || record.RevokedAt != nil || record.Version != 1 {
		return domain.ErrInvalidSession
	}
	_, err := r.database.RW().Exec(ctx, `INSERT INTO auth.sessions (`+columns+`,refresh_digest) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, record.ID.Bytes(), record.SubjectID.Bytes(), record.Version, record.CreatedAt, record.AccessExpiresAt, record.RefreshExpiresAt, record.ExpiresAt, record.RevokedAt, digest[:])
	return unavailable(err)
}
func (r *Repository) Find(ctx context.Context, id domain.ID) (domain.Record, error) {
	record, err := scanRecord(r.database.RW().QueryRow(ctx, `SELECT `+columns+` FROM auth.sessions WHERE session_id=$1`, id.Bytes()))
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.Record{}, domain.ErrInvalidSession
	}
	if err != nil {
		return domain.Record{}, domain.ErrUnavailable
	}
	return record, nil
}
func (r *Repository) Rotate(ctx context.Context, id domain.ID, previous, next domain.Digest, now time.Time, accessTTL, idleTTL time.Duration) (domain.Record, error) {
	if id == (domain.ID{}) || accessTTL <= 0 || idleTTL <= 0 || previous == next {
		return domain.Record{}, domain.ErrInvalidSession
	}
	var result domain.Record
	var outcome error
	err := r.database.WithinTransaction(ctx, pg.TransactionOptions{}, func(ctx context.Context, executor pg.Executor) error {
		record, err := scanRecord(executor.QueryRow(ctx, `SELECT `+columns+` FROM auth.sessions WHERE session_id=$1 FOR UPDATE`, id.Bytes()))
		if errors.Is(err, pgx.ErrNoRows) {
			outcome = domain.ErrInvalidSession
			return nil
		}
		if err != nil {
			return err
		}
		if !record.Active(now) {
			outcome = domain.ErrInvalidSession
			return nil
		}
		var current []byte
		if err := executor.QueryRow(ctx, `SELECT refresh_digest FROM auth.sessions WHERE session_id=$1`, id.Bytes()).Scan(&current); err != nil {
			return err
		}
		if subtle.ConstantTimeCompare(current, previous[:]) != 1 {
			var consumed bool
			if err := executor.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM auth.consumed_refresh_digests WHERE session_id=$1 AND digest=$2)`, id.Bytes(), previous[:]).Scan(&consumed); err != nil {
				return err
			}
			if consumed {
				if err := revoke(ctx, executor, id, now, "refresh_reuse"); err != nil {
					return err
				}
				outcome = domain.ErrRefreshReuse
			} else {
				outcome = domain.ErrInvalidSession
			}
			return nil
		}
		if !now.Before(record.RefreshExpiresAt) || record.Version == math.MaxInt64 {
			outcome = domain.ErrInvalidSession
			return nil
		}
		// Не допускаем случайного повторного выпуска уже использованного digest.
		var reusedNext bool
		if err := executor.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM auth.consumed_refresh_digests WHERE session_id=$1 AND digest=$2)`, id.Bytes(), next[:]).Scan(&reusedNext); err != nil {
			return err
		}
		if reusedNext {
			outcome = domain.ErrInvalidSession
			return nil
		}
		if _, err := executor.Exec(ctx, `INSERT INTO auth.consumed_refresh_digests (session_id,digest,consumed_at) VALUES ($1,$2,$3)`, id.Bytes(), previous[:], now); err != nil {
			return err
		}
		record.Version++
		record.AccessExpiresAt = minTime(now.Add(accessTTL), record.ExpiresAt)
		record.RefreshExpiresAt = minTime(now.Add(idleTTL), record.ExpiresAt)
		if _, err := executor.Exec(ctx, `UPDATE auth.sessions SET version=$2,refresh_digest=$3,access_expires_at=$4,refresh_expires_at=$5 WHERE session_id=$1`, id.Bytes(), record.Version, next[:], record.AccessExpiresAt, record.RefreshExpiresAt); err != nil {
			return err
		}
		result = record
		return nil
	})
	if err != nil {
		return domain.Record{}, domain.ErrUnavailable
	}
	return result, outcome
}
func (r *Repository) Revoke(ctx context.Context, id domain.ID, now time.Time, reason string) error {
	if !validReason(reason) {
		return domain.ErrInvalidSession
	}
	return unavailable(r.database.WithinTransaction(ctx, pg.TransactionOptions{}, func(ctx context.Context, executor pg.Executor) error { return revoke(ctx, executor, id, now, reason) }))
}
func (r *Repository) RevokeAll(ctx context.Context, subject credential.SubjectID, now time.Time, reason string) error {
	if !validReason(reason) || subject == (credential.SubjectID{}) {
		return domain.ErrInvalidSession
	}
	return unavailable(r.database.WithinTransaction(ctx, pg.TransactionOptions{}, func(ctx context.Context, executor pg.Executor) error {
		_, err := executor.Exec(ctx, `WITH revoked AS (UPDATE auth.sessions SET revoked_at=$2 WHERE subject_id=$1 AND revoked_at IS NULL RETURNING session_id,subject_id,version) INSERT INTO auth.session_revocation_outbox(session_id,subject_id,version,occurred_at,reason) SELECT session_id,subject_id,version,$2,$3 FROM revoked`, subject.Bytes(), now, reason)
		return err
	}))
}
func revoke(ctx context.Context, executor pg.Executor, id domain.ID, now time.Time, reason string) error {
	_, err := executor.Exec(ctx, `WITH revoked AS (UPDATE auth.sessions SET revoked_at=$2 WHERE session_id=$1 AND revoked_at IS NULL RETURNING session_id,subject_id,version) INSERT INTO auth.session_revocation_outbox(session_id,subject_id,version,occurred_at,reason) SELECT session_id,subject_id,version,$2,$3 FROM revoked`, id.Bytes(), now, reason)
	return err
}
func scanRecord(row pgx.Row) (domain.Record, error) {
	var record domain.Record
	var id, subject []byte
	if err := row.Scan(&id, &subject, &record.Version, &record.CreatedAt, &record.AccessExpiresAt, &record.RefreshExpiresAt, &record.ExpiresAt, &record.RevokedAt); err != nil {
		return record, err
	}
	if len(id) != 16 || len(subject) != 16 {
		return domain.Record{}, domain.ErrUnavailable
	}
	copy(record.ID[:], id)
	copy(record.SubjectID[:], subject)
	if !validRecord(record) {
		return domain.Record{}, domain.ErrUnavailable
	}
	return record, nil
}
func validRecord(record domain.Record) bool {
	return record.ID != (domain.ID{}) && record.SubjectID != (credential.SubjectID{}) && record.Version > 0 && !record.CreatedAt.IsZero() && record.CreatedAt.Before(record.AccessExpiresAt) && record.CreatedAt.Before(record.RefreshExpiresAt) && !record.AccessExpiresAt.After(record.ExpiresAt) && !record.RefreshExpiresAt.After(record.ExpiresAt)
}
func validReason(reason string) bool {
	switch reason {
	case "logout", "logout_all", "refresh_reuse", "credential_change", "administrative", "issuance_failed":
		return true
	}
	return false
}
func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
func unavailable(err error) error {
	if err != nil {
		return domain.ErrUnavailable
	}
	return nil
}

var _ application.Store = (*Repository)(nil)
