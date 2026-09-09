// Package session implements session lifecycle through service-owned ports.
package session

import (
	"context"
	"time"

	"github.com/v0hmly/marketmesh/services/auth/internal/domain/credential"
	domain "github.com/v0hmly/marketmesh/services/auth/internal/domain/session"
)

// Store makes refresh rotation and revocation events atomic in PostgreSQL.
// All authentication reads use RW. Rotate commits reuse revocation before
// returning ErrRefreshReuse; unknown digests must never revoke a session.
type Store interface {
	Create(context.Context, domain.Record, domain.Digest) error
	Find(context.Context, domain.ID) (domain.Record, error)
	Rotate(ctx context.Context, id domain.ID, previous, next domain.Digest, now time.Time, accessTTL, idleTTL time.Duration) (domain.Record, error)
	Revoke(ctx context.Context, id domain.ID, now time.Time, reason string) error
	RevokeAll(ctx context.Context, subject credential.SubjectID, now time.Time, reason string) error
}

// AccessStore keeps access-token digests in Redis with finite TTL. Put must not
// overwrite a newer version. Authority always also requires canonical Store state.
type AccessStore interface {
	Put(ctx context.Context, record domain.Record, digest domain.Digest, now time.Time) error
	Get(context.Context, domain.ID) (domain.Access, error)
}

// AssertionIssuer accepts only server-selected audiences and scopes.
type AssertionIssuer interface {
	Issue(ctx context.Context, record domain.Record, audience string, expiresAt time.Time) (string, error)
}
