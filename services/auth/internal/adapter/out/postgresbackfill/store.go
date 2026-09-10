// Package postgresbackfill accesses only opaque Auth subject IDs and Auth's outbox.
package postgresbackfill

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/v0hmly/marketmesh/services/auth/internal/adapter/out/registrationwire"
	app "github.com/v0hmly/marketmesh/services/auth/internal/application/backfillregistration"
	"github.com/v0hmly/marketmesh/services/auth/internal/domain/credential"
	"github.com/v0hmly/marketmesh/services/auth/internal/domain/registrationevent"
	"time"
)

type DB interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}
type Store struct{ db DB }

func New(db DB) (*Store, error) {
	if db == nil {
		return nil, errors.New("registration backfill: database required")
	}
	return &Store{db}, nil
}

type safeError struct{ cause error }

func (safeError) Error() string   { return "registration backfill: storage operation failed" }
func (e safeError) Unwrap() error { return e.cause }
func bounded(ctx context.Context) (context.Context, context.CancelFunc, error) {
	if ctx == nil {
		return nil, nil, errors.New("registration backfill: context required")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	c, cancel := context.WithTimeout(ctx, 5*time.Second)
	return c, cancel, nil
}
func (s *Store) Scan(ctx context.Context, after []byte, limit int) ([]app.Candidate, error) {
	if (len(after) != 0 && len(after) != 16) || limit < 1 || limit > 1000 {
		return nil, errors.New("registration backfill: invalid page")
	}
	ctx, cancel, err := bounded(ctx)
	if err != nil {
		return nil, err
	}
	defer cancel()
	rows, err := s.db.Query(ctx, `SELECT c.subject_id,o.event_id IS NULL,o.published_at FROM auth.credentials c LEFT JOIN auth.registration_outbox o USING(subject_id) WHERE c.subject_id>$1 ORDER BY c.subject_id LIMIT $2`, after, limit)
	if err != nil {
		return nil, safeError{err}
	}
	defer rows.Close()
	result := make([]app.Candidate, 0, limit)
	for rows.Next() {
		var c app.Candidate
		var raw []byte
		if err = rows.Scan(&raw, &c.Missing, &c.PublishedAt); err != nil {
			return nil, safeError{err}
		}
		c.Subject, err = credential.NewSubjectID(raw)
		if err != nil {
			return nil, safeError{err}
		}
		result = append(result, c)
	}
	if err = rows.Err(); err != nil {
		return nil, safeError{err}
	}
	return result, nil
}
func (s *Store) Ensure(ctx context.Context, e registrationevent.Event) (bool, error) {
	payload, err := registrationwire.Marshal(e)
	if err != nil {
		return false, err
	}
	ctx, cancel, err := bounded(ctx)
	if err != nil {
		return false, err
	}
	defer cancel()
	tag, err := s.db.Exec(ctx, `INSERT INTO auth.registration_outbox(event_id,subject_id,occurred_at,payload) SELECT $1,subject_id,$3,$4 FROM auth.credentials WHERE subject_id=$2 ON CONFLICT(subject_id) DO NOTHING`, e.ID[:], e.SubjectID.Bytes(), e.OccurredAt, payload)
	if err != nil {
		return false, safeError{err}
	}
	return tag.RowsAffected() == 1, nil
}

// Replay preserves canonical payload and event ID. Snapshot timestamp prevents a
// concurrent reconciler from resetting a delivery completed after its scan.
func (s *Store) Replay(ctx context.Context, subject credential.SubjectID, published time.Time) (bool, error) {
	if subject == (credential.SubjectID{}) || published.IsZero() {
		return false, errors.New("registration backfill: invalid replay")
	}
	ctx, cancel, err := bounded(ctx)
	if err != nil {
		return false, err
	}
	defer cancel()
	tag, err := s.db.Exec(ctx, `UPDATE auth.registration_outbox SET published_at=NULL,next_attempt_at=clock_timestamp() WHERE subject_id=$1 AND published_at=$2 AND lease_token IS NULL AND lease_until IS NULL`, subject.Bytes(), published)
	if err != nil {
		return false, safeError{err}
	}
	return tag.RowsAffected() == 1, nil
}
