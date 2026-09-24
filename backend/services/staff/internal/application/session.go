// Package application owns the staff identity, session and invitation use cases.
// It never accepts a buyer identity or trusts identity-bearing HTTP headers.
package application

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"time"
)

var (
	ErrUnauthenticated = errors.New("staff authentication required")
	ErrInvalidLogin    = errors.New("invalid staff login")
	ErrInvalidInvite   = errors.New("invalid staff invite")
)

// Principal is verified by the configured OIDC provider. Issuer and subject,
// not a mutable email address, identify an existing member.
type Principal struct{ Issuer, Subject, Email, Name string }
type Session struct {
	Principal
	Role      string
	ExpiresAt time.Time
}
type Login struct {
	StateHash, BrowserHash, Nonce, Verifier, Invite string
	ExpiresAt                                       time.Time
}
type Invite struct {
	State, Role, Inviter string
	Principal            Principal
}

// IdentityProvider must validate issuer, audience, signature, expiry and nonce.
type IdentityProvider interface {
	Authorize(state, nonce, verifier string) string
	Exchange(context.Context, string, string, string) (Principal, error)
}

// Store atomically consumes challenges/invites and maintains opaque sessions.
// Stored state/session/browser tokens are digests. The PKCE verifier is secret.
type Store interface {
	SaveLogin(context.Context, Login) error
	ConsumeLogin(context.Context, string, string, time.Time) (Login, error)
	SaveSession(context.Context, string, Principal, time.Time, time.Time) error
	Session(context.Context, string, time.Time, time.Time) (Session, error)
	DeleteSession(context.Context, string) error
	Invite(context.Context, string, Principal, time.Time, bool) (Invite, error)
}
type Service struct {
	store          Store
	provider       IdentityProvider
	idle, lifetime time.Duration
	now            func() time.Time
}

func New(store Store, provider IdentityProvider, idle, lifetime time.Duration) *Service {
	return &Service{store: store, provider: provider, idle: idle, lifetime: lifetime, now: time.Now}
}
func token() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func Digest(value string) string {
	b := sha256.Sum256([]byte(value))
	return base64.RawURLEncoding.EncodeToString(b[:])
}
func ValidToken(value string) bool {
	b, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(b) == 32 && base64.RawURLEncoding.EncodeToString(b) == value
}

func (s *Service) Start(ctx context.Context, invite, previousCookie string) (authorize, browser string, err error) {
	if invite != "" && !ValidToken(invite) {
		return "", "", ErrInvalidInvite
	}
	// SameSite=Strict deliberately withholds the old staff cookie on the IdP
	// callback. Revoke it here, on the same-origin explicit sign-in action.
	if err = s.Logout(ctx, previousCookie); err != nil {
		return "", "", err
	}
	state, browser, nonce, verifier := token(), token(), token(), token()
	err = s.store.SaveLogin(ctx, Login{StateHash: Digest(state), BrowserHash: Digest(browser), Nonce: nonce, Verifier: verifier, Invite: invite, ExpiresAt: s.now().Add(10 * time.Minute)})
	if err != nil {
		return "", "", err
	}
	return s.provider.Authorize(state, nonce, verifier), browser, nil
}
func (s *Service) Complete(ctx context.Context, state, browser, code string) (session, invite string, err error) {
	if !ValidToken(state) || !ValidToken(browser) || code == "" || len(code) > 4096 {
		return "", "", ErrInvalidLogin
	}
	login, err := s.store.ConsumeLogin(ctx, Digest(state), Digest(browser), s.now())
	if err != nil {
		return "", "", err
	}
	principal, err := s.provider.Exchange(ctx, code, login.Verifier, login.Nonce)
	if err != nil {
		return "", "", ErrInvalidLogin
	}
	if principal.Issuer == "" || principal.Subject == "" || principal.Email == "" {
		return "", "", ErrInvalidLogin
	}
	principal.Email = strings.ToLower(principal.Email)
	session = token()
	now := s.now()
	if err = s.store.SaveSession(ctx, Digest(session), principal, now.Add(s.lifetime), now.Add(s.idle)); err != nil {
		return "", "", err
	}
	return session, login.Invite, nil
}
func (s *Service) Session(ctx context.Context, cookie string) (Session, error) {
	if !ValidToken(cookie) {
		return Session{}, ErrUnauthenticated
	}
	now := s.now()
	return s.store.Session(ctx, Digest(cookie), now, now.Add(s.idle))
}
func (s *Service) Logout(ctx context.Context, cookie string) error {
	if !ValidToken(cookie) {
		return nil
	}
	return s.store.DeleteSession(ctx, Digest(cookie))
}
func (s *Service) Invite(ctx context.Context, cookie, invite string, accept bool) (Invite, error) {
	if !ValidToken(invite) {
		return Invite{}, ErrInvalidInvite
	}
	session, err := s.Session(ctx, cookie)
	if err != nil {
		return Invite{}, err
	}
	result, err := s.store.Invite(ctx, Digest(invite), session.Principal, s.now(), accept)
	result.Principal = session.Principal
	return result, err
}
