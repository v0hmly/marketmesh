package session

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"errors"
	"io"
	"time"

	"github.com/v0hmly/marketmesh/services/auth/internal/domain/credential"
	domain "github.com/v0hmly/marketmesh/services/auth/internal/domain/session"
)

// Config bounds every session lifetime. Clock and Random are injectable for tests.
type Config struct {
	AccessTTL, IdleTTL, AbsoluteTTL, AssertionTTL time.Duration
	Clock                                         func() time.Time
	Random                                        io.Reader
}

type Service struct {
	store  Store
	access AccessStore
	issuer AssertionIssuer
	config Config
}

func New(store Store, access AccessStore, issuer AssertionIssuer, config Config) (*Service, error) {
	if store == nil || access == nil || issuer == nil {
		return nil, errors.New("session: dependencies are required")
	}
	if config.Clock == nil {
		config.Clock = time.Now
	}
	if config.Random == nil {
		config.Random = rand.Reader
	}
	for _, ttl := range []time.Duration{config.AccessTTL, config.IdleTTL, config.AbsoluteTTL, config.AssertionTTL} {
		if ttl < time.Second || ttl%time.Second != 0 {
			return nil, errors.New("session: lifetimes must be positive whole seconds")
		}
	}
	if config.AssertionTTL > 5*time.Minute || config.AssertionTTL > config.AccessTTL || config.AccessTTL > config.IdleTTL || config.IdleTTL > config.AbsoluteTTL || config.AbsoluteTTL > 90*24*time.Hour {
		return nil, errors.New("session: inconsistent or excessive lifetimes")
	}
	return &Service{store: store, access: access, issuer: issuer, config: config}, nil
}

// Tokens is returned only to the trusted transport adapter for secure cookies.
type Tokens struct {
	Record          domain.Record
	Access, Refresh domain.Token
}

func (Tokens) String() string          { return "session.Tokens{[REDACTED]}" }
func (tokens Tokens) GoString() string { return tokens.String() }

func (service *Service) Start(ctx context.Context, subject credential.SubjectID) (Tokens, error) {
	if ctx == nil || subject == (credential.SubjectID{}) {
		return Tokens{}, domain.ErrInvalidSession
	}
	var id domain.ID
	if _, err := io.ReadFull(service.config.Random, id[:]); err != nil || id == (domain.ID{}) {
		return Tokens{}, domain.ErrUnavailable
	}
	now := service.now()
	record := domain.Record{ID: id, SubjectID: subject, Version: 1, CreatedAt: now, AccessExpiresAt: now.Add(service.config.AccessTTL), RefreshExpiresAt: now.Add(service.config.IdleTTL), ExpiresAt: now.Add(service.config.AbsoluteTTL)}
	tokens, err := service.mint(record)
	if err != nil {
		return Tokens{}, err
	}
	if err := service.store.Create(ctx, record, tokens.Refresh.Digest()); err != nil {
		return Tokens{}, safeError(err)
	}
	if err := service.access.Put(ctx, record, tokens.Access.Digest(), now); err != nil {
		// Creation without an acknowledged access write never exposes credentials.
		// Even if cleanup fails, absent Redis state remains fail-closed.
		_ = service.store.Revoke(ctx, id, service.now(), "issuance_failed")
		return Tokens{}, domain.ErrUnavailable
	}
	return tokens, nil
}

func (service *Service) Refresh(ctx context.Context, value string) (Tokens, error) {
	if ctx == nil {
		return Tokens{}, domain.ErrInvalidSession
	}
	previous, err := domain.ParseToken(value)
	if err != nil {
		return Tokens{}, domain.ErrInvalidSession
	}
	tokens, err := service.mint(domain.Record{ID: previous.ID()})
	if err != nil {
		return Tokens{}, err
	}
	now := service.now()
	record, err := service.store.Rotate(ctx, previous.ID(), previous.Digest(), tokens.Refresh.Digest(), now, service.config.AccessTTL, service.config.IdleTTL)
	if err != nil {
		return Tokens{}, safeError(err)
	}
	if !record.Active(now) || !now.Before(record.AccessExpiresAt) {
		return Tokens{}, domain.ErrInvalidSession
	}
	tokens.Record = record
	if err := service.access.Put(ctx, record, tokens.Access.Digest(), now); err != nil {
		// Do not retry an uncertain refresh: the old token was consumed durably.
		return Tokens{}, domain.ErrUnavailable
	}
	return tokens, nil
}

// Authenticate consumes the external access token only inside Auth.
func (service *Service) Authenticate(ctx context.Context, value string) (domain.Record, error) {
	if ctx == nil {
		return domain.Record{}, domain.ErrInvalidSession
	}
	token, err := domain.ParseToken(value)
	if err != nil {
		return domain.Record{}, domain.ErrInvalidSession
	}
	record, access, err := service.active(ctx, token.ID())
	if err != nil {
		return domain.Record{}, err
	}
	digest := token.Digest()
	if subtle.ConstantTimeCompare(digest[:], access.Digest[:]) != 1 {
		return domain.Record{}, domain.ErrInvalidSession
	}
	return record, nil
}

func (service *Service) Revoke(ctx context.Context, value string) error {
	record, err := service.Authenticate(ctx, value)
	if err != nil {
		return err
	}
	return safeError(service.store.Revoke(ctx, record.ID, service.now(), "logout"))
}

func (service *Service) RevokeAll(ctx context.Context, value string) error {
	record, err := service.Authenticate(ctx, value)
	if err != nil {
		return err
	}
	return safeError(service.store.RevokeAll(ctx, record.SubjectID, service.now(), "logout_all"))
}

// Exchange returns an internal, audience-bound assertion; this method is never public HTTP.
func (service *Service) Exchange(ctx context.Context, value, audience string) (string, time.Time, error) {
	record, err := service.Authenticate(ctx, value)
	if err != nil {
		return "", time.Time{}, err
	}
	now := service.now()
	expiry := now.Add(service.config.AssertionTTL)
	if record.AccessExpiresAt.Before(expiry) {
		expiry = record.AccessExpiresAt
	}
	if record.ExpiresAt.Before(expiry) {
		expiry = record.ExpiresAt
	}
	ttl := expiry.Sub(now).Truncate(time.Second)
	if ttl < time.Second {
		return "", time.Time{}, domain.ErrInvalidSession
	}
	token, err := service.issuer.Issue(ctx, record, audience, now.Add(ttl))
	if err != nil {
		return "", time.Time{}, safeError(err)
	}
	return token, now.Add(ttl), nil
}

// Check verifies canonical state for an already signature-validated assertion.
// It intentionally does not accept an external bearer token from domain services.
func (service *Service) Check(ctx context.Context, id domain.ID, subject credential.SubjectID, authTime time.Time) error {
	if ctx == nil {
		return domain.ErrInvalidSession
	}
	record, _, err := service.active(ctx, id)
	if err != nil {
		return err
	}
	if record.SubjectID != subject || !record.CreatedAt.Equal(authTime) {
		return domain.ErrInvalidSession
	}
	return nil
}

func (service *Service) active(ctx context.Context, id domain.ID) (domain.Record, domain.Access, error) {
	record, err := service.store.Find(ctx, id)
	if err != nil {
		return domain.Record{}, domain.Access{}, safeError(err)
	}
	access, err := service.access.Get(ctx, id)
	if err != nil {
		return domain.Record{}, domain.Access{}, safeError(err)
	}
	now := service.now()
	if !record.Active(now) || !now.Before(record.AccessExpiresAt) || !now.Before(access.ExpiresAt) || access.Version != record.Version || !access.ExpiresAt.Equal(record.AccessExpiresAt) {
		return domain.Record{}, domain.Access{}, domain.ErrInvalidSession
	}
	return record, access, nil
}

func (service *Service) mint(record domain.Record) (Tokens, error) {
	var secrets [64]byte
	defer clear(secrets[:])
	if _, err := io.ReadFull(service.config.Random, secrets[:]); err != nil {
		return Tokens{}, domain.ErrUnavailable
	}
	access, err := domain.NewToken(record.ID, secrets[:32])
	if err != nil {
		return Tokens{}, domain.ErrUnavailable
	}
	refresh, err := domain.NewToken(record.ID, secrets[32:])
	if err != nil || access.Digest() == refresh.Digest() {
		return Tokens{}, domain.ErrUnavailable
	}
	return Tokens{Record: record, Access: access, Refresh: refresh}, nil
}
func (service *Service) now() time.Time { return service.config.Clock().UTC().Truncate(time.Second) }
func safeError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, domain.ErrRefreshReuse):
		return domain.ErrRefreshReuse
	case errors.Is(err, domain.ErrInvalidSession):
		return domain.ErrInvalidSession
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, context.DeadlineExceeded):
		return context.DeadlineExceeded
	default:
		return domain.ErrUnavailable
	}
}
