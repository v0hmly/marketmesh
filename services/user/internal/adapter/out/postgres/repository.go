// Package postgres persists profiles exclusively in User's PostgreSQL database.
package postgres

import (
	"context"
	"errors"
	"math"

	"github.com/jackc/pgx/v5"
	platformpostgres "github.com/v0hmly/marketmesh/platform/postgres"
	"github.com/v0hmly/marketmesh/services/user/internal/domain/profile"
)

const getSQL = `SELECT subject_id, display_name, bio, version, created_at, updated_at FROM users.profiles WHERE subject_id=$1`
const updateSQL = `UPDATE users.profiles SET display_name=$2, bio=$3, version=version+1, updated_at=clock_timestamp() WHERE subject_id=$1 AND version=$4 RETURNING subject_id, display_name, bio, version, created_at, updated_at`

type Repository struct{ rw platformpostgres.Executor }

// New requires the RW executor so immediate reads observe completed updates.
func New(rw platformpostgres.Executor) (*Repository, error) {
	if rw == nil {
		return nil, errors.New("user postgres: RW executor required")
	}
	return &Repository{rw: rw}, nil
}
func (r *Repository) Get(ctx context.Context, id profile.SubjectID) (profile.Profile, error) {
	if ctx == nil {
		return profile.Profile{}, errors.New("user postgres: context must not be nil")
	}
	if err := ctx.Err(); err != nil {
		return profile.Profile{}, err
	}
	if id == (profile.SubjectID{}) {
		return profile.Profile{}, profile.ErrInvalidProfile
	}
	return decode(r.rw.QueryRow(ctx, getSQL, id.Bytes()))
}
func (r *Repository) Update(ctx context.Context, id profile.SubjectID, fields profile.Fields, expected uint64) (profile.Profile, error) {
	if ctx == nil {
		return profile.Profile{}, errors.New("user postgres: context must not be nil")
	}
	if err := ctx.Err(); err != nil {
		return profile.Profile{}, err
	}
	valid, err := profile.NewFields(fields.DisplayName, fields.Bio)
	if err != nil || valid != fields || id == (profile.SubjectID{}) || expected == 0 || expected >= math.MaxInt64 {
		return profile.Profile{}, profile.ErrInvalidProfile
	}
	result, err := decode(r.rw.QueryRow(ctx, updateSQL, id.Bytes(), fields.DisplayName, fields.Bio, int64(expected)))
	if !errors.Is(err, profile.ErrNotReady) {
		return result, err
	}
	// A failed CAS never inserts a row. The follow-up read distinguishes provisioning
	// lag from a stale version; all reads use the same primary executor.
	_, err = r.Get(ctx, id)
	if err != nil {
		return profile.Profile{}, err
	}
	return profile.Profile{}, profile.ErrConflict
}
func decode(row pgx.Row) (profile.Profile, error) {
	var result profile.Profile
	var raw []byte
	var version int64
	err := row.Scan(&raw, &result.Fields.DisplayName, &result.Fields.Bio, &version, &result.CreatedAt, &result.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return profile.Profile{}, profile.ErrNotReady
	}
	if err != nil {
		return profile.Profile{}, safeError{err}
	}
	result.SubjectID, err = profile.NewSubjectID(raw)
	if err != nil {
		return profile.Profile{}, safeError{errors.New("stored profile is invalid")}
	}
	valid, err := profile.NewFields(result.Fields.DisplayName, result.Fields.Bio)
	if err != nil || valid != result.Fields || version < 1 {
		return profile.Profile{}, safeError{errors.New("stored profile is invalid")}
	}
	result.Version = uint64(version)
	return result, nil
}

type safeError struct{ cause error }

func (safeError) Error() string   { return "user postgres: profile operation failed" }
func (e safeError) Unwrap() error { return e.cause }
