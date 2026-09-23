// Package postgres persists profiles exclusively in User's PostgreSQL database.
package postgres

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	platformpostgres "github.com/v0hmly/marketmesh/platform/postgres"
	"github.com/v0hmly/marketmesh/services/user/internal/domain/profile"
)

const profileColumns = `subject_id, display_name, bio, version, created_at, updated_at, last_name, COALESCE(to_char(birth_date, 'YYYY-MM-DD'), ''), gender, phone, city, show_age`
const getSQL = `SELECT ` + profileColumns + ` FROM users.profiles WHERE subject_id=$1`
const updateSQL = `UPDATE users.profiles SET display_name=$2, bio=$3, last_name=$5, birth_date=NULLIF($6,'')::date, gender=$7, phone=$8, city=$9, show_age=$10, version=version+1, updated_at=clock_timestamp() WHERE subject_id=$1 AND version=$4 RETURNING ` + profileColumns

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
	valid, err := profile.NormalizeFields(fields, time.Now())
	if err != nil || valid != fields || id == (profile.SubjectID{}) || expected == 0 || expected >= math.MaxInt64 {
		return profile.Profile{}, profile.ErrInvalidProfile
	}
	result, err := decode(r.rw.QueryRow(ctx, updateSQL, id.Bytes(), fields.DisplayName, fields.Bio, int64(expected), fields.LastName, fields.BirthDate, int32(fields.Gender), fields.Phone, fields.City, fields.ShowAge))
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
	var gender int32
	err := row.Scan(&raw, &result.Fields.DisplayName, &result.Fields.Bio, &version, &result.CreatedAt, &result.UpdatedAt, &result.Fields.LastName, &result.Fields.BirthDate, &gender, &result.Fields.Phone, &result.Fields.City, &result.Fields.ShowAge)
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
	result.Fields.Gender = profile.Gender(gender)
	valid, err := profile.NormalizeFields(result.Fields, time.Now())
	if err != nil || valid != result.Fields || version < 1 {
		return profile.Profile{}, safeError{errors.New("stored profile is invalid")}
	}
	result.Version = uint64(version)
	return result, nil
}

type safeError struct{ cause error }

func (safeError) Error() string   { return "user postgres: profile operation failed" }
func (e safeError) Unwrap() error { return e.cause }
