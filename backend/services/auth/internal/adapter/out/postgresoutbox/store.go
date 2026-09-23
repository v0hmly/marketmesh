// Package postgresoutbox owns atomic claims and lease-guarded registration delivery state.
package postgresoutbox

import (
	"context"
	"crypto/rand"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	platformpostgres "github.com/v0hmly/marketmesh/platform/postgres"
	"github.com/v0hmly/marketmesh/services/auth/internal/application/publishregistration"
)

const claimSQL = `WITH candidate AS (
 SELECT event_id FROM auth.registration_outbox
 WHERE published_at IS NULL AND next_attempt_at<=clock_timestamp()
 AND (lease_until IS NULL OR lease_until<=clock_timestamp())
 ORDER BY next_attempt_at,occurred_at,event_id FOR UPDATE SKIP LOCKED LIMIT 1
)
UPDATE auth.registration_outbox AS events
SET lease_token=$2,lease_until=clock_timestamp()+$1::bigint*interval '1 microsecond',
 attempts=LEAST(events.attempts,2147483646)+1
FROM candidate WHERE events.event_id=candidate.event_id
RETURNING events.event_id,events.lease_token,events.payload,events.attempts,events.occurred_at`
const markSQL = `UPDATE auth.registration_outbox SET published_at=clock_timestamp(),lease_token=NULL,lease_until=NULL
WHERE event_id=$1 AND lease_token=$2 AND lease_until>clock_timestamp() AND published_at IS NULL`
const retrySQL = `UPDATE auth.registration_outbox SET next_attempt_at=clock_timestamp()+$3::bigint*interval '1 microsecond',lease_token=NULL,lease_until=NULL
WHERE event_id=$1 AND lease_token=$2 AND lease_until>clock_timestamp() AND published_at IS NULL`
const pendingSQL = `SELECT count(*),min(occurred_at) FROM auth.registration_outbox WHERE published_at IS NULL`

type Store struct{ rw platformpostgres.Executor }

func New(rw platformpostgres.Executor) (*Store, error) {
	if rw == nil {
		return nil, errors.New("registration outbox: RW executor required")
	}
	return &Store{rw: rw}, nil
}
func (s *Store) Claim(ctx context.Context, lease time.Duration) (publishregistration.Record, bool, error) {
	if err := validContext(ctx); err != nil {
		return publishregistration.Record{}, false, err
	}
	if lease < time.Millisecond || lease > time.Hour {
		return publishregistration.Record{}, false, errors.New("registration outbox: invalid lease duration")
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return publishregistration.Record{}, false, safeError{err}
	}
	var rawID, rawLease []byte
	var record publishregistration.Record
	err := s.rw.QueryRow(ctx, claimSQL, lease.Microseconds(), token[:]).Scan(&rawID, &rawLease, &record.Payload, &record.Attempts, &record.OccurredAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return publishregistration.Record{}, false, nil
	}
	if err != nil {
		return publishregistration.Record{}, false, safeError{err}
	}
	if len(rawID) != 16 || len(rawLease) != 16 || len(record.Payload) < 1 || len(record.Payload) > 8192 || record.Attempts < 1 || record.OccurredAt.IsZero() {
		return publishregistration.Record{}, false, errors.New("registration outbox: invalid stored record")
	}
	copy(record.ID[:], rawID)
	copy(record.LeaseToken[:], rawLease)
	if record.ID == ([16]byte{}) || record.LeaseToken != token {
		return publishregistration.Record{}, false, errors.New("registration outbox: invalid stored identity")
	}
	return record, true, nil
}
func (s *Store) MarkPublished(ctx context.Context, r publishregistration.Record) (bool, error) {
	if err := validContext(ctx); err != nil {
		return false, err
	}
	if err := validRecord(r); err != nil {
		return false, err
	}
	tag, err := s.rw.Exec(ctx, markSQL, r.ID[:], r.LeaseToken[:])
	if err != nil {
		return false, safeError{err}
	}
	return tag.RowsAffected() == 1, nil
}
func (s *Store) Retry(ctx context.Context, r publishregistration.Record, delay time.Duration) (bool, error) {
	if err := validContext(ctx); err != nil {
		return false, err
	}
	if err := validRecord(r); err != nil {
		return false, err
	}
	if delay < time.Millisecond || delay > 24*time.Hour {
		return false, errors.New("registration outbox: invalid retry delay")
	}
	tag, err := s.rw.Exec(ctx, retrySQL, r.ID[:], r.LeaseToken[:], delay.Microseconds())
	if err != nil {
		return false, safeError{err}
	}
	return tag.RowsAffected() == 1, nil
}
func (s *Store) Pending(ctx context.Context) (int64, time.Time, error) {
	if err := validContext(ctx); err != nil {
		return 0, time.Time{}, err
	}
	var count int64
	var oldest *time.Time
	if err := s.rw.QueryRow(ctx, pendingSQL).Scan(&count, &oldest); err != nil {
		return 0, time.Time{}, safeError{err}
	}
	if oldest == nil {
		return count, time.Time{}, nil
	}
	return count, *oldest, nil
}
func validRecord(r publishregistration.Record) error {
	if r.ID == ([16]byte{}) || r.LeaseToken == ([16]byte{}) {
		return errors.New("registration outbox: record identity required")
	}
	return nil
}
func validContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("registration outbox: context required")
	}
	return ctx.Err()
}

type safeError struct{ cause error }

func (safeError) Error() string   { return "registration outbox: storage operation failed" }
func (e safeError) Unwrap() error { return e.cause }

var _ publishregistration.Store = (*Store)(nil)
