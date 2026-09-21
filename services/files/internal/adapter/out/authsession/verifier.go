// Package authsession verifies internal assertions and current Auth session state.
package authsession

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"

	authv1 "github.com/v0hmly/marketmesh/api/gen/go/auth/v1"
	"github.com/v0hmly/marketmesh/platform/sessionassert"
	"github.com/v0hmly/marketmesh/services/files/internal/application/identity"
	"github.com/v0hmly/marketmesh/services/files/internal/domain/file"
	"google.golang.org/grpc"
)

const (
	MaxAssertionBytes = 16 * 1024
	maxJWKSBytes      = 64 * 1024
	maxKeys           = 64
	readScope         = "files:read"
	writeScope        = "files:write"
)

type Config struct {
	Issuer  string
	MaxTTL  time.Duration
	Timeout time.Duration
	Clock   func() time.Time
}

type Verifier struct {
	client authv1.AuthInternalServiceClient
	config Config
}

func New(client authv1.AuthInternalServiceClient, config Config) (*Verifier, error) {
	if client == nil || strings.TrimSpace(config.Issuer) == "" || config.MaxTTL < time.Second || config.MaxTTL > 5*time.Minute || config.MaxTTL%time.Second != 0 || config.Timeout <= 0 {
		return nil, errors.New("files authsession: invalid configuration")
	}
	if config.Clock == nil {
		config.Clock = time.Now
	}
	return &Verifier{client: client, config: config}, nil
}

// Ready checks the authenticated Auth endpoint and its currently usable signing keys.
func (v *Verifier) Ready(ctx context.Context) error {
	if ctx == nil {
		return identity.ErrUnauthenticated
	}
	ctx, cancel := context.WithTimeout(ctx, v.config.Timeout)
	defer cancel()
	response, err := v.client.GetSigningKeys(ctx, &authv1.GetSigningKeysRequest{}, grpc.MaxCallRecvMsgSize(maxJWKSBytes+1024))
	if err != nil || response == nil || ctx.Err() != nil {
		return identity.ErrUnauthenticated
	}
	if _, err = parseKeys(response.GetJwksJson(), v.config.Clock); err != nil {
		return identity.ErrUnauthenticated
	}
	return nil
}

// Verify never accepts external credentials and never caches authentication results.
func (v *Verifier) Verify(ctx context.Context, token string) (identity.Principal, error) {
	denied := func() (identity.Principal, error) { return identity.Principal{}, identity.ErrUnauthenticated }
	if ctx == nil || len(token) == 0 || len(token) > MaxAssertionBytes || strings.Count(token, ".") != 2 || strings.ContainsAny(token, " \t\r\n;=") {
		return denied()
	}
	ctx, cancel := context.WithTimeout(ctx, v.config.Timeout)
	defer cancel()
	// Refresh is explicit and independent of kid. Key performs only local lookups.
	response, err := v.client.GetSigningKeys(ctx, &authv1.GetSigningKeysRequest{}, grpc.MaxCallRecvMsgSize(maxJWKSBytes+1024))
	if err != nil || response == nil {
		return denied()
	}
	keys, err := parseKeys(response.GetJwksJson(), v.config.Clock)
	if err != nil {
		return denied()
	}
	verifier, err := sessionassert.NewVerifier(v.config.Issuer, "files", keys, sessionassert.WithLeeway(0), sessionassert.WithVerifierMaxTTL(v.config.MaxTTL), sessionassert.WithVerifierClock(v.config.Clock))
	if err != nil {
		return denied()
	}
	claims, err := verifier.VerifyContext(ctx, token)
	if err != nil {
		return denied()
	}
	raw, err := base64.RawURLEncoding.Strict().DecodeString(claims.Subject)
	if err != nil {
		return denied()
	}
	subject, err := file.ParseID(raw)
	if err != nil {
		return denied()
	}
	verified, err := v.client.VerifyAssertion(ctx, &authv1.VerifyAssertionRequest{Assertion: token}, grpc.MaxCallRecvMsgSize(4096))
	if err != nil || verified == nil || !bytes.Equal(verified.GetSubjectId(), raw) || verified.GetSessionId() != claims.SessionID || verified.GetExpiresAtUnix() != claims.ExpiresAt.Unix() || ctx.Err() != nil || !v.config.Clock().Before(claims.ExpiresAt) {
		return denied()
	}
	return identity.Principal{Owner: file.Owner{Tenant: subject, Subject: subject}, CanRead: claims.HasScope(readScope), CanWrite: claims.HasScope(writeScope)}, nil
}

type publicKey struct {
	Kty         string    `json:"kty"`
	Crv         string    `json:"crv"`
	Alg         string    `json:"alg"`
	Use         string    `json:"use"`
	Kid         string    `json:"kid"`
	X           string    `json:"x"`
	VerifyUntil time.Time `json:"verify_until"`
}
type trustedKey struct {
	key   ed25519.PublicKey
	until time.Time
}
type keySet struct {
	keys  map[string]trustedKey
	clock func() time.Time
}

func (s keySet) Key(kid string) (ed25519.PublicKey, error) {
	key, ok := s.keys[kid]
	if !ok || !s.clock().Before(key.until) {
		return nil, sessionassert.ErrUnknownKeyID
	}
	return append(ed25519.PublicKey(nil), key.key...), nil
}
func parseKeys(raw string, clock func() time.Time) (keySet, error) {
	invalid := errors.New("files authsession: invalid signing keys")
	if len(raw) == 0 || len(raw) > maxJWKSBytes {
		return keySet{}, invalid
	}
	var document struct {
		Keys []publicKey `json:"keys"`
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&document) != nil || decoder.Decode(new(any)) != io.EOF || len(document.Keys) == 0 || len(document.Keys) > maxKeys {
		return keySet{}, invalid
	}
	result := keySet{keys: make(map[string]trustedKey, len(document.Keys)), clock: clock}
	for _, item := range document.Keys {
		if item.Kty != "OKP" || item.Crv != "Ed25519" || item.Alg != "EdDSA" || item.Use != "sig" || item.Kid == "" || len(item.Kid) > 256 || item.VerifyUntil.IsZero() || !clock().Before(item.VerifyUntil) {
			return keySet{}, invalid
		}
		if _, exists := result.keys[item.Kid]; exists {
			return keySet{}, invalid
		}
		rawKey, err := base64.RawURLEncoding.Strict().DecodeString(item.X)
		if err != nil || len(rawKey) != ed25519.PublicKeySize {
			return keySet{}, invalid
		}
		result.keys[item.Kid] = trustedKey{key: ed25519.PublicKey(rawKey), until: item.VerifyUntil}
	}
	return result, nil
}
