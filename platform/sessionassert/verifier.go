package sessionassert

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// DefaultLeeway — допустимое окно рассинхронизации часов по умолчанию.
const DefaultLeeway = 30 * time.Second

// Verifier локально проверяет утверждения по набору доверенных открытых
// ключей. Алгоритм зафиксирован: подпись всегда проверяется как
// EdDSA/Ed25519 независимо от значения alg в заголовке (RFC 8725).
type Verifier struct {
	issuer         string
	audience       string
	keys           KeySource
	leeway         time.Duration
	requiredScopes []string
}

// VerifierOption настраивает Verifier.
type VerifierOption func(*Verifier)

// WithLeeway задаёт допустимое окно рассинхронизации часов для exp и iat.
func WithLeeway(d time.Duration) VerifierOption {
	return func(v *Verifier) { v.leeway = d }
}

// RequireScopes задаёт области действия, обязательные для каждого
// принимаемого утверждения.
func RequireScopes(scopes ...string) VerifierOption {
	return func(v *Verifier) { v.requiredScopes = append(v.requiredScopes, scopes...) }
}

// NewVerifier создаёт верификатор для ожидаемых издателя и аудитории
// с источником доверенных открытых ключей.
func NewVerifier(issuer, audience string, keys KeySource, opts ...VerifierOption) (*Verifier, error) {
	if issuer == "" {
		return nil, fmt.Errorf("%w: empty issuer", ErrInvalidParams)
	}
	if audience == "" {
		return nil, fmt.Errorf("%w: empty audience", ErrInvalidParams)
	}
	if keys == nil {
		return nil, fmt.Errorf("%w: nil key source", ErrInvalidParams)
	}
	v := &Verifier{issuer: issuer, audience: audience, keys: keys, leeway: DefaultLeeway}
	for _, opt := range opts {
		opt(v)
	}
	if v.leeway < 0 {
		return nil, fmt.Errorf("%w: negative leeway", ErrInvalidParams)
	}
	return v, nil
}

// Verify разбирает и проверяет утверждение: compact-формат, тип заголовка,
// подпись Ed25519 по kid из доверенного набора, издателя, аудиторию, срок
// действия и обязательные области. При успехе возвращает типизированные
// claims.
func (v *Verifier) Verify(token string) (*Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("%w: want 3 segments, got %d", ErrMalformed, len(parts))
	}
	enc := base64.RawURLEncoding
	headerRaw, err := enc.DecodeString(parts[0])
	if err != nil || len(headerRaw) == 0 {
		return nil, fmt.Errorf("%w: bad header segment", ErrMalformed)
	}
	payloadRaw, err := enc.DecodeString(parts[1])
	if err != nil || len(payloadRaw) == 0 {
		return nil, fmt.Errorf("%w: bad payload segment", ErrMalformed)
	}
	sig, err := enc.DecodeString(parts[2])
	if err != nil || len(sig) != ed25519.SignatureSize {
		return nil, fmt.Errorf("%w: bad signature segment", ErrMalformed)
	}

	var header assertionHeader
	if err := json.Unmarshal(headerRaw, &header); err != nil {
		return nil, fmt.Errorf("%w: invalid header JSON", ErrMalformed)
	}
	if header.Typ != TokenType {
		return nil, fmt.Errorf("%w: header typ %q", ErrBadType, header.Typ)
	}
	if header.Kid == "" {
		return nil, fmt.Errorf("%w: empty kid", ErrMalformed)
	}

	key, err := v.keys.Key(header.Kid)
	if err != nil {
		if errors.Is(err, ErrUnknownKeyID) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: key source: %v", ErrUnknownKeyID, err)
	}
	// alg из заголовка намеренно игнорируется: подпись всегда Ed25519.
	if !ed25519.Verify(key, []byte(parts[0]+"."+parts[1]), sig) {
		return nil, fmt.Errorf("%w: kid %q", ErrBadSignature, header.Kid)
	}

	var cj claimsJSON
	if err := json.Unmarshal(payloadRaw, &cj); err != nil {
		return nil, fmt.Errorf("%w: invalid payload JSON", ErrMalformed)
	}
	if cj.Type != TokenType {
		return nil, fmt.Errorf("%w: claims typ %q", ErrBadType, cj.Type)
	}

	now := time.Now().UTC()
	exp := time.Unix(cj.ExpiresAt, 0).UTC()
	iat := time.Unix(cj.IssuedAt, 0).UTC()
	if now.After(exp.Add(v.leeway)) {
		return nil, fmt.Errorf("%w: exp %s", ErrExpired, exp.Format(time.RFC3339))
	}
	if iat.After(now.Add(v.leeway)) {
		return nil, fmt.Errorf("%w: iat %s", ErrNotYetValid, iat.Format(time.RFC3339))
	}
	if cj.Issuer != v.issuer {
		return nil, fmt.Errorf("%w: got %q", ErrBadIssuer, cj.Issuer)
	}
	if cj.Audience != v.audience {
		return nil, fmt.Errorf("%w: got %q", ErrBadAudience, cj.Audience)
	}

	claims := claimsFromJSON(cj)
	for _, scope := range v.requiredScopes {
		if !claims.HasScope(scope) {
			return nil, fmt.Errorf("%w: %q", ErrMissingScope, scope)
		}
	}
	return claims, nil
}
