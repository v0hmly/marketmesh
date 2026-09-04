package sessionassert

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

// testKeys генерирует пару ключей Ed25519 для тестов.
func testKeys(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return pub, priv
}

// newTestIssuer создаёт издателя со свежим ключом.
func newTestIssuer(t *testing.T, kid string) (*Issuer, ed25519.PublicKey) {
	t.Helper()
	pub, priv := testKeys(t)
	iss, err := NewIssuer(priv, kid, "auth.marketmesh")
	if err != nil {
		t.Fatalf("NewIssuer: %v", err)
	}
	return iss, pub
}

// newTestVerifier создаёт верификатор с одним доверенным ключом.
func newTestVerifier(t *testing.T, kid string, pub ed25519.PublicKey, opts ...VerifierOption) *Verifier {
	t.Helper()
	ks := NewStaticKeySet()
	if err := ks.Add(kid, pub); err != nil {
		t.Fatalf("StaticKeySet.Add: %v", err)
	}
	v, err := NewVerifier("auth.marketmesh", "user-service", ks, opts...)
	if err != nil {
		t.Fatalf("NewVerifier: %v", err)
	}
	return v
}

// validParams возвращает корректные параметры выпуска.
func validParams() IssueParams {
	return IssueParams{
		Audience:  "user-service",
		Subject:   "u-123",
		SessionID: "s-456",
		TTL:       time.Minute,
		AuthTime:  time.Now().Add(-time.Hour),
		ACR:       "urn:marketmesh:acr:1",
		AMR:       []string{"pwd", "otp"},
		Scopes:    []string{"profile:read"},
	}
}

// craftToken подписывает произвольные header/payload указанным ключом.
// Нужен для изготовления токенов, которые штатный Issuer не выпустит.
func craftToken(t *testing.T, priv ed25519.PrivateKey, header, payload map[string]any) string {
	t.Helper()
	h, err := json.Marshal(header)
	if err != nil {
		t.Fatalf("marshal header: %v", err)
	}
	p, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return signCompact(priv, h, p)
}

// validPayload возвращает корректные claims в виде map для craftToken.
func validPayload() map[string]any {
	now := time.Now().UTC().Truncate(time.Second)
	return map[string]any{
		"iss":       "auth.marketmesh",
		"aud":       "user-service",
		"sub":       "u-123",
		"sid":       "s-456",
		"iat":       now.Unix(),
		"exp":       now.Add(time.Minute).Unix(),
		"jti":       "j-1",
		"auth_time": now.Add(-time.Hour).Unix(),
		"acr":       "urn:marketmesh:acr:1",
		"amr":       []string{"pwd"},
		"scope":     []string{"profile:read"},
		"typ":       TokenType,
	}
}

func validHeader(kid string) map[string]any {
	return map[string]any{"alg": "EdDSA", "typ": TokenType, "kid": kid}
}

func TestIssueVerifyRoundTrip(t *testing.T) {
	iss, pub := newTestIssuer(t, "k1")
	v := newTestVerifier(t, "k1", pub)

	token, err := iss.Issue(validParams())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	claims, err := v.Verify(token)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}

	if claims.Issuer != "auth.marketmesh" || claims.Audience != "user-service" ||
		claims.Subject != "u-123" || claims.SessionID != "s-456" {
		t.Fatalf("unexpected claims: %v", claims)
	}
	if claims.ID == "" {
		t.Fatal("empty jti")
	}
	if claims.ACR != "urn:marketmesh:acr:1" {
		t.Fatalf("unexpected acr: %q", claims.ACR)
	}
	if len(claims.AMR) != 2 || claims.AMR[0] != "pwd" || claims.AMR[1] != "otp" {
		t.Fatalf("unexpected amr: %v", claims.AMR)
	}
	if !claims.HasScope("profile:read") {
		t.Fatalf("missing scope: %v", claims.Scopes)
	}
	if claims.ExpiresAt.Sub(claims.IssuedAt) != time.Minute {
		t.Fatalf("unexpected ttl: iat=%s exp=%s", claims.IssuedAt, claims.ExpiresAt)
	}
	// Время кодируется секундами unix.
	if claims.IssuedAt.Nanosecond() != 0 || claims.ExpiresAt.Nanosecond() != 0 {
		t.Fatalf("timestamps not truncated to seconds: %s %s", claims.IssuedAt, claims.ExpiresAt)
	}
}

func TestIssueDefaultTTL(t *testing.T) {
	iss, _ := newTestIssuer(t, "k1")
	p := validParams()
	p.TTL = 0
	token, err := iss.Issue(p)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	var cj claimsJSON
	payload, err := base64.RawURLEncoding.DecodeString(strings.Split(token, ".")[1])
	if err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	if err := json.Unmarshal(payload, &cj); err != nil {
		t.Fatalf("unmarshal payload: %v", err)
	}
	if got := time.Duration(cj.ExpiresAt-cj.IssuedAt) * time.Second; got != DefaultMaxTTL {
		t.Fatalf("default TTL = %s, want %s", got, DefaultMaxTTL)
	}
}

func TestIssueValidation(t *testing.T) {
	iss, _ := newTestIssuer(t, "k1")

	cases := map[string]func(*IssueParams){
		"empty audience":  func(p *IssueParams) { p.Audience = "" },
		"empty subject":   func(p *IssueParams) { p.Subject = "" },
		"empty session":   func(p *IssueParams) { p.SessionID = "" },
		"zero auth_time":  func(p *IssueParams) { p.AuthTime = time.Time{} },
		"ttl over max":    func(p *IssueParams) { p.TTL = DefaultMaxTTL + time.Second },
		"negative ttl":    func(p *IssueParams) { p.TTL = -time.Second },
		"empty scope":     func(p *IssueParams) { p.Scopes = []string{"profile:read", ""} },
		"duplicate scope": func(p *IssueParams) { p.Scopes = []string{"a", "a"} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			p := validParams()
			mutate(&p)
			if _, err := iss.Issue(p); !errors.Is(err, ErrInvalidParams) {
				t.Fatalf("Issue error = %v, want ErrInvalidParams", err)
			}
		})
	}
}

func TestNewIssuerValidation(t *testing.T) {
	_, priv := testKeys(t)
	if _, err := NewIssuer(priv, "", "auth"); !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("empty kid: %v", err)
	}
	if _, err := NewIssuer(priv, "k1", ""); !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("empty issuer: %v", err)
	}
	if _, err := NewIssuer(priv[:10], "k1", "auth"); !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("short key: %v", err)
	}
	if _, err := NewIssuer(priv, "k1", "auth", WithMaxTTL(0)); !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("zero maxTTL: %v", err)
	}
}

func TestNewVerifierValidation(t *testing.T) {
	pub, _ := testKeys(t)
	ks := NewStaticKeySet()
	if err := ks.Add("k1", pub); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if _, err := NewVerifier("", "aud", ks); !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("empty issuer: %v", err)
	}
	if _, err := NewVerifier("iss", "", ks); !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("empty audience: %v", err)
	}
	if _, err := NewVerifier("iss", "aud", nil); !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("nil keys: %v", err)
	}
	if _, err := NewVerifier("iss", "aud", ks, WithLeeway(-time.Second)); !errors.Is(err, ErrInvalidParams) {
		t.Fatalf("negative leeway: %v", err)
	}
}
