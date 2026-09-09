// Package session defines opaque session tokens and durable lifecycle metadata.
package session

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"strings"
	"time"

	"github.com/v0hmly/marketmesh/services/auth/internal/domain/credential"
)

var (
	ErrInvalidSession = errors.New("session: invalid session")
	ErrRefreshReuse   = errors.New("session: refresh token reused")
	ErrUnavailable    = errors.New("session: unavailable")
)

// ID identifies one session and its refresh-token family.
type ID [16]byte

func (id ID) String() string { return hex.EncodeToString(id[:]) }
func (id ID) Bytes() []byte  { return append([]byte(nil), id[:]...) }
func ParseID(value string) (ID, error) {
	var id ID
	if len(value) != 32 {
		return id, ErrInvalidSession
	}
	b, err := hex.DecodeString(value)
	if err != nil {
		return id, ErrInvalidSession
	}
	copy(id[:], b)
	if id == (ID{}) || id.String() != value {
		return ID{}, ErrInvalidSession
	}
	return id, nil
}

// Digest is the one-way representation persisted by adapters, never a bearer credential.
type Digest [sha256.Size]byte

func (digest Digest) String() string   { return "[REDACTED]" }
func (digest Digest) GoString() string { return digest.String() }

// Token is opaque outside Auth. String and GoString deliberately conceal it.
type Token struct {
	id    ID
	value string
}

func NewToken(id ID, random []byte) (Token, error) {
	if id == (ID{}) || len(random) != 32 {
		return Token{}, ErrInvalidSession
	}
	return Token{id: id, value: "mm1." + id.String() + "." + base64.RawURLEncoding.EncodeToString(random)}, nil
}
func ParseToken(value string) (Token, error) {
	if len(value) != 80 {
		return Token{}, ErrInvalidSession
	}
	parts := strings.Split(value, ".")
	if len(parts) != 3 || parts[0] != "mm1" {
		return Token{}, ErrInvalidSession
	}
	id, err := ParseID(parts[1])
	if err != nil {
		return Token{}, ErrInvalidSession
	}
	secret, err := base64.RawURLEncoding.Strict().DecodeString(parts[2])
	if err != nil || len(secret) != 32 {
		return Token{}, ErrInvalidSession
	}
	clear(secret)
	return Token{id: id, value: value}, nil
}
func (token Token) ID() ID           { return token.id }
func (token Token) Reveal() string   { return token.value }
func (token Token) Digest() Digest   { return sha256.Sum256([]byte(token.value)) }
func (token Token) String() string   { return "[REDACTED]" }
func (token Token) GoString() string { return token.String() }

// Record is canonical PostgreSQL session state. Version increases on refresh.
type Record struct {
	ID               ID
	SubjectID        credential.SubjectID
	Version          int64
	CreatedAt        time.Time
	AccessExpiresAt  time.Time
	RefreshExpiresAt time.Time
	ExpiresAt        time.Time
	RevokedAt        *time.Time
}

func (record Record) Active(now time.Time) bool {
	return record.ID != (ID{}) && record.SubjectID != (credential.SubjectID{}) && record.Version > 0 &&
		record.RevokedAt == nil && !now.Before(record.CreatedAt) && now.Before(record.ExpiresAt)
}

// Access is short-lived state stored only in the isolated Auth Redis.
type Access struct {
	Digest    Digest
	Version   int64
	ExpiresAt time.Time
}
