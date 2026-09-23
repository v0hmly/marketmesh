package postgresavatars

import (
	"context"
	"crypto/rand"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/v0hmly/marketmesh/services/user/internal/application/avatars"
	"github.com/v0hmly/marketmesh/services/user/internal/domain/avatar"
	"github.com/v0hmly/marketmesh/services/user/internal/domain/profile"
)

const claimSQL = `WITH candidate AS (
 SELECT subject_id,file_id FROM users.avatar_retirements
 WHERE retired_at IS NULL AND next_attempt_at<=clock_timestamp()
 AND (lease_until IS NULL OR lease_until<=clock_timestamp())
 ORDER BY next_attempt_at,created_at,subject_id,file_id FOR UPDATE SKIP LOCKED LIMIT 1
)
UPDATE users.avatar_retirements AS q
SET lease_token=$2,lease_until=clock_timestamp()+$1::bigint*interval '1 microsecond',attempts=LEAST(q.attempts,2147483646)+1
FROM candidate WHERE q.subject_id=candidate.subject_id AND q.file_id=candidate.file_id
RETURNING q.subject_id,q.file_id,q.lease_token,q.attempts`

func (r *Repository) Claim(ctx context.Context, lease time.Duration) (avatars.Retirement, bool, error) {
	if ctx == nil || lease < time.Millisecond || lease > time.Hour {
		return avatars.Retirement{}, false, avatar.ErrInvalid
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return avatars.Retirement{}, false, safeError{err}
	}
	var result avatars.Retirement
	var owner, id, claimed []byte
	err := r.rw.QueryRow(ctx, claimSQL, lease.Microseconds(), token[:]).Scan(&owner, &id, &claimed, &result.Attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, false, nil
	}
	if err != nil {
		return result, false, safeError{err}
	}
	result.SubjectID, err = profile.NewSubjectID(owner)
	if err != nil || len(claimed) != 16 || result.Attempts < 1 {
		return avatars.Retirement{}, false, safeError{avatar.ErrInvalid}
	}
	result.FileID, err = avatar.ParseFileID(id)
	copy(result.LeaseToken[:], claimed)
	if err != nil || result.LeaseToken != token || token == ([16]byte{}) {
		return avatars.Retirement{}, false, safeError{avatar.ErrInvalid}
	}
	return result, true, nil
}
func (r *Repository) Finish(ctx context.Context, record avatars.Retirement) (bool, error) {
	if err := validRecord(ctx, record); err != nil {
		return false, err
	}
	// Preserve the reservation: a retired file can never become an avatar again.
	tag, err := r.rw.Exec(ctx, `UPDATE users.avatar_retirements SET retired_at=clock_timestamp(),lease_token=NULL,lease_until=NULL WHERE subject_id=$1 AND file_id=$2 AND lease_token=$3 AND lease_until>clock_timestamp() AND retired_at IS NULL`, record.SubjectID.Bytes(), record.FileID[:], record.LeaseToken[:])
	if err != nil {
		return false, safeError{err}
	}
	return tag.RowsAffected() == 1, nil
}
func (r *Repository) Retry(ctx context.Context, record avatars.Retirement, delay time.Duration) (bool, error) {
	if err := validRecord(ctx, record); err != nil {
		return false, err
	}
	if delay < time.Millisecond || delay > 24*time.Hour {
		return false, avatar.ErrInvalid
	}
	tag, err := r.rw.Exec(ctx, `UPDATE users.avatar_retirements SET next_attempt_at=clock_timestamp()+$4::bigint*interval '1 microsecond',lease_token=NULL,lease_until=NULL WHERE subject_id=$1 AND file_id=$2 AND lease_token=$3 AND lease_until>clock_timestamp() AND retired_at IS NULL`, record.SubjectID.Bytes(), record.FileID[:], record.LeaseToken[:], delay.Microseconds())
	if err != nil {
		return false, safeError{err}
	}
	return tag.RowsAffected() == 1, nil
}
func validRecord(ctx context.Context, r avatars.Retirement) error {
	if err := valid(ctx, r.SubjectID); err != nil {
		return err
	}
	if r.FileID == (avatar.FileID{}) || r.LeaseToken == ([16]byte{}) {
		return avatar.ErrInvalid
	}
	return nil
}
