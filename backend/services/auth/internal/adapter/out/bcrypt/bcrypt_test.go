package bcrypt_test

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	passwordbcrypt "github.com/v0hmly/marketmesh/services/auth/internal/adapter/out/bcrypt"
	"github.com/v0hmly/marketmesh/services/auth/internal/domain/credential"
	cryptobcrypt "golang.org/x/crypto/bcrypt"
)

func TestHashUsesRandomSaltAndInteroperatesWithBcrypt(t *testing.T) {
	t.Parallel()
	hasher := newHasher(t, 10)
	password := []byte("пароль🔒пароль")
	original := bytes.Clone(password)
	first, err := hasher.Hash(password)
	if err != nil {
		t.Fatal(err)
	}
	second, err := hasher.Hash(password)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("Hash reused a salt")
	}
	if !bytes.Equal(password, original) {
		t.Fatal("Hash modified the caller's password")
	}
	if cost, err := cryptobcrypt.Cost([]byte(first.String())); err != nil || cost != 10 {
		t.Fatalf("stored cost = %d, error = %v", cost, err)
	}
	if err := cryptobcrypt.CompareHashAndPassword([]byte(first.String()), password); err != nil {
		t.Fatalf("bcrypt interoperability: %v", err)
	}
	valid, upgrade, err := hasher.Verify(password, first)
	if err != nil || !valid || upgrade {
		t.Fatalf("Verify = %v, %v, %v", valid, upgrade, err)
	}
	valid, upgrade, err = hasher.Verify([]byte("incorrect password"), first)
	if err != nil || valid || upgrade {
		t.Fatalf("Verify(wrong) = %v, %v, %v", valid, upgrade, err)
	}
	if err := hasher.EqualizeMissing(password); err != nil {
		t.Fatalf("EqualizeMissing: %v", err)
	}
}

func TestVerifyUpgradesCostWithoutDowngrading(t *testing.T) {
	t.Parallel()
	hasher := newHasher(t, 10)
	password := []byte("eight123")
	for _, cost := range []int{9, 10, 11} {
		t.Run(fmt.Sprintf("cost_%d", cost), func(t *testing.T) {
			encoded, err := cryptobcrypt.GenerateFromPassword(password, cost)
			if err != nil {
				t.Fatal(err)
			}
			valid, upgrade, err := hasher.Verify(password, digest(t, string(encoded)))
			if err != nil || !valid || upgrade != (cost < 10) {
				t.Fatalf("Verify = %v, %v, %v", valid, upgrade, err)
			}
		})
	}
}

func TestBcryptNeverTruncatesLongPasswords(t *testing.T) {
	t.Parallel()
	hasher := newHasher(t, 10)
	password := []byte(strings.Repeat("я", 36)) // 36 code points, exactly 72 bytes.
	encoded, err := hasher.Hash(password)
	if err != nil {
		t.Fatal(err)
	}
	valid, _, err := hasher.Verify(password, encoded)
	if err != nil || !valid {
		t.Fatalf("Verify(72 bytes) = %v, %v", valid, err)
	}
	for _, test := range []struct {
		name     string
		password []byte
	}{
		{name: "empty"},
		{name: "same prefix plus suffix", password: append(bytes.Clone(password), 'x')},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := hasher.Hash(test.password); !errors.Is(err, credential.ErrInvalidPassword) {
				t.Fatalf("Hash error = %v", err)
			}
			if valid, _, err := hasher.Verify(test.password, encoded); valid || !errors.Is(err, credential.ErrInvalidPassword) {
				t.Fatalf("Verify = %v, %v", valid, err)
			}
		})
	}
}

func TestBcryptRejectsNULInsteadOfAcceptingEquivalentPasswords(t *testing.T) {
	t.Parallel()
	hasher := newHasher(t, 10)
	encoded, err := hasher.Hash([]byte("12345678"))
	if err != nil {
		t.Fatal(err)
	}
	for _, password := range []string{"12345678\x0012345678", strings.Repeat("\x00", 8)} {
		if _, err := hasher.Hash([]byte(password)); !errors.Is(err, credential.ErrInvalidPassword) {
			t.Fatalf("Hash error = %v", err)
		}
		if valid, upgrade, err := hasher.Verify([]byte(password), encoded); valid || upgrade || !errors.Is(err, credential.ErrInvalidPassword) {
			t.Fatalf("Verify = %v, %v, %v", valid, upgrade, err)
		}
	}
}

func TestVerifyRejectsInvalidDigestsAndExcessiveCost(t *testing.T) {
	t.Parallel()
	hasher := newHasher(t, 10)
	for _, test := range []struct {
		name    string
		encoded string
	}{
		{name: "unsupported", encoded: "$unsupported$private-digest"},
		{name: "truncated", encoded: "$2a$10$short"},
		{name: "excessive cost", encoded: "$2a$31$" + strings.Repeat(".", 53)},
		{name: "invalid cost", encoded: "$2a$xx$" + strings.Repeat(".", 53)},
		{name: "invalid encoding", encoded: "$2a$10$" + strings.Repeat("!", 53)},
		{name: "trailing data", encoded: "$2a$10$" + strings.Repeat(".", 54)},
	} {
		t.Run(test.name, func(t *testing.T) {
			valid, upgrade, err := hasher.Verify([]byte("private-password"), digest(t, test.encoded))
			if err == nil || valid || upgrade {
				t.Fatalf("Verify = %v, %v, %v", valid, upgrade, err)
			}
			if strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), test.encoded) {
				t.Fatal("verification error leaked input")
			}
		})
	}
}

func TestNewRejectsUnsafeCosts(t *testing.T) {
	t.Parallel()
	for _, cost := range []int{-1, 0, 4, 9, 15, 31, 32} {
		t.Run(fmt.Sprintf("cost_%d", cost), func(t *testing.T) {
			if _, err := passwordbcrypt.New(cost); err == nil {
				t.Fatal("New accepted an unsafe cost")
			}
		})
	}
}

func TestUninitializedHasherReturnsErrors(t *testing.T) {
	t.Parallel()
	for _, hasher := range []*passwordbcrypt.Hasher{nil, {}} {
		if _, err := hasher.Hash([]byte("password")); err == nil {
			t.Fatal("Hash accepted an uninitialized hasher")
		}
		if _, _, err := hasher.Verify([]byte("password"), digest(t, "encoded")); err == nil {
			t.Fatal("Verify accepted an uninitialized hasher")
		}
		if err := hasher.EqualizeMissing([]byte("password")); err == nil {
			t.Fatal("EqualizeMissing accepted an uninitialized hasher")
		}
	}
}

func BenchmarkHash(b *testing.B) {
	for _, cost := range []int{10, passwordbcrypt.DefaultCost} {
		b.Run(fmt.Sprintf("cost_%d", cost), func(b *testing.B) {
			hasher, err := passwordbcrypt.New(cost)
			if err != nil {
				b.Fatal(err)
			}
			password := []byte("diagnostic-password")
			b.ReportAllocs()
			for b.Loop() {
				if _, err := hasher.Hash(password); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func newHasher(t *testing.T, cost int) *passwordbcrypt.Hasher {
	t.Helper()
	hasher, err := passwordbcrypt.New(cost)
	if err != nil {
		t.Fatal(err)
	}
	return hasher
}

func digest(t *testing.T, encoded string) credential.PasswordDigest {
	t.Helper()
	value, err := credential.NewPasswordDigest(encoded)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
