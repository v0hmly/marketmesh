package sessionassert

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"
)

// DefaultMaxTTL — максимальная длительность утверждения по умолчанию.
const DefaultMaxTTL = 5 * time.Minute

// assertionHeader — фиксированный заголовок JWS. Порядок полей структуры
// задаёт стабильную JSON-сериализацию.
type assertionHeader struct {
	Alg string `json:"alg"`
	Typ string `json:"typ"`
	Kid string `json:"kid"`
}

// Issuer подписывает утверждения о сессии закрытым ключом Ed25519.
// Закрытый ключ есть только у сервиса аутентификации.
type Issuer struct {
	key    ed25519.PrivateKey
	kid    string
	issuer string
	maxTTL time.Duration
}

// IssuerOption настраивает Issuer.
type IssuerOption func(*Issuer)

// WithMaxTTL задаёт максимальную допустимую длительность утверждения.
func WithMaxTTL(d time.Duration) IssuerOption {
	return func(i *Issuer) { i.maxTTL = d }
}

// NewIssuer создаёт издателя утверждений: закрытый ключ Ed25519,
// идентификатор ключа kid и имя издателя (iss).
func NewIssuer(key ed25519.PrivateKey, kid, issuer string, opts ...IssuerOption) (*Issuer, error) {
	if len(key) != ed25519.PrivateKeySize {
		return nil, fmt.Errorf("%w: invalid private key size %d", ErrInvalidParams, len(key))
	}
	if kid == "" {
		return nil, fmt.Errorf("%w: empty kid", ErrInvalidParams)
	}
	if issuer == "" {
		return nil, fmt.Errorf("%w: empty issuer", ErrInvalidParams)
	}
	i := &Issuer{key: key, kid: kid, issuer: issuer, maxTTL: DefaultMaxTTL}
	for _, opt := range opts {
		opt(i)
	}
	if i.maxTTL <= 0 {
		return nil, fmt.Errorf("%w: non-positive max TTL", ErrInvalidParams)
	}
	return i, nil
}

// IssueParams — параметры выпуска утверждения.
type IssueParams struct {
	Audience  string        // aud — единственная целевая аудитория
	Subject   string        // sub — неизменяемый идентификатор субъекта
	SessionID string        // sid — идентификатор сессии
	TTL       time.Duration // время жизни; 0 — использовать MaxTTL издателя
	AuthTime  time.Time     // auth_time — время первичной аутентификации
	ACR       string        // acr — уровень аутентификации
	AMR       []string      // amr — методы аутентификации
	Scopes    []string      // scope — ограниченные области действия
	Actor     string        // act — субъект делегирования, опционально
}

// Issue собирает claims, валидирует их и возвращает подписанное утверждение
// в формате JWS compact serialization.
func (i *Issuer) Issue(p IssueParams) (string, error) {
	if p.Audience == "" {
		return "", fmt.Errorf("%w: empty audience", ErrInvalidParams)
	}
	if p.Subject == "" {
		return "", fmt.Errorf("%w: empty subject", ErrInvalidParams)
	}
	if p.SessionID == "" {
		return "", fmt.Errorf("%w: empty session id", ErrInvalidParams)
	}
	if p.AuthTime.IsZero() {
		return "", fmt.Errorf("%w: zero auth time", ErrInvalidParams)
	}
	ttl := p.TTL
	if ttl == 0 {
		ttl = i.maxTTL
	}
	if ttl < 0 || ttl > i.maxTTL {
		return "", fmt.Errorf("%w: TTL %s exceeds max %s", ErrInvalidParams, ttl, i.maxTTL)
	}
	seen := make(map[string]struct{}, len(p.Scopes))
	for _, s := range p.Scopes {
		if s == "" {
			return "", fmt.Errorf("%w: empty scope", ErrInvalidParams)
		}
		if _, dup := seen[s]; dup {
			return "", fmt.Errorf("%w: duplicate scope %q", ErrInvalidParams, s)
		}
		seen[s] = struct{}{}
	}

	now := time.Now().UTC().Truncate(time.Second)
	claims := &Claims{
		Issuer:    i.issuer,
		Audience:  p.Audience,
		Subject:   p.Subject,
		SessionID: p.SessionID,
		IssuedAt:  now,
		ExpiresAt: now.Add(ttl),
		ID:        newAssertionID(),
		AuthTime:  p.AuthTime.UTC().Truncate(time.Second),
		ACR:       p.ACR,
		AMR:       p.AMR,
		Scopes:    p.Scopes,
		Actor:     p.Actor,
	}

	header, err := json.Marshal(assertionHeader{Alg: "EdDSA", Typ: TokenType, Kid: i.kid})
	if err != nil {
		return "", fmt.Errorf("sessionassert: marshal header: %w", err)
	}
	payload, err := json.Marshal(claims.toJSON())
	if err != nil {
		return "", fmt.Errorf("sessionassert: marshal claims: %w", err)
	}
	return signCompact(i.key, header, payload), nil
}

// signCompact подписывает готовые JSON-сегменты и собирает
// compact-представление header.payload.signature в base64url без padding.
func signCompact(key ed25519.PrivateKey, header, payload []byte) string {
	enc := base64.RawURLEncoding
	input := enc.EncodeToString(header) + "." + enc.EncodeToString(payload)
	sig := ed25519.Sign(key, []byte(input))
	return input + "." + enc.EncodeToString(sig)
}

// newAssertionID генерирует криптографически случайный идентификатор jti.
func newAssertionID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("sessionassert: read random: %v", err))
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}
