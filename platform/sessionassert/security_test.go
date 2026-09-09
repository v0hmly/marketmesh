package sessionassert

import (
	"context"
	"crypto/ed25519"
	"errors"
	"testing"
	"time"
)

type sessionCheckFunc func(context.Context, Claims) error

func (f sessionCheckFunc) CheckSession(ctx context.Context, c Claims) error { return f(ctx, c) }

func TestDeterministicClockAndExpiry(t *testing.T) {
	pub, priv := testKeys(t)
	now := time.Unix(1800000000, 0).UTC()
	clock := func() time.Time { return now }
	issuer, err := NewIssuer(priv, "k1", "auth.marketmesh", WithIssuerClock(clock))
	if err != nil {
		t.Fatal(err)
	}
	p := validParams()
	p.AuthTime = now.Add(-time.Hour)
	p.TTL = time.Minute
	token, err := issuer.Issue(p)
	if err != nil {
		t.Fatal(err)
	}
	for _, leeway := range []time.Duration{0, 30 * time.Second} {
		v := newTestVerifier(t, "k1", pub, WithVerifierClock(clock), WithLeeway(leeway))
		now = time.Unix(1800000000, 0).Add(time.Minute + leeway - time.Nanosecond)
		c, err := v.Verify(token)
		if err != nil {
			t.Fatal(err)
		}
		if !c.IssuedAt.Equal(time.Unix(1800000000, 0)) {
			t.Fatal("issuer ignored clock")
		}
		now = now.Add(time.Nanosecond)
		if _, err := v.Verify(token); !errors.Is(err, ErrExpired) {
			t.Fatalf("boundary: %v", err)
		}
	}
}

func TestRejectInvalidSignedClaims(t *testing.T) {
	pub, priv := testKeys(t)
	now := time.Now().UTC().Truncate(time.Second)
	v := newTestVerifier(t, "k1", pub, WithVerifierClock(func() time.Time { return now }))
	cases := map[string]func(map[string]any){
		"subject":        func(p map[string]any) { delete(p, "sub") },
		"session":        func(p map[string]any) { delete(p, "sid") },
		"id":             func(p map[string]any) { delete(p, "jti") },
		"acr":            func(p map[string]any) { delete(p, "acr") },
		"amr":            func(p map[string]any) { delete(p, "amr") },
		"duplicate amr":  func(p map[string]any) { p["amr"] = []string{"pwd", "pwd"} },
		"scope":          func(p map[string]any) { p["scope"] = []string{""} },
		"iat":            func(p map[string]any) { delete(p, "iat") },
		"auth time":      func(p map[string]any) { delete(p, "auth_time") },
		"future auth":    func(p map[string]any) { p["auth_time"] = now.Add(time.Second).Unix() },
		"exp equals iat": func(p map[string]any) { p["exp"] = now.Unix() },
		"long ttl":       func(p map[string]any) { p["exp"] = now.Add(DefaultMaxTTL + time.Second).Unix() },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			p := validPayload()
			p["iat"] = now.Unix()
			mutate(p)
			if _, err := v.Verify(craftToken(t, priv, validHeader("k1"), p)); !errors.Is(err, ErrMalformed) {
				t.Fatalf("%v", err)
			}
		})
	}
}

func TestSessionCheckerFailClosedAndContext(t *testing.T) {
	issuer, pub := newTestIssuer(t, "k1")
	token, err := issuer.Issue(validParams())
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	rejected := false
	type contextKey struct{}
	ctx := context.WithValue(context.Background(), contextKey{}, true)
	v := newTestVerifier(t, "k1", pub, WithSessionChecker(sessionCheckFunc(func(got context.Context, c Claims) error {
		calls++
		if got.Value(contextKey{}) != true || c.SessionID == "" {
			t.Fatal("missing verified context/claims")
		}
		c.Scopes[0] = "mutated"
		if rejected {
			return errors.New("private storage details")
		}
		return nil
	})))
	c, err := v.VerifyContext(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	if c.Scopes[0] == "mutated" {
		t.Fatal("checker mutated verified claims")
	}
	rejected = true
	if _, err := v.VerifyContext(ctx, token); err != ErrSessionRejected {
		t.Fatalf("%v", err)
	}
	if _, err := v.VerifyContext(ctx, "invalid"); err == nil {
		t.Fatal("accepted malformed token")
	}
	if calls != 2 {
		t.Fatalf("checker called before verification: %d", calls)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := v.VerifyContext(canceled, token); !errors.Is(err, context.Canceled) {
		t.Fatalf("%v", err)
	}
	if calls != 2 {
		t.Fatal("checker called after cancellation")
	}
}

func TestDefensiveKeyCopies(t *testing.T) {
	pub, priv := testKeys(t)
	original := append(ed25519.PublicKey(nil), pub...)
	issuer, err := NewIssuer(priv, "k1", "auth.marketmesh")
	if err != nil {
		t.Fatal(err)
	}
	ks := NewStaticKeySet()
	if err := ks.Add("k1", pub); err != nil {
		t.Fatal(err)
	}
	clear(pub)
	clear(priv)
	got, err := ks.Key("k1")
	if err != nil {
		t.Fatal(err)
	}
	clear(got)
	v, err := NewVerifier("auth.marketmesh", "user-service", ks)
	if err != nil {
		t.Fatal(err)
	}
	token, err := issuer.Issue(validParams())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := v.Verify(token); err != nil {
		t.Fatal(err)
	}
	got, err = ks.Key("k1")
	if err != nil {
		t.Fatal(err)
	}
	if !got.Equal(original) {
		t.Fatal("caller altered key set")
	}
}

func TestInvalidClockAndTTLConfig(t *testing.T) {
	pub, priv := testKeys(t)
	if _, err := NewIssuer(priv, "k1", "auth", WithIssuerClock(nil)); !errors.Is(err, ErrInvalidParams) {
		t.Fatal(err)
	}
	ks := NewStaticKeySet()
	if err := ks.Add("k1", pub); err != nil {
		t.Fatal(err)
	}
	for _, opt := range []VerifierOption{WithVerifierClock(nil), WithVerifierMaxTTL(0), WithLeeway(DefaultMaxTTL + time.Second)} {
		if _, err := NewVerifier("auth", "user", ks, opt); !errors.Is(err, ErrInvalidParams) {
			t.Fatal(err)
		}
	}
}

func TestSlowCheckerCannotExtendAssertionLifetime(t *testing.T) {
	pub, priv := testKeys(t)
	now := time.Now().UTC().Truncate(time.Second)
	payload := validPayload()
	payload["iat"] = now.Unix()
	payload["exp"] = now.Add(time.Minute).Unix()
	v := newTestVerifier(t, "k1", pub, WithLeeway(0), WithVerifierClock(func() time.Time { return now }),
		WithSessionChecker(sessionCheckFunc(func(context.Context, Claims) error { now = now.Add(time.Minute); return nil })))
	if _, err := v.Verify(craftToken(t, priv, validHeader("k1"), payload)); !errors.Is(err, ErrExpired) {
		t.Fatalf("%v", err)
	}
}

func TestVerifierCustomMaxTTL(t *testing.T) {
	pub, priv := testKeys(t)
	v := newTestVerifier(t, "k1", pub, WithVerifierMaxTTL(30*time.Second))
	payload := validPayload()
	if _, err := v.Verify(craftToken(t, priv, validHeader("k1"), payload)); !errors.Is(err, ErrMalformed) {
		t.Fatalf("%v", err)
	}
	payload["exp"] = payload["iat"].(int64) + 30
	if _, err := v.Verify(craftToken(t, priv, validHeader("k1"), payload)); err != nil {
		t.Fatal(err)
	}
}
