package sessionkeys

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/v0hmly/marketmesh/platform/sessionassert"
	"github.com/v0hmly/marketmesh/services/auth/internal/domain/credential"
	domain "github.com/v0hmly/marketmesh/services/auth/internal/domain/session"
)

func makeKey(t *testing.T, kid string, from, until time.Time) fileKey {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return fileKey{Kid: kid, PrivateKey: base64.RawURLEncoding.EncodeToString(priv), PublicKey: base64.RawURLEncoding.EncodeToString(pub), SignFrom: from, SignUntil: until, VerifyUntil: until.Add(time.Minute)}
}
func replaceFile(t *testing.T, path string, keys ...fileKey) {
	t.Helper()
	data, err := json.Marshal(keyFile{Keys: keys})
	if err != nil {
		t.Fatal(err)
	}
	replacement := path + ".next"
	if err := os.WriteFile(replacement, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacement, path); err != nil {
		t.Fatal(err)
	}
}
func testConfig(t *testing.T, now *time.Time) Config {
	t.Helper()
	return Config{Path: filepath.Join(t.TempDir(), "keys.json"), Issuer: "auth.marketmesh", MaxTTL: time.Minute, Clock: func() time.Time { return *now }, Audiences: map[string][]string{"user-service": {"profile:read"}}}
}
func testRecord(now time.Time) domain.Record {
	return domain.Record{ID: domain.ID{1}, SubjectID: credential.SubjectID{2}, Version: 1, CreatedAt: now.Add(-time.Hour), AccessExpiresAt: now.Add(time.Hour), RefreshExpiresAt: now.Add(time.Hour), ExpiresAt: now.Add(time.Hour)}
}
func TestRotationBoundariesAndPublication(t *testing.T) {
	now := time.Unix(1800000000, 0).UTC()
	start := now
	cfg := testConfig(t, &now)
	old := makeKey(t, "old", now.Add(-time.Hour), now.Add(time.Minute))
	next := makeKey(t, "next", old.SignUntil, now.Add(time.Hour))
	replaceFile(t, cfg.Path, old, next)
	manager, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	record := testRecord(now)
	oldToken, err := manager.Issue(context.Background(), record, "user-service", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := sessionassert.NewVerifier(cfg.Issuer, "user-service", manager.KeySource(), sessionassert.WithVerifierClock(cfg.Clock), sessionassert.WithLeeway(0))
	if err != nil {
		t.Fatal(err)
	}
	claims, err := verifier.Verify(oldToken)
	if err != nil {
		t.Fatal(err)
	}
	if claims.Subject != base64.RawURLEncoding.EncodeToString(record.SubjectID[:]) || claims.SessionID != record.ID.String() || claims.ACR != "urn:marketmesh:auth:password" || !claims.HasScope("profile:read") {
		t.Fatal("wrong claims")
	}
	published, err := manager.PublicKeys()
	if err != nil || len(published) != 2 {
		t.Fatalf("publication: %v", err)
	}
	if published[0].Kty != "OKP" || published[0].Crv != "Ed25519" || published[0].Alg != "EdDSA" || published[0].Use != "sig" {
		t.Fatal("wrong JWK")
	}
	encoded, _ := json.Marshal(published)
	if strings.Contains(string(encoded), old.PrivateKey) {
		t.Fatal("private key published")
	}
	now = old.SignUntil
	old.PrivateKey = ""
	replaceFile(t, cfg.Path, old, next)
	token, err := manager.Issue(context.Background(), record, "user-service", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(token, ".")
	header, _ := base64.RawURLEncoding.DecodeString(parts[0])
	var h map[string]string
	if err := json.Unmarshal(header, &h); err != nil {
		t.Fatal(err)
	}
	if h["kid"] != "next" {
		t.Fatal("old signer used on boundary")
	}
	if _, ok := manager.Key("old"); !ok {
		t.Fatal("overlap key removed early")
	}
	now = old.VerifyUntil
	if _, ok := manager.Key("old"); ok {
		t.Fatal("old key trusted at expiry")
	}
	published, err = manager.PublicKeys()
	if err != nil || len(published) != 1 || published[0].Kid != "next" {
		t.Fatalf("expired publication: %v", err)
	}
	replaceFile(t, cfg.Path, next)
	if _, err := manager.PublicKeys(); err != nil {
		t.Fatal(err)
	}
	if _, ok := manager.Key("unknown"); ok {
		t.Fatal("unknown key trusted")
	}
	if _, err := verifier.Verify(oldToken); err == nil {
		t.Fatal("old assertion accepted")
	}
	now = start.Add(time.Hour)
	if _, err := manager.PublicKeys(); !errors.Is(err, domain.ErrUnavailable) {
		t.Fatalf("no active signer: %v", err)
	}
}

func TestReloadFailsClosedAndCanRecover(t *testing.T) {
	now := time.Unix(1800000000, 0).UTC()
	cfg := testConfig(t, &now)
	key := makeKey(t, "current", now.Add(-time.Hour), now.Add(time.Hour))
	replaceFile(t, cfg.Path, key)
	manager, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfg.Path, []byte("invalid private contents"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.PublicKeys(); err != domain.ErrUnavailable {
		t.Fatal(err)
	}
	if _, err := manager.Issue(context.Background(), testRecord(now), "user-service", now.Add(time.Minute)); err != domain.ErrUnavailable {
		t.Fatal(err)
	}
	if _, ok := manager.Key("current"); ok {
		t.Fatal("stale fallback")
	}
	replaceFile(t, cfg.Path, key)
	if _, ok := manager.Key("current"); !ok {
		t.Fatal("valid recovery rejected")
	}
	changed := makeKey(t, "current", key.SignFrom, key.SignUntil)
	replaceFile(t, cfg.Path, changed)
	if _, ok := manager.Key("current"); ok {
		t.Fatal("kid material replaced")
	}
	replaceFile(t, cfg.Path, key)
	if _, ok := manager.Key("current"); !ok {
		t.Fatal("invalid candidate poisoned previous key")
	}
}

func TestInvalidSchedulesAndEncoding(t *testing.T) {
	now := time.Unix(1800000000, 0).UTC()
	cases := map[string]func([]fileKey) []fileKey{
		"duplicate kid":   func(k []fileKey) []fileKey { return append(k, k[0]) },
		"overlap":         func(k []fileKey) []fileKey { k[1].SignFrom = k[0].SignUntil.Add(-time.Second); return k },
		"no active":       func(k []fileKey) []fileKey { k[0].SignFrom = now.Add(time.Second); return k },
		"missing private": func(k []fileKey) []fileKey { k[0].PrivateKey = ""; return k },
		"public newline":  func(k []fileKey) []fileKey { k[0].PublicKey += "\n"; return k },
		"private newline": func(k []fileKey) []fileKey { k[0].PrivateKey += "\n"; return k },
		"missing public":  func(k []fileKey) []fileKey { k[0].PublicKey = ""; return k },
		"bad private":     func(k []fileKey) []fileKey { k[0].PrivateKey = "secret"; return k },
		"key mismatch":    func(k []fileKey) []fileKey { k[0].PrivateKey = k[1].PrivateKey; return k },
		"short verify": func(k []fileKey) []fileKey {
			k[0].VerifyUntil = k[0].SignUntil.Add(time.Minute - time.Nanosecond)
			return k
		},
		"zero from":   func(k []fileKey) []fileKey { k[0].SignFrom = time.Time{}; return k },
		"zero until":  func(k []fileKey) []fileKey { k[0].SignUntil = time.Time{}; return k },
		"zero verify": func(k []fileKey) []fileKey { k[0].VerifyUntil = time.Time{}; return k },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := testConfig(t, &now)
			a := makeKey(t, "a", now.Add(-time.Hour), now.Add(time.Hour))
			b := makeKey(t, "b", a.SignUntil, now.Add(2*time.Hour))
			replaceFile(t, cfg.Path, mutate([]fileKey{a, b})...)
			if _, err := New(cfg); err != domain.ErrUnavailable {
				t.Fatalf("%v", err)
			}
		})
	}
}

func TestConfigAndFileBounds(t *testing.T) {
	now := time.Unix(1800000000, 0).UTC()
	cfg := testConfig(t, &now)
	key := makeKey(t, "current", now.Add(-time.Hour), now.Add(time.Hour))
	replaceFile(t, cfg.Path, key)
	cases := []Config{cfg, cfg, cfg, cfg, cfg}
	cases[0].Path = "relative.json"
	cases[1].Issuer = ""
	cases[2].MaxTTL = 0
	cases[3].Audiences = nil
	cases[4].Audiences = map[string][]string{"a": {"s", "s"}}
	for _, c := range cases {
		if _, err := New(c); err != domain.ErrUnavailable {
			t.Fatal(err)
		}
	}
	for _, contents := range []string{strings.Repeat(" ", maxFileBytes+1), `{"keys":[],"unexpected":true}`, `{"keys":[]} {}`} {
		if err := os.WriteFile(cfg.Path, []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := New(cfg); err != domain.ErrUnavailable {
			t.Fatal(err)
		}
	}
}

func TestPolicyAndCopies(t *testing.T) {
	now := time.Unix(1800000000, 0).UTC()
	cfg := testConfig(t, &now)
	key := makeKey(t, "current", now.Add(-time.Hour), now.Add(time.Hour))
	replaceFile(t, cfg.Path, key)
	manager, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Audiences["user-service"][0] = "admin"
	cfg.Audiences["attacker"] = []string{"admin"}
	pub, ok := manager.Key("current")
	if !ok {
		t.Fatal("missing key")
	}
	clear(pub)
	token, err := manager.Issue(context.Background(), testRecord(now), "user-service", now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	v, err := sessionassert.NewVerifier(cfg.Issuer, "user-service", manager.KeySource(), sessionassert.WithVerifierClock(cfg.Clock))
	if err != nil {
		t.Fatal(err)
	}
	claims, err := v.Verify(token)
	if err != nil {
		t.Fatal(err)
	}
	if claims.HasScope("admin") || !claims.HasScope("profile:read") {
		t.Fatal("caller mutated audience policy")
	}
	if _, err := manager.Issue(context.Background(), testRecord(now), "attacker", now.Add(time.Minute)); err != domain.ErrInvalidSession {
		t.Fatal(err)
	}
	for _, ttl := range []time.Duration{0, -time.Second, time.Minute + time.Second} {
		if _, err := manager.Issue(context.Background(), testRecord(now), "user-service", now.Add(ttl)); err != domain.ErrInvalidSession {
			t.Fatal(err)
		}
	}
	revoked := testRecord(now)
	revoked.RevokedAt = &now
	if _, err := manager.Issue(context.Background(), revoked, "user-service", now.Add(time.Minute)); err != domain.ErrInvalidSession {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := manager.Issue(ctx, testRecord(now), "user-service", now.Add(time.Minute)); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestConcurrentReads(t *testing.T) {
	now := time.Unix(1800000000, 0).UTC()
	cfg := testConfig(t, &now)
	key := makeKey(t, "current", now.Add(-time.Hour), now.Add(time.Hour))
	replaceFile(t, cfg.Path, key)
	manager, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 10 {
				if _, ok := manager.Key("current"); !ok {
					t.Error("missing key")
				}
				if _, err := manager.PublicKeys(); err != nil {
					t.Error(err)
				}
				if _, err := manager.Issue(context.Background(), testRecord(now), "user-service", now.Add(time.Minute)); err != nil {
					t.Error(err)
				}
			}
		}()
	}
	wg.Wait()
}

func TestReloadRetainsUnexpiredKeys(t *testing.T) {
	now := time.Unix(1800000000, 0).UTC()
	cfg := testConfig(t, &now)
	old := makeKey(t, "old", now.Add(-time.Hour), now)
	old.PrivateKey = ""
	current := makeKey(t, "current", now, now.Add(time.Hour))
	replaceFile(t, cfg.Path, old, current)
	manager, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	replaceFile(t, cfg.Path, current)
	if _, err := manager.PublicKeys(); err != domain.ErrUnavailable {
		t.Fatalf("early removal: %v", err)
	}
	replaceFile(t, cfg.Path, old, current)
	if _, err := manager.PublicKeys(); err != nil {
		t.Fatal(err)
	}
	shortened := current
	shortened.SignUntil = now.Add(30 * time.Minute)
	shortened.VerifyUntil = shortened.SignUntil.Add(time.Minute)
	replaceFile(t, cfg.Path, old, shortened)
	if _, err := manager.PublicKeys(); err != domain.ErrUnavailable {
		t.Fatalf("shortened trust: %v", err)
	}
	now = old.VerifyUntil
	replaceFile(t, cfg.Path, current)
	if _, err := manager.PublicKeys(); err != nil {
		t.Fatal(err)
	}
	reused := makeKey(t, "old", current.SignUntil, current.SignUntil.Add(time.Hour))
	replaceFile(t, cfg.Path, current, reused)
	if _, err := manager.PublicKeys(); err != domain.ErrUnavailable {
		t.Fatalf("kid reused after expiry: %v", err)
	}
}

func TestIssuePreservesDeadlineAfterCallerClockAdvances(t *testing.T) {
	for _, absolute := range []bool{false, true} {
		t.Run(map[bool]string{false: "access", true: "absolute"}[absolute], func(t *testing.T) {
			callerNow := time.Unix(1800000000, 0).UTC()
			now := callerNow
			cfg := testConfig(t, &now)
			key := makeKey(t, "current", now.Add(-time.Hour), now.Add(time.Hour))
			replaceFile(t, cfg.Path, key)
			manager, err := New(cfg)
			if err != nil {
				t.Fatal(err)
			}
			record := testRecord(callerNow)
			deadline := callerNow.Add(10*time.Second + 500*time.Millisecond)
			if absolute {
				record.ExpiresAt = deadline
			} else {
				record.AccessExpiresAt = deadline
			}
			// Caller selected an absolute deadline; entering the adapter later must not extend it.
			now = callerNow.Add(6*time.Second + 750*time.Millisecond)
			token, err := manager.Issue(context.Background(), record, "user-service", deadline.Truncate(time.Second))
			if err != nil {
				t.Fatal(err)
			}
			verifier, err := sessionassert.NewVerifier(cfg.Issuer, "user-service", manager.KeySource(), sessionassert.WithVerifierClock(cfg.Clock), sessionassert.WithLeeway(0))
			if err != nil {
				t.Fatal(err)
			}
			claims, err := verifier.Verify(token)
			if err != nil {
				t.Fatal(err)
			}
			if !claims.ExpiresAt.Equal(deadline.Truncate(time.Second)) || claims.ExpiresAt.After(deadline) {
				t.Fatalf("expiry escaped durable bound: %v", claims.ExpiresAt)
			}
			now = deadline.Truncate(time.Second)
			if _, err := manager.Issue(context.Background(), record, "user-service", deadline.Truncate(time.Second)); err != domain.ErrInvalidSession {
				t.Fatalf("subsecond lifetime accepted: %v", err)
			}
			now = deadline
			if _, err := manager.Issue(context.Background(), record, "user-service", deadline.Truncate(time.Second)); err != domain.ErrInvalidSession {
				t.Fatalf("exact expiry accepted: %v", err)
			}
		})
	}
}

func TestIssueRechecksClockAfterSecretRead(t *testing.T) {
	now := time.Unix(1800000000, 0).UTC()
	cfg := testConfig(t, &now)
	key := makeKey(t, "current", now.Add(-time.Hour), now.Add(time.Hour))
	replaceFile(t, cfg.Path, key)
	calls := 0
	cfg.Clock = func() time.Time {
		calls++
		if calls >= 3 {
			return now.Add(time.Minute)
		}
		return now
	}
	manager, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	} // first clock read
	record := testRecord(now)
	record.AccessExpiresAt = now.Add(time.Minute)
	if _, err := manager.Issue(context.Background(), record, "user-service", now.Add(time.Minute)); err != domain.ErrInvalidSession {
		t.Fatalf("post-load expiry accepted: %v", err)
	}
}

func TestIssueRejectsDeadlineOutsideCanonicalBounds(t *testing.T) {
	now := time.Unix(1800000000, 0).UTC()
	cfg := testConfig(t, &now)
	key := makeKey(t, "current", now.Add(-time.Hour), now.Add(time.Hour))
	replaceFile(t, cfg.Path, key)
	manager, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	record := testRecord(now)
	record.AccessExpiresAt = now.Add(10 * time.Second)
	for _, deadline := range []time.Time{now.Add(11 * time.Second), now.Add(time.Second + time.Nanosecond), now} {
		if _, err := manager.Issue(context.Background(), record, "user-service", deadline); err != domain.ErrInvalidSession {
			t.Fatalf("%v: %v", deadline, err)
		}
	}
	record.AccessExpiresAt = now.Add(time.Hour)
	record.ExpiresAt = now.Add(10 * time.Second)
	if _, err := manager.Issue(context.Background(), record, "user-service", now.Add(11*time.Second)); err != domain.ErrInvalidSession {
		t.Fatal(err)
	}
}

func TestIssueRejectsExpiryDuringSigning(t *testing.T) {
	now := time.Unix(1800000000, 0).UTC()
	cfg := testConfig(t, &now)
	key := makeKey(t, "current", now.Add(-time.Hour), now.Add(time.Hour))
	replaceFile(t, cfg.Path, key)
	calls := 0
	cfg.Clock = func() time.Time {
		calls++
		if calls >= 4 {
			return now.Add(time.Second)
		}
		return now
	}
	manager, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Issue(context.Background(), testRecord(now), "user-service", now.Add(time.Second)); err != domain.ErrInvalidSession {
		t.Fatalf("expired during signing: %v", err)
	}
}
