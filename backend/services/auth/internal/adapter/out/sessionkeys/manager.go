// Package sessionkeys loads scheduled assertion signing keys from an atomic file snapshot.
package sessionkeys

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/v0hmly/marketmesh/platform/sessionassert"
	domain "github.com/v0hmly/marketmesh/services/auth/internal/domain/session"
)

const maxFileBytes = 64 * 1024

// Config is server-owned policy. Clock defaults to time.Now.
type Config struct {
	Path      string
	Issuer    string
	MaxTTL    time.Duration
	Clock     func() time.Time
	Audiences map[string][]string
}

// PublicKey is a public-only JWK, with an exclusive trust deadline.
type PublicKey struct {
	Kty         string    `json:"kty"`
	Crv         string    `json:"crv"`
	Alg         string    `json:"alg"`
	Use         string    `json:"use"`
	Kid         string    `json:"kid"`
	X           string    `json:"x"`
	VerifyUntil time.Time `json:"verify_until"`
}

type keyFile struct {
	Keys []fileKey `json:"keys"`
}
type fileKey struct {
	Kid         string    `json:"kid"`
	PrivateKey  string    `json:"private_key,omitempty"`
	PublicKey   string    `json:"public_key"`
	SignFrom    time.Time `json:"sign_from"`
	SignUntil   time.Time `json:"sign_until"`
	VerifyUntil time.Time `json:"verify_until"`
}
type loadedKey struct {
	spec    fileKey
	private ed25519.PrivateKey
	public  ed25519.PublicKey
}
type rememberedKey struct {
	fingerprint [32]byte
	verifyUntil time.Time
}

// Manager rereads and validates the whole file for each operation; there is no stale fallback.
// It remembers public fingerprints for its lifetime to prohibit kid reuse.
type Manager struct {
	mu     sync.Mutex
	config Config
	seen   map[string]rememberedKey
}

// New validates policy and the initial key snapshot without changing any files.
func New(config Config) (*Manager, error) {
	if !filepath.IsAbs(config.Path) || !validLabel(config.Issuer) || config.MaxTTL < time.Second || len(config.Audiences) == 0 {
		return nil, domain.ErrUnavailable
	}
	if config.Clock == nil {
		config.Clock = time.Now
	}
	audiences := make(map[string][]string, len(config.Audiences))
	for audience, scopes := range config.Audiences {
		if !validLabel(audience) {
			return nil, domain.ErrUnavailable
		}
		seen := map[string]bool{}
		for _, scope := range scopes {
			if !validLabel(scope) || seen[scope] {
				return nil, domain.ErrUnavailable
			}
			seen[scope] = true
		}
		audiences[audience] = append([]string(nil), scopes...)
	}
	config.Audiences = audiences
	m := &Manager{config: config, seen: map[string]rememberedKey{}}
	keys, err := m.load(config.Clock().UTC())
	clearKeys(keys)
	if err != nil {
		return nil, err
	}
	return m, nil
}

// Issue signs only server-selected scopes for the configured audience.
func (m *Manager) Issue(ctx context.Context, record domain.Record, audience string, expiresAt time.Time) (string, error) {
	if ctx == nil {
		return "", domain.ErrInvalidSession
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.config.Clock().UTC()
	scopes, allowed := m.config.Audiences[audience]
	if !allowed || !expiresAt.Equal(expiresAt.Truncate(time.Second)) {
		return "", domain.ErrInvalidSession
	}
	keys, err := m.load(now)
	defer clearKeys(keys)
	if err != nil {
		return "", err
	}
	// Reading the secret may take time. Recheck session bounds against the actual
	// signing clock, independently of the application caller's earlier snapshot.
	now = m.config.Clock().UTC()
	if !record.Active(now) || !now.Before(record.AccessExpiresAt) {
		return "", domain.ErrInvalidSession
	}
	// The absolute, whole-second deadline is returned unchanged by application.
	// Never silently shorten it: the response and signed exp must agree exactly.
	issuedAt := now.Truncate(time.Second)
	ttl := expiresAt.Sub(issuedAt)
	if ttl < time.Second || ttl > m.config.MaxTTL || !now.Before(expiresAt) ||
		expiresAt.After(record.AccessExpiresAt) || expiresAt.After(record.ExpiresAt) {
		return "", domain.ErrInvalidSession
	}
	for _, key := range keys {
		if len(key.private) == 0 || now.Before(key.spec.SignFrom) || !now.Before(key.spec.SignUntil) {
			continue
		}
		issuer, err := sessionassert.NewIssuer(key.private, key.spec.Kid, m.config.Issuer,
			sessionassert.WithMaxTTL(m.config.MaxTTL), sessionassert.WithIssuerClock(func() time.Time { return now }))
		if err != nil {
			return "", domain.ErrUnavailable
		}
		token, err := issuer.Issue(sessionassert.IssueParams{
			Audience: audience, Subject: base64.RawURLEncoding.EncodeToString(record.SubjectID[:]), SessionID: record.ID.String(),
			TTL: ttl, AuthTime: record.CreatedAt, ACR: "urn:marketmesh:auth:password", AMR: []string{"pwd"}, Scopes: scopes,
		})
		if err != nil {
			return "", domain.ErrInvalidSession
		}
		if err := ctx.Err(); err != nil {
			return "", err
		}
		finishedAt := m.config.Clock().UTC()
		if !finishedAt.Before(expiresAt) || !finishedAt.Before(key.spec.SignUntil) {
			return "", domain.ErrInvalidSession
		}
		return token, nil
	}
	return "", domain.ErrUnavailable
}

// PublicKeys publishes current and prepublished future keys, never expired ones.
func (m *Manager) PublicKeys() ([]PublicKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.config.Clock().UTC()
	keys, err := m.load(now)
	defer clearKeys(keys)
	if err != nil {
		return nil, err
	}
	result := make([]PublicKey, 0, len(keys))
	for _, key := range keys {
		if now.Before(key.spec.VerifyUntil) {
			result = append(result, PublicKey{Kty: "OKP", Crv: "Ed25519", Alg: "EdDSA", Use: "sig", Kid: key.spec.Kid, X: base64.RawURLEncoding.EncodeToString(key.public), VerifyUntil: key.spec.VerifyUntil})
		}
	}
	return result, nil
}

// Key returns a defensive copy; unknown, expired or invalid snapshots fail closed.
func (m *Manager) Key(kid string) (ed25519.PublicKey, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.config.Clock().UTC()
	keys, err := m.load(now)
	defer clearKeys(keys)
	if err != nil {
		return nil, false
	}
	for _, key := range keys {
		if key.spec.Kid == kid && now.Before(key.spec.VerifyUntil) {
			return append(ed25519.PublicKey(nil), key.public...), true
		}
	}
	return nil, false
}

type keySource struct{ manager *Manager }

func (s keySource) Key(kid string) (ed25519.PublicKey, error) {
	key, ok := s.manager.Key(kid)
	if !ok {
		return nil, sessionassert.ErrUnknownKeyID
	}
	return key, nil
}

// KeySource adapts the bool lookup API to the platform verifier interface.
func (m *Manager) KeySource() sessionassert.KeySource { return keySource{manager: m} }

// load is called while holding mu (or before New returns).
func (m *Manager) load(now time.Time) ([]loadedKey, error) {
	file, err := os.Open(m.config.Path)
	if err != nil {
		return nil, domain.ErrUnavailable
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxFileBytes {
		return nil, domain.ErrUnavailable
	}
	data, err := io.ReadAll(io.LimitReader(file, maxFileBytes+1))
	if err != nil || len(data) > maxFileBytes {
		clear(data)
		return nil, domain.ErrUnavailable
	}
	defer clear(data)
	var document keyFile
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&document) != nil || len(document.Keys) == 0 || len(document.Keys) > 64 {
		return nil, domain.ErrUnavailable
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return nil, domain.ErrUnavailable
	}
	keys := make([]loadedKey, 0, len(document.Keys))
	success := false
	defer func() {
		if !success {
			clearKeys(keys)
		}
	}()
	present := map[string]bool{}
	active := 0
	for _, spec := range document.Keys {
		if !validLabel(spec.Kid) || present[spec.Kid] || spec.SignFrom.Unix() <= 0 || !spec.SignUntil.After(spec.SignFrom) || spec.VerifyUntil.Before(spec.SignUntil.Add(m.config.MaxTTL)) {
			return nil, domain.ErrUnavailable
		}
		present[spec.Kid] = true
		public, err := base64.RawURLEncoding.Strict().DecodeString(spec.PublicKey)
		if err != nil || len(public) != ed25519.PublicKeySize || base64.RawURLEncoding.EncodeToString(public) != spec.PublicKey {
			return nil, domain.ErrUnavailable
		}
		key := loadedKey{spec: spec, public: public}
		if spec.PrivateKey != "" {
			private, err := base64.RawURLEncoding.Strict().DecodeString(spec.PrivateKey)
			if err != nil || len(private) != ed25519.PrivateKeySize || base64.RawURLEncoding.EncodeToString(private) != spec.PrivateKey {
				clear(private)
				return nil, domain.ErrUnavailable
			}
			canonical := ed25519.NewKeyFromSeed(private[:ed25519.SeedSize])
			matches := bytes.Equal(canonical, private) && bytes.Equal(canonical[ed25519.SeedSize:], public)
			clear(canonical)
			if !matches {
				clear(private)
				return nil, domain.ErrUnavailable
			}
			key.private = private
		} else if now.Before(spec.SignUntil) {
			return nil, domain.ErrUnavailable
		}
		// Do not retain the base64 private-key string in the loaded snapshot.
		key.spec.PrivateKey = ""
		keys = append(keys, key)
		if !now.Before(spec.SignFrom) && now.Before(spec.SignUntil) {
			active++
		}
		fingerprint := sha256.Sum256(public)
		if previous, ok := m.seen[spec.Kid]; ok && (previous.fingerprint != fingerprint || spec.VerifyUntil.Before(previous.verifyUntil)) {
			return nil, domain.ErrUnavailable
		}
	}
	if active != 1 {
		return nil, domain.ErrUnavailable
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].spec.SignFrom.Before(keys[j].spec.SignFrom) })
	for i := 1; i < len(keys); i++ {
		if keys[i].spec.SignFrom.Before(keys[i-1].spec.SignUntil) {
			return nil, domain.ErrUnavailable
		}
	}
	for kid, previous := range m.seen {
		if !present[kid] && now.Before(previous.verifyUntil) {
			return nil, domain.ErrUnavailable
		}
	}
	for _, key := range keys {
		m.seen[key.spec.Kid] = rememberedKey{fingerprint: sha256.Sum256(key.public), verifyUntil: key.spec.VerifyUntil}
	}
	success = true
	return keys, nil
}

func clearKeys(keys []loadedKey) {
	for i := range keys {
		clear(keys[i].private)
	}
}
func validLabel(value string) bool {
	return value != "" && len(value) <= 256 && strings.TrimSpace(value) == value && !strings.ContainsAny(value, "\r\n\x00")
}
