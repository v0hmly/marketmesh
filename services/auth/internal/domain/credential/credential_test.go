package credential_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/v0hmly/marketmesh/services/auth/internal/domain/credential"
)

func TestIdentifierNormalizesAndRejectsUnsafeValues(t *testing.T) {
	t.Parallel()

	identifier, err := credential.NewIdentifier("  Alice@example.COM ")
	if err != nil {
		t.Fatalf("NewIdentifier() error = %v", err)
	}
	if got := identifier.String(); got != "alice@example.com" {
		t.Fatalf("Identifier.String() = %q", got)
	}

	invalid := []string{"", "ab", "a b", "a\nb", strings.Repeat("a", 255), string([]byte{0xff, 0xfe, 0xfd})}
	for _, value := range invalid {
		value := value
		t.Run("invalid", func(t *testing.T) {
			t.Parallel()
			_, err := credential.NewIdentifier(value)
			if !errors.Is(err, credential.ErrInvalidIdentifier) {
				t.Fatalf("NewIdentifier() error = %v", err)
			}
		})
	}
}

func TestPasswordOwnsCopiesAndCanBeDestroyed(t *testing.T) {
	t.Parallel()

	raw := []byte("correct horse battery staple")
	password, err := credential.NewPassword(raw)
	if err != nil {
		t.Fatalf("NewPassword() error = %v", err)
	}
	raw[0] = 'X'
	first := password.Bytes()
	if string(first) != "correct horse battery staple" {
		t.Fatalf("Password.Bytes() = %q", first)
	}
	first[0] = 'Y'
	if string(password.Bytes()) != "correct horse battery staple" {
		t.Fatal("Password.Bytes() did not return a defensive copy")
	}
	password.Destroy()
	if got := password.Bytes(); len(got) != 0 {
		t.Fatalf("Password.Bytes() after Destroy = %q", got)
	}
}

func TestPasswordAndDigestBounds(t *testing.T) {
	t.Parallel()

	for _, value := range [][]byte{[]byte("short"), make([]byte, 1025), {0xff, 0xfe, 0xfd, 0xfc, 0xfb, 0xfa, 0xf9, 0xf8, 0xf7, 0xf6, 0xf5, 0xf4}} {
		_, err := credential.NewPassword(value)
		if !errors.Is(err, credential.ErrInvalidPassword) {
			t.Fatalf("NewPassword() error = %v", err)
		}
	}
	if _, err := credential.NewPassword([]byte("twelve-bytes!")); err != nil {
		t.Fatalf("NewPassword(valid) error = %v", err)
	}
	for _, value := range []string{"", strings.Repeat("x", 513)} {
		_, err := credential.NewPasswordDigest(value)
		if !errors.Is(err, credential.ErrInvalidPasswordDigest) {
			t.Fatalf("NewPasswordDigest() error = %v", err)
		}
	}
}

func TestSubjectIDCopiesAndValidates(t *testing.T) {
	t.Parallel()

	raw := make([]byte, credential.SubjectIDBytes)
	raw[0] = 42
	subjectID, err := credential.NewSubjectID(raw)
	if err != nil {
		t.Fatalf("NewSubjectID() error = %v", err)
	}
	raw[0] = 0
	copyValue := subjectID.Bytes()
	if copyValue[0] != 42 {
		t.Fatal("NewSubjectID() did not copy input")
	}
	copyValue[0] = 0
	if subjectID.Bytes()[0] != 42 {
		t.Fatal("SubjectID.Bytes() did not return a copy")
	}
	if _, err := credential.NewSubjectID(make([]byte, 15)); !errors.Is(err, credential.ErrInvalidSubjectID) {
		t.Fatalf("NewSubjectID(short) error = %v", err)
	}
}

func TestCredentialExposesValidatedValues(t *testing.T) {
	t.Parallel()

	identifier, err := credential.NewIdentifier("user@example.com")
	if err != nil {
		t.Fatalf("NewIdentifier() error = %v", err)
	}
	digest, err := credential.NewPasswordDigest("encoded-digest")
	if err != nil {
		t.Fatalf("NewPasswordDigest() error = %v", err)
	}
	var subjectID credential.SubjectID
	subjectID[0] = 3
	value := credential.New(subjectID, identifier, digest)
	if value.SubjectID() != subjectID || value.Identifier() != identifier || value.PasswordDigest() != digest || digest.String() != "encoded-digest" {
		t.Fatal("Credential getters did not preserve validated values")
	}
	var nilPassword *credential.Password
	nilPassword.Destroy()
}
