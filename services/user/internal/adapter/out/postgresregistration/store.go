// Package postgresregistration commits the inbox fact and initial profile together.
package postgresregistration

import (
	"context"
	"crypto/sha256"
	"errors"
	"github.com/jackc/pgx/v5"
	platformpostgres "github.com/v0hmly/marketmesh/platform/postgres"
	"github.com/v0hmly/marketmesh/services/user/internal/adapter/out/registrationwire"
	"github.com/v0hmly/marketmesh/services/user/internal/application/consumeregistration"
	"github.com/v0hmly/marketmesh/services/user/internal/domain/registrationevent"
)

type Store struct{ pool platformpostgres.Executor }

func New(pool platformpostgres.Executor) (*Store, error) {
	if pool == nil {
		return nil, errors.New("registration store: executor required")
	}
	return &Store{pool: pool}, nil
}
func (s *Store) Apply(ctx context.Context, e registrationevent.Event) (consumeregistration.Outcome, error) {
	if ctx == nil {
		return "", errors.New("registration store: context required")
	}
	payload, err := registrationwire.Marshal(e)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(payload)
	var inserted bool
	err = s.pool.QueryRow(ctx, `WITH accepted AS (INSERT INTO users.registration_inbox(event_id,subject_id,payload_hash)
 VALUES($1,$2,$3) ON CONFLICT(event_id) DO UPDATE SET event_id=EXCLUDED.event_id
 WHERE users.registration_inbox.subject_id=EXCLUDED.subject_id AND users.registration_inbox.payload_hash=EXCLUDED.payload_hash
 RETURNING (xmax=0) AS inserted), projection AS (INSERT INTO users.profiles(subject_id) SELECT $2 FROM accepted ON CONFLICT(subject_id) DO NOTHING) SELECT inserted FROM accepted`, e.ID[:], e.SubjectID.Bytes(), hash[:]).Scan(&inserted)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", consumeregistration.ErrConflict
	}
	if err != nil {
		return "", err
	}
	if !inserted {
		return consumeregistration.Duplicate, nil
	}
	return consumeregistration.Applied, nil
}
