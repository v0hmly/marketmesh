package sessionassert

import (
	"context"
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
// EdDSA/Ed25519; alg обязан явно указывать EdDSA.
type Verifier struct {
	issuer         string
	audience       string
	keys           KeySource
	leeway         time.Duration
	requiredScopes []string
	clock          func() time.Time
	maxTTL         time.Duration
	checker        SessionChecker
}

// VerifierOption настраивает Verifier.
type VerifierOption func(*Verifier)

// SessionChecker проверяет актуальность уже криптографически проверенной сессии.
// Ошибка, включая недоступность хранилища, запрещает использование утверждения.
// Реализация обязана соблюдать отмену и deadline переданного context.
type SessionChecker interface {
	CheckSession(context.Context, Claims) error
}

// WithSessionChecker включает online-проверку сессии после локальной проверки.
func WithSessionChecker(checker SessionChecker) VerifierOption {
	return func(v *Verifier) { v.checker = checker }
}

// WithVerifierClock задаёт конкурентно безопасные часы верификатора.
func WithVerifierClock(clock func() time.Time) VerifierOption {
	return func(v *Verifier) { v.clock = clock }
}

// WithVerifierMaxTTL ограничивает exp-iat; по умолчанию DefaultMaxTTL.
func WithVerifierMaxTTL(ttl time.Duration) VerifierOption {
	return func(v *Verifier) { v.maxTTL = ttl }
}

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
	v := &Verifier{issuer: issuer, audience: audience, keys: keys, leeway: DefaultLeeway, maxTTL: DefaultMaxTTL, clock: time.Now}
	for _, opt := range opts {
		opt(v)
	}
	if v.clock == nil || v.maxTTL < time.Second || v.leeway < 0 || v.leeway > v.maxTTL || !validValues(v.requiredScopes, false) {
		return nil, fmt.Errorf("%w: invalid clock, TTL, leeway or required scopes", ErrInvalidParams)
	}
	return v, nil
}

// Verify разбирает и проверяет утверждение: compact-формат, тип заголовка,
// подпись Ed25519 по kid из доверенного набора, издателя, аудиторию, срок
// действия и обязательные области. При успехе возвращает типизированные
// claims.
func (v *Verifier) Verify(token string) (*Claims, error) {
	return v.VerifyContext(context.Background(), token)
}

// VerifyContext проверяет утверждение и, если настроено, актуальность сессии.
func (v *Verifier) VerifyContext(ctx context.Context, token string) (*Claims, error) {
	if ctx == nil {
		return nil, ErrInvalidParams
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

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

	if header.Alg != "EdDSA" {
		return nil, ErrBadAlgorithm
	}

	key, err := v.keys.Key(header.Kid)
	if err != nil {
		if errors.Is(err, ErrUnknownKeyID) {
			return nil, err
		}
		return nil, fmt.Errorf("%w: key source: %v", ErrUnknownKeyID, err)
	}
	// Алгоритм и его реализация фиксированы, ключи других размеров отклоняются.
	if len(key) != ed25519.PublicKeySize {
		return nil, ErrBadSignature
	}
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

	now := v.clock().UTC()
	exp := time.Unix(cj.ExpiresAt, 0).UTC()
	iat := time.Unix(cj.IssuedAt, 0).UTC()
	if !now.Before(exp.Add(v.leeway)) {
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

	if cj.Subject == "" || cj.SessionID == "" || cj.ID == "" || cj.ACR == "" ||
		cj.IssuedAt <= 0 || cj.ExpiresAt <= cj.IssuedAt || cj.AuthTime <= 0 || cj.AuthTime > cj.IssuedAt ||
		exp.Sub(iat) > v.maxTTL || !validValues(cj.AMR, true) || !validValues(cj.Scopes, false) {
		return nil, ErrMalformed
	}
	claims := claimsFromJSON(cj)
	for _, scope := range v.requiredScopes {
		if !claims.HasScope(scope) {
			return nil, fmt.Errorf("%w: %q", ErrMissingScope, scope)
		}
	}
	if v.checker != nil {
		checked := *claims
		checked.AMR = append([]string(nil), claims.AMR...)
		checked.Scopes = append([]string(nil), claims.Scopes...)
		if err := v.checker.CheckSession(ctx, checked); err != nil {
			return nil, ErrSessionRejected
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !v.clock().UTC().Before(exp.Add(v.leeway)) {
		return nil, ErrExpired
	}
	return claims, nil
}
