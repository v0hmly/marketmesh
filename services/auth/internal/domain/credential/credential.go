// Package credential contains the pure credential domain model.
package credential

import (
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	minIdentifierBytes = 3
	maxIdentifierBytes = 254
	minPasswordBytes   = 12
	maxPasswordBytes   = 1024
	maxDigestBytes     = 512
	SubjectIDBytes     = 16
)

var (
	// ErrInvalidIdentifier reports a structurally invalid identifier without retaining it.
	ErrInvalidIdentifier = errors.New("credential: invalid identifier")
	// ErrInvalidPassword reports a structurally invalid password without retaining it.
	ErrInvalidPassword = errors.New("credential: invalid password")
	// ErrInvalidSubjectID reports an identifier with an unexpected binary size.
	ErrInvalidSubjectID = errors.New("credential: invalid subject ID")
	// ErrInvalidPasswordDigest reports an empty or unreasonably large encoded digest.
	ErrInvalidPasswordDigest = errors.New("credential: invalid password digest")
)

// Identifier is a normalized, case-insensitive credential identifier.
type Identifier struct {
	value string
}

// NewIdentifier validates and normalizes an identifier.
func NewIdentifier(value string) (Identifier, error) {
	if !utf8.ValidString(value) {
		return Identifier{}, ErrInvalidIdentifier
	}
	normalized := strings.ToLower(strings.TrimSpace(value))
	if len(normalized) < minIdentifierBytes || len(normalized) > maxIdentifierBytes {
		return Identifier{}, ErrInvalidIdentifier
	}
	for _, character := range normalized {
		if unicode.IsControl(character) || unicode.IsSpace(character) {
			return Identifier{}, ErrInvalidIdentifier
		}
	}

	return Identifier{value: normalized}, nil
}

// String returns the normalized value for persistence and comparison only.
func (identifier Identifier) String() string {
	return identifier.value
}

// Password owns a defensive copy of a validated raw password.
type Password struct {
	value []byte
}

// NewPassword validates a raw password and makes a defensive copy.
func NewPassword(value []byte) (Password, error) {
	if len(value) < minPasswordBytes || len(value) > maxPasswordBytes || !utf8.Valid(value) {
		return Password{}, ErrInvalidPassword
	}

	return Password{value: bytesClone(value)}, nil
}

// Bytes returns a defensive copy that the caller should clear after use.
func (password Password) Bytes() []byte {
	return bytesClone(password.value)
}

// Destroy clears the domain-owned password buffer.
func (password *Password) Destroy() {
	if password == nil {
		return
	}
	clear(password.value)
	password.value = nil
}

// SubjectID is an opaque 128-bit identity identifier.
type SubjectID [SubjectIDBytes]byte

// NewSubjectID validates and copies a binary subject identifier.
func NewSubjectID(value []byte) (SubjectID, error) {
	if len(value) != SubjectIDBytes {
		return SubjectID{}, ErrInvalidSubjectID
	}
	var subjectID SubjectID
	copy(subjectID[:], value)

	return subjectID, nil
}

// Bytes returns a defensive copy suitable for transport or persistence.
func (subjectID SubjectID) Bytes() []byte {
	return bytesClone(subjectID[:])
}

// PasswordDigest is a versioned encoded password digest.
type PasswordDigest struct {
	value string
}

// NewPasswordDigest validates an encoded digest without interpreting its format.
func NewPasswordDigest(value string) (PasswordDigest, error) {
	if value == "" || len(value) > maxDigestBytes {
		return PasswordDigest{}, ErrInvalidPasswordDigest
	}

	return PasswordDigest{value: value}, nil
}

// String returns the encoded digest for a hashing adapter or persistence.
func (digest PasswordDigest) String() string {
	return digest.value
}

// Credential associates one normalized identifier with a subject and digest.
type Credential struct {
	subjectID      SubjectID
	identifier     Identifier
	passwordDigest PasswordDigest
}

// New creates a credential aggregate from validated values.
func New(subjectID SubjectID, identifier Identifier, digest PasswordDigest) Credential {
	return Credential{subjectID: subjectID, identifier: identifier, passwordDigest: digest}
}

// SubjectID returns the opaque subject identifier.
func (credential Credential) SubjectID() SubjectID {
	return credential.subjectID
}

// Identifier returns the normalized identifier.
func (credential Credential) Identifier() Identifier {
	return credential.identifier
}

// PasswordDigest returns the encoded digest.
func (credential Credential) PasswordDigest() PasswordDigest {
	return credential.passwordDigest
}

func bytesClone(value []byte) []byte {
	return append([]byte(nil), value...)
}
