// Package security defines Auth's email and account-security transitions.
package security

import (
	"errors"
	"net/mail"
	"strings"
	"time"
	"unicode"

	"github.com/v0hmly/marketmesh/services/auth/internal/domain/credential"
)

type ID [16]byte
type Digest [32]byte
type Purpose string

const (
	LoginCode         Purpose = "login"
	VerifyEmail       Purpose = "verify"
	ResetPassword     Purpose = "reset"
	ChangeEmail       Purpose = "change_email"
	CancelEmailChange Purpose = "cancel_email"
	CancelDeletion    Purpose = "cancel_deletion"
	EnableCode        Purpose = "enable_code"
	DisableCode       Purpose = "disable_code"
)

// Failure is a bounded reason; it never contains user input or secrets.
type Failure string

func (e Failure) Error() string { return "auth security: " + string(e) }

const (
	InvalidInput       Failure = "INVALID_INPUT"
	InvalidCredentials Failure = "INVALID_CREDENTIALS"
	LoginLocked        Failure = "LOGIN_LOCKED"
	CodeMismatch       Failure = "CODE_MISMATCH"
	CodeReissued       Failure = "CODE_REISSUED"
	CodeExpired        Failure = "CODE_EXPIRED"
	TokenExpired       Failure = "TOKEN_EXPIRED"
	TokenUsed          Failure = "TOKEN_USED"
	EmailUnverified    Failure = "EMAIL_UNVERIFIED"
	CodeRequired       Failure = "CODE_REQUIRED"
	RateLimited        Failure = "RATE_LIMITED"
	NewDeviceCooldown  Failure = "NEW_DEVICE_COOLDOWN"
	Unavailable        Failure = "UNAVAILABLE"
	NotFound           Failure = "NOT_FOUND"
)

// Account is locked with its credential during every security transition.
// Revision invalidates all outstanding challenges on a credential change.
type Account struct {
	Subject               credential.SubjectID
	Email                 string
	PasswordDigest        credential.PasswordDigest
	Verified, CodeEnabled bool
	Revision              int64
	DeletionAt            *time.Time
	DeletedAt             *time.Time
}

type Challenge struct {
	ID      ID
	Subject credential.SubjectID
	Purpose Purpose
	Digest  Digest
	// RegistrationDigest binds verification to the browser that created the account.
	// Zero means confirmation-only, including challenges created by older versions.
	RegistrationDigest Digest
	Revision           int64
	Email              string
	ExpiresAt, SentAt  time.Time
	Attempts, Sends    int
	UsedAt             *time.Time
}

func (c Challenge) Check(account Account, purpose Purpose, now time.Time) error {
	if c.ID == (ID{}) || c.Subject != account.Subject || c.Purpose != purpose || c.Revision != account.Revision || account.DeletedAt != nil {
		return TokenExpired
	}
	if c.UsedAt != nil {
		return TokenUsed
	}
	if !now.Before(c.ExpiresAt) {
		return TokenExpired
	}
	return nil
}

// Email accepts a single ASCII addr-spec. Display names, comments, SMTPUTF8,
// control characters, and ambiguous quoted local parts are not supported.
func Email(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) < 3 || len(value) > 254 || strings.Count(value, "@") != 1 || strings.IndexFunc(value, func(r rune) bool { return r < 33 || r > 126 || strings.ContainsRune("<>\"\\(),;:", r) }) >= 0 {
		return "", InvalidInput
	}
	a, err := mail.ParseAddress(value)
	if err != nil || a.Name != "" || a.Address != value {
		return "", InvalidInput
	}
	return value, nil
}

// NewPassword applies the product's complexity rule only to new credentials.
// Existing passwords remain verifiable without imposing new composition rules.
func NewPassword(value []byte) (credential.Password, error) {
	password, err := credential.NewPassword(value)
	if err != nil {
		return credential.Password{}, InvalidInput
	}
	var lower, upper, digit, special bool
	for _, r := range string(value) {
		lower = lower || r >= 'a' && r <= 'z'
		upper = upper || r >= 'A' && r <= 'Z'
		digit = digit || r >= '0' && r <= '9'
		special = special || !unicode.IsLetter(r) && !unicode.IsDigit(r) && !unicode.IsSpace(r) && !unicode.IsControl(r)
	}
	if !lower || !upper || !digit || !special {
		password.Destroy()
		return credential.Password{}, InvalidInput
	}
	return password, nil
}

func IsExpected(err error) bool {
	var failure Failure
	return errors.As(err, &failure) && failure != Unavailable
}
