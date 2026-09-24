package application

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type memory struct {
	mu       sync.Mutex
	login    map[string]Login
	sessions map[string]Session
	idle     map[string]time.Time
}

func newMemory() *memory {
	return &memory{login: map[string]Login{}, sessions: map[string]Session{}, idle: map[string]time.Time{}}
}
func (m *memory) SaveLogin(_ context.Context, l Login) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.login[l.StateHash] = l
	return nil
}
func (m *memory) ConsumeLogin(_ context.Context, state, browser string, now time.Time) (Login, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.login[state]
	if !ok || l.BrowserHash != browser || !now.Before(l.ExpiresAt) {
		return Login{}, ErrInvalidLogin
	}
	delete(m.login, state)
	return l, nil
}
func (m *memory) SaveSession(_ context.Context, hash string, p Principal, absolute, idle time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[hash] = Session{Principal: p, ExpiresAt: absolute}
	m.idle[hash] = idle
	return nil
}
func (m *memory) Session(_ context.Context, hash string, now, idle time.Time) (Session, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[hash]
	if !ok || !now.Before(s.ExpiresAt) || !now.Before(m.idle[hash]) {
		return Session{}, ErrUnauthenticated
	}
	m.idle[hash] = idle
	return s, nil
}
func (m *memory) DeleteSession(_ context.Context, hash string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, hash)
	return nil
}
func (*memory) Invite(context.Context, string, Principal, time.Time, bool) (Invite, error) {
	return Invite{}, nil
}

type identity struct {
	state, nonce, verifier string
	calls                  int
}

func (p *identity) Authorize(state, nonce, verifier string) string {
	p.state, p.nonce, p.verifier = state, nonce, verifier
	return "https://idp.test/authorize"
}
func (p *identity) Exchange(_ context.Context, _ string, verifier, nonce string) (Principal, error) {
	p.calls++
	if verifier != p.verifier || nonce != p.nonce {
		return Principal{}, ErrInvalidLogin
	}
	return Principal{Issuer: "https://idp.test", Subject: "employee", Email: "Employee@example.test"}, nil
}
func TestLoginBindingReplayAndDeadlines(t *testing.T) {
	store, idp := newMemory(), &identity{}
	service := New(store, idp, time.Minute, time.Hour)
	now := time.Now()
	service.now = func() time.Time { return now }
	invite := token()
	_, browser, err := service.Start(t.Context(), invite, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = service.Complete(t.Context(), idp.state, token(), "code"); !errors.Is(err, ErrInvalidLogin) {
		t.Fatal("different browser accepted")
	}
	if idp.calls != 0 {
		t.Fatal("invalid callback reached IdP")
	}
	cookie, returned, err := service.Complete(t.Context(), idp.state, browser, "code")
	if err != nil || returned != invite {
		t.Fatalf("valid callback failed: %v", err)
	}
	if !ValidToken(cookie) || cookie == browser || Digest(cookie) == cookie {
		t.Fatal("session token not independent")
	}
	if _, _, err = service.Complete(t.Context(), idp.state, browser, "code"); !errors.Is(err, ErrInvalidLogin) {
		t.Fatal("callback replay accepted")
	}
	session, err := service.Session(t.Context(), cookie)
	if err != nil || session.Email != "employee@example.test" {
		t.Fatal("verified identity missing")
	}
	now = now.Add(2 * time.Minute)
	if _, err = service.Session(t.Context(), cookie); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("idle session accepted")
	}
	_, browser, err = service.Start(t.Context(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(11 * time.Minute)
	if _, _, err = service.Complete(t.Context(), idp.state, browser, "code"); !errors.Is(err, ErrInvalidLogin) {
		t.Fatal("expired login accepted")
	}
}
func TestLogoutAndAbsoluteExpiry(t *testing.T) {
	store, idp := newMemory(), &identity{}
	service := New(store, idp, time.Hour, time.Hour)
	now := time.Now()
	service.now = func() time.Time { return now }
	login := func() string {
		_, browser, err := service.Start(t.Context(), "", "")
		if err != nil {
			t.Fatal(err)
		}
		cookie, _, err := service.Complete(t.Context(), idp.state, browser, "code")
		if err != nil {
			t.Fatal(err)
		}
		return cookie
	}
	revoked := login()
	if err := service.Logout(t.Context(), revoked); err != nil {
		t.Fatal(err)
	}
	if _, err := service.Session(t.Context(), revoked); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("revoked session accepted")
	}
	cookie := login()
	now = now.Add(50 * time.Minute)
	if _, err := service.Session(t.Context(), cookie); err != nil {
		t.Fatal(err)
	}
	now = now.Add(11 * time.Minute)
	if _, err := service.Session(t.Context(), cookie); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("activity extended absolute lifetime")
	}
}

func TestStartingAnotherLoginRevokesCurrentSession(t *testing.T) {
	store, idp := newMemory(), &identity{}
	service := New(store, idp, time.Hour, time.Hour)
	_, browser, err := service.Start(t.Context(), "", "")
	if err != nil {
		t.Fatal(err)
	}
	old, _, err := service.Complete(t.Context(), idp.state, browser, "code")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = service.Start(t.Context(), "", old); err != nil {
		t.Fatal(err)
	}
	if _, err = service.Session(t.Context(), old); !errors.Is(err, ErrUnauthenticated) {
		t.Fatal("old session survived another login")
	}
}
