package security

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	applicationsession "github.com/v0hmly/marketmesh/services/auth/internal/application/session"
	"github.com/v0hmly/marketmesh/services/auth/internal/domain/credential"
	domain "github.com/v0hmly/marketmesh/services/auth/internal/domain/security"
	"github.com/v0hmly/marketmesh/services/auth/internal/domain/session"
)

// Unexpected calls to the embedded ports fail this focused transition fixture.
type registrationUnit struct {
	Unit
	account   domain.Account
	challenge domain.Challenge
	created   int
	failSave  bool
}

func (u *registrationUnit) Account() *domain.Account { return &u.account }
func (u *registrationUnit) Challenge(context.Context, domain.ID) (domain.Challenge, error) {
	return u.challenge, nil
}
func (u *registrationUnit) SaveChallenge(_ context.Context, c domain.Challenge) error {
	if u.failSave {
		return errors.New("storage failure")
	}
	u.challenge = c
	return nil
}
func (u *registrationUnit) CreateSession(context.Context, session.Record, session.Digest) error {
	u.created++
	return nil
}
func (*registrationUnit) Queue(context.Context, Mail) error { return nil }

type registrationStore struct {
	Store
	state registrationUnit
}

func (s *registrationStore) ChallengeSubject(context.Context, domain.ID) (credential.SubjectID, error) {
	return s.state.account.Subject, nil
}
func (s *registrationStore) WithAccount(_ context.Context, _ Selector, apply func(Unit) error) error {
	next := s.state
	if err := apply(&next); err != nil {
		return err
	}
	s.state = next
	return nil
}

type registrationSessions struct {
	Sessions
	activations int
}

func (*registrationSessions) Prepare(subject credential.SubjectID) (applicationsession.Tokens, error) {
	return applicationsession.Tokens{Record: session.Record{ID: session.ID{9}, SubjectID: subject}}, nil
}
func (s *registrationSessions) Activate(_ context.Context, tokens applicationsession.Tokens) (applicationsession.Tokens, error) {
	s.activations++
	return tokens, nil
}

func TestConfirmRegistrationRequiresBothOneUseLinkAndBrowserProof(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name    string
		browser string
		mutate  func(*registrationUnit)
		want    error
		session bool
	}{
		{name: "both proofs", browser: strings.Repeat("a", 43), session: true},
		{name: "missing browser proof"},
		{name: "foreign browser proof", browser: strings.Repeat("b", 43)},
		{name: "old challenge without binding", browser: strings.Repeat("a", 43), mutate: func(u *registrationUnit) { u.challenge.RegistrationDigest = domain.Digest{} }},
		{name: "used link", browser: strings.Repeat("a", 43), mutate: func(u *registrationUnit) { u.challenge.UsedAt = &u.challenge.SentAt }, want: domain.TokenUsed},
		{name: "expired link", browser: strings.Repeat("a", 43), mutate: func(u *registrationUnit) { u.challenge.ExpiresAt = u.challenge.SentAt }, want: domain.TokenExpired},
		{name: "credential revision changed", browser: strings.Repeat("a", 43), mutate: func(u *registrationUnit) { u.account.Revision++ }, want: domain.TokenExpired},
		{name: "account deleted", browser: strings.Repeat("a", 43), mutate: func(u *registrationUnit) { u.account.DeletedAt = &u.challenge.SentAt }, want: domain.TokenExpired},
		{name: "already verified", browser: strings.Repeat("a", 43), mutate: func(u *registrationUnit) { u.account.Verified = true }, want: domain.TokenExpired},
		{name: "different address", browser: strings.Repeat("a", 43), mutate: func(u *registrationUnit) { u.account.Email = "changed@example.test" }, want: domain.TokenExpired},
		{name: "token persistence failure", browser: strings.Repeat("a", 43), mutate: func(u *registrationUnit) { u.failSave = true }, want: domain.Unavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			now := time.Date(2026, 9, 24, 12, 0, 0, 0, time.UTC)
			store := &registrationStore{}
			sessions := &registrationSessions{}
			svc := &Service{store: store, sessions: sessions, key: [32]byte{1}, clock: func() time.Time { return now }, origin: "https://shop.test"}
			account := domain.Account{Subject: credential.SubjectID{1}, Email: "buyer@example.test", Revision: 1, CodeEnabled: true}
			linkSecret := strings.Repeat("c", 43)
			challenge := domain.Challenge{ID: domain.ID{2}, Subject: account.Subject, Purpose: domain.VerifyEmail, Email: account.Email, Revision: 1, SentAt: now, ExpiresAt: now.Add(time.Hour)}
			challenge.Digest = svc.digest(string(domain.VerifyEmail), challenge.ID, linkSecret)
			challenge.RegistrationDigest = svc.digest("registration-browser", domain.ID(account.Subject), strings.Repeat("a", 43))
			store.state = registrationUnit{account: account, challenge: challenge}
			if tc.mutate != nil {
				tc.mutate(&store.state)
			}
			before := store.state
			tokens, err := svc.ConfirmRegistration(t.Context(), token(challenge.ID, linkSecret), tc.browser)
			if !errors.Is(err, tc.want) {
				t.Fatalf("error = %v, want %v", err, tc.want)
			}
			if (tokens.Record.ID != (session.ID{})) != tc.session {
				t.Fatal("incorrect session outcome")
			}
			if tc.want != nil {
				if store.state.account != before.account || store.state.challenge != before.challenge || store.state.created != 0 || sessions.activations != 0 {
					t.Fatal("rejection changed account or exposed session")
				}
				return
			}
			if !store.state.account.Verified || store.state.challenge.UsedAt == nil {
				t.Fatal("confirmation did not consume link and verify account")
			}
			if tc.session && (store.state.created != 1 || sessions.activations != 1) {
				t.Fatal("first login was not activated once")
			}
			if !tc.session && (store.state.created != 0 || sessions.activations != 0) {
				t.Fatal("confirmation-only created session state")
			}
			if _, err := svc.ConfirmRegistration(t.Context(), token(challenge.ID, linkSecret), tc.browser); err != domain.TokenUsed {
				t.Fatal("link was reusable")
			}
		})
	}
}
