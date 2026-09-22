// Package postgresavatars atomically updates avatar CAS and a durable retirement queue.
package postgresavatars

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
	platformpostgres "github.com/v0hmly/marketmesh/platform/postgres"
	"github.com/v0hmly/marketmesh/services/user/internal/domain/avatar"
	"github.com/v0hmly/marketmesh/services/user/internal/domain/profile"
)

type Transactor interface {
	WithinTransaction(context.Context, platformpostgres.TransactionOptions, platformpostgres.TransactionFunc) error
}
type Repository struct {
	rw platformpostgres.Executor
	tx Transactor
}

func New(rw platformpostgres.Executor, tx Transactor) (*Repository, error) {
	if rw == nil || tx == nil {
		return nil, errors.New("avatar postgres: primary and transactor required")
	}
	return &Repository{rw: rw, tx: tx}, nil
}

const readSQL = `SELECT subject_id,avatar_version,avatar_file_id FROM users.profiles WHERE subject_id=$1`

func (r *Repository) Get(ctx context.Context, owner profile.SubjectID) (avatar.Avatar, error) {
	if err := valid(ctx, owner); err != nil {
		return avatar.Avatar{}, err
	}
	return decode(r.rw.QueryRow(ctx, readSQL, owner.Bytes()))
}
func (r *Repository) Set(ctx context.Context, owner profile.SubjectID, id avatar.FileID, expected uint64) (avatar.Avatar, error) {
	if err := valid(ctx, owner); err != nil {
		return avatar.Avatar{}, err
	}
	if !avatar.ValidExpectedVersion(expected) {
		return avatar.Avatar{}, avatar.ErrInvalid
	}
	var result avatar.Avatar
	err := r.tx.WithinTransaction(ctx, platformpostgres.TransactionOptions{Isolation: platformpostgres.IsolationReadCommitted}, func(ctx context.Context, e platformpostgres.Executor) error {
		current, err := decode(e.QueryRow(ctx, readSQL+` FOR UPDATE`, owner.Bytes()))
		if err != nil {
			return err
		}
		if current.Version != expected {
			return avatar.ErrConflict
		}
		var raw []byte
		if id != (avatar.FileID{}) {
			var retired bool
			if err = e.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM users.avatar_retirements WHERE subject_id=$1 AND file_id=$2)`, owner.Bytes(), id[:]).Scan(&retired); err != nil {
				return err
			}
			// Retired IDs remain reserved after cleanup. This prevents reattachment
			// racing an in-flight or ambiguously acknowledged Files deletion.
			if retired {
				return avatar.ErrUnavailable
			}
			raw = id[:]
		}
		if current.FileID != (avatar.FileID{}) && current.FileID != id {
			if _, err = e.Exec(ctx, `INSERT INTO users.avatar_retirements(subject_id,file_id) VALUES($1,$2) ON CONFLICT(subject_id,file_id) DO NOTHING`, owner.Bytes(), current.FileID[:]); err != nil {
				return err
			}
		}
		result, err = decode(e.QueryRow(ctx, `UPDATE users.profiles SET avatar_file_id=$2,avatar_version=avatar_version+1 WHERE subject_id=$1 RETURNING subject_id,avatar_version,avatar_file_id`, owner.Bytes(), raw))
		return err
	})
	if err != nil {
		return avatar.Avatar{}, safeError{err}
	}
	return result, nil
}
func decode(row pgx.Row) (avatar.Avatar, error) {
	var result avatar.Avatar
	var owner, id []byte
	var version int64
	err := row.Scan(&owner, &version, &id)
	if errors.Is(err, pgx.ErrNoRows) {
		return result, profile.ErrNotReady
	}
	if err != nil {
		return result, safeError{err}
	}
	result.SubjectID, err = profile.NewSubjectID(owner)
	if err != nil || version < 1 {
		return avatar.Avatar{}, safeError{avatar.ErrInvalid}
	}
	result.Version = uint64(version)
	if id != nil {
		result.FileID, err = avatar.ParseFileID(id)
		if err != nil {
			return avatar.Avatar{}, safeError{err}
		}
	}
	return result, nil
}
func valid(ctx context.Context, owner profile.SubjectID) error {
	if ctx == nil || owner == (profile.SubjectID{}) {
		return avatar.ErrInvalid
	}
	return ctx.Err()
}

type safeError struct{ cause error }

func (safeError) Error() string   { return "avatar postgres: operation failed" }
func (e safeError) Unwrap() error { return e.cause }
