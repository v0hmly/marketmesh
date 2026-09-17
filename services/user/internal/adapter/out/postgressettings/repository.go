// Package postgressettings stores independent settings versions on User's primary.
package postgressettings

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5"
	platformpostgres "github.com/v0hmly/marketmesh/platform/postgres"
	"github.com/v0hmly/marketmesh/services/user/internal/domain/profile"
	"github.com/v0hmly/marketmesh/services/user/internal/domain/settings"
)

const getSQL = `SELECT subject_id,settings_version,theme FROM users.profiles WHERE subject_id=$1`
const updateSQL = `UPDATE users.profiles SET theme=$2,settings_version=settings_version+1 WHERE subject_id=$1 AND settings_version=$3 RETURNING subject_id,settings_version,theme`

type Repository struct{ rw platformpostgres.Executor }

func New(rw platformpostgres.Executor) (*Repository, error) {
	if rw == nil {
		return nil, errors.New("settings postgres: primary executor required")
	}
	return &Repository{rw}, nil
}

type safeError struct{ cause error }

func (safeError) Error() string   { return "settings postgres: operation failed" }
func (e safeError) Unwrap() error { return e.cause }
func valid(ctx context.Context, id profile.SubjectID) error {
	if ctx == nil {
		return errors.New("settings postgres: context required")
	}
	if e := ctx.Err(); e != nil {
		return e
	}
	if id == (profile.SubjectID{}) {
		return settings.ErrInvalid
	}
	return nil
}
func (r *Repository) Get(ctx context.Context, id profile.SubjectID) (settings.Settings, error) {
	if e := valid(ctx, id); e != nil {
		return settings.Settings{}, e
	}
	return decode(r.rw.QueryRow(ctx, getSQL, id.Bytes()))
}
func (r *Repository) Update(ctx context.Context, id profile.SubjectID, theme settings.Theme, expected uint64) (settings.Settings, error) {
	if e := valid(ctx, id); e != nil {
		return settings.Settings{}, e
	}
	if !theme.Valid() || !settings.ValidExpectedVersion(expected) {
		return settings.Settings{}, settings.ErrInvalid
	}
	s, e := decode(r.rw.QueryRow(ctx, updateSQL, id.Bytes(), string(theme), int64(expected)))
	if !errors.Is(e, profile.ErrNotReady) {
		return s, e
	}
	// Failed CAS never provisions a profile; the primary read distinguishes lag.
	if _, e = r.Get(ctx, id); e != nil {
		return settings.Settings{}, e
	}
	return settings.Settings{}, settings.ErrConflict
}
func decode(row pgx.Row) (settings.Settings, error) {
	var s settings.Settings
	var raw []byte
	var version int64
	var theme string
	e := row.Scan(&raw, &version, &theme)
	if errors.Is(e, pgx.ErrNoRows) {
		return s, profile.ErrNotReady
	}
	if e != nil {
		return s, safeError{e}
	}
	s.SubjectID, e = profile.NewSubjectID(raw)
	s.Version = uint64(version)
	s.Theme = settings.Theme(theme)
	if e != nil || version < 1 || s.Validate() != nil {
		return settings.Settings{}, safeError{errors.New("invalid stored settings")}
	}
	return s, nil
}
