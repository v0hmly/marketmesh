// Package security implements browser account security through atomic ports.
package security

import (
	"context"
	"time"

	applicationsession "github.com/v0hmly/marketmesh/services/auth/internal/application/session"
	"github.com/v0hmly/marketmesh/services/auth/internal/domain/credential"
	"github.com/v0hmly/marketmesh/services/auth/internal/domain/registrationevent"
	domain "github.com/v0hmly/marketmesh/services/auth/internal/domain/security"
	"github.com/v0hmly/marketmesh/services/auth/internal/domain/session"
)

// Selector chooses an account by exactly one trusted lookup key.
type Selector struct {
	Email   string
	Subject credential.SubjectID
}

// Mail contains secrets until sealed by the persistence adapter. It is never
// logged, traced, included in errors, or exported as a registration event.
type Mail struct {
	ID                                               domain.ID
	Subject                                          credential.SubjectID
	Kind, Email, OtherEmail, URL, SecondaryURL, Code string
	At, ExpiresAt                                    time.Time
}

func (Mail) String() string     { return "security.Mail{[REDACTED]}" }
func (m Mail) GoString() string { return m.String() }

// Unit is valid only inside Store.WithAccount. Account state and all writes
// commit together. A returned application rejection must use an outcome outside
// the callback when failed-attempt counters need to commit.
type Unit interface {
	Account() *domain.Account
	Challenge(context.Context, domain.ID) (domain.Challenge, error)
	LatestChallenge(context.Context, domain.Purpose) (domain.Challenge, bool, error)
	SaveChallenge(context.Context, domain.Challenge) error
	Queue(context.Context, Mail) error
	ReserveAttempt(context.Context, domain.Digest, time.Time) (int, error)
	ReserveBudget(context.Context, domain.Digest, time.Time, int, time.Duration) (int, error)
	ResetAttempts(context.Context, domain.Digest) error
	CreateSession(context.Context, session.Record, session.Digest) error
	RevokeSessions(context.Context, time.Time) error
	Sessions(context.Context, time.Time) ([]session.Record, error)
	RevokeSession(context.Context, session.ID, time.Time) error
	CheckSession(context.Context, session.Record, time.Time) error
}

type Store interface {
	WithAccount(context.Context, Selector, func(Unit) error) error
	ChallengeSubject(context.Context, domain.ID) (credential.SubjectID, error)
	Register(context.Context, credential.Credential, registrationevent.Event, domain.Challenge, Mail) error
}

type PasswordHasher interface {
	Hash([]byte) (credential.PasswordDigest, error)
	Verify([]byte, credential.PasswordDigest) (bool, bool, error)
	EqualizeMissing([]byte) error
}
type RegistrationEvents interface {
	New(context.Context, credential.SubjectID) (registrationevent.Event, error)
}
type Sessions interface {
	Prepare(credential.SubjectID) (applicationsession.Tokens, error)
	Activate(context.Context, applicationsession.Tokens) (applicationsession.Tokens, error)
}
