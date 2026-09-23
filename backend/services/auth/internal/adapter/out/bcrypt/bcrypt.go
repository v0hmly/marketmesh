// Package bcrypt implements password hashing and bounded bcrypt verification.
package bcrypt

import (
	"bytes"
	"crypto/rand"
	"errors"
	"strings"

	"github.com/v0hmly/marketmesh/services/auth/internal/domain/credential"
	cryptobcrypt "golang.org/x/crypto/bcrypt"
)

const (
	// DefaultCost balances login latency and offline guessing cost.
	DefaultCost = 12
	// MinCost is the lowest permitted cost for newly generated digests.
	MinCost = 10
	// MaxCost bounds CPU work, including verification of stored digests.
	MaxCost          = 14
	maxPasswordBytes = 72
)

var errInvalidDigest = errors.New("bcrypt: invalid password digest")

// Hasher owns the current cost and a process-local dummy digest.
type Hasher struct {
	cost        int
	dummyDigest credential.PasswordDigest
}

// New constructs a hasher and prepares a random dummy digest for unknown identifiers.
func New(cost int) (*Hasher, error) {
	if cost < MinCost || cost > MaxCost {
		return nil, errors.New("bcrypt: cost must be between 10 and 14")
	}
	hasher := &Hasher{cost: cost}
	password := []byte(rand.Text())
	defer clear(password)
	digest, err := hasher.Hash(password)
	if err != nil {
		return nil, err
	}
	hasher.dummyDigest = digest
	return hasher, nil
}

// Hash generates a bcrypt digest with a fresh cryptographically random salt.
func (hasher *Hasher) Hash(password []byte) (credential.PasswordDigest, error) {
	if hasher == nil || hasher.cost < MinCost || hasher.cost > MaxCost {
		return credential.PasswordDigest{}, errors.New("bcrypt: uninitialized hasher")
	}
	if len(password) == 0 || len(password) > maxPasswordBytes || bytes.IndexByte(password, 0) >= 0 {
		return credential.PasswordDigest{}, credential.ErrInvalidPassword
	}
	encoded, err := cryptobcrypt.GenerateFromPassword(password, hasher.cost)
	if err != nil {
		return credential.PasswordDigest{}, errors.New("bcrypt: generating password digest")
	}
	defer clear(encoded)
	return credential.NewPasswordDigest(string(encoded))
}

// Verify checks the whole password and requests an upgrade only for a lower cost.
func (hasher *Hasher) Verify(password []byte, digest credential.PasswordDigest) (bool, bool, error) {
	if hasher == nil || hasher.cost < MinCost || hasher.cost > MaxCost {
		return false, false, errors.New("bcrypt: uninitialized hasher")
	}
	// CompareHashAndPassword does not enforce bcrypt's input limit itself.
	// NUL can make distinct passwords equivalent during bcrypt key expansion.
	if len(password) == 0 || len(password) > maxPasswordBytes || bytes.IndexByte(password, 0) >= 0 {
		return false, false, credential.ErrInvalidPassword
	}
	encoded := digest.String()
	if len(encoded) != 60 || (!strings.HasPrefix(encoded, "$2a$") && !strings.HasPrefix(encoded, "$2b$") && !strings.HasPrefix(encoded, "$2y$")) {
		return false, false, errInvalidDigest
	}
	cost, err := cryptobcrypt.Cost([]byte(encoded))
	if err != nil || cost > MaxCost {
		return false, false, errInvalidDigest
	}
	err = cryptobcrypt.CompareHashAndPassword([]byte(encoded), password)
	if errors.Is(err, cryptobcrypt.ErrMismatchedHashAndPassword) {
		return false, false, nil
	}
	if err != nil {
		return false, false, errInvalidDigest
	}
	return true, cost < hasher.cost, nil
}

// EqualizeMissing performs a complete bcrypt verification for an unknown identifier.
func (hasher *Hasher) EqualizeMissing(password []byte) error {
	if hasher == nil {
		return errors.New("bcrypt: uninitialized hasher")
	}
	_, _, err := hasher.Verify(password, hasher.dummyDigest)
	return err
}
