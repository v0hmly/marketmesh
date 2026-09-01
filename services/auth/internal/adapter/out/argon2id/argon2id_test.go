package argon2id

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/v0hmly/marketmesh/services/auth/internal/domain/credential"
)

func TestHashVerifyAndRandomSalt(t *testing.T) {
	t.Parallel()

	hasher, err := New(testConfig())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	password := []byte("correct horse battery staple")
	first, err := hasher.Hash(password)
	if err != nil {
		t.Fatalf("Hash() error = %v", err)
	}
	second, err := hasher.Hash(password)
	if err != nil {
		t.Fatalf("Hash() second error = %v", err)
	}
	if first == second {
		t.Fatal("Hash() reused salt")
	}
	valid, needsRehash, err := hasher.Verify(password, first)
	if err != nil || !valid || needsRehash {
		t.Fatalf("Verify() = valid %v, rehash %v, error %v", valid, needsRehash, err)
	}
	valid, _, err = hasher.Verify([]byte("incorrect password value"), first)
	if err != nil || valid {
		t.Fatalf("Verify(wrong) = valid %v, error %v", valid, err)
	}
}

func TestVerifyReportsRehashForPreviousParameters(t *testing.T) {
	t.Parallel()

	previousConfig := testConfig()
	previousConfig.Time = 1
	previous, err := New(previousConfig)
	if err != nil {
		t.Fatalf("New(previous) error = %v", err)
	}
	digest, err := previous.Hash([]byte("correct horse battery staple"))
	if err != nil {
		t.Fatalf("Hash() error = %v", err)
	}
	current, err := New(testConfig())
	if err != nil {
		t.Fatalf("New(current) error = %v", err)
	}
	valid, needsRehash, err := current.Verify([]byte("correct horse battery staple"), digest)
	if err != nil || !valid || !needsRehash {
		t.Fatalf("Verify() = valid %v, rehash %v, error %v", valid, needsRehash, err)
	}
}

func TestVerifyRejectsMalformedAndExcessiveDigestsBeforeHashing(t *testing.T) {
	t.Parallel()

	hasher, err := New(testConfig())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	values := []string{
		"not-phc",
		"$argon2id$v=19$m=8192,t=2,p=1junk$MTIzNDU2Nzg5MDEyMzQ1Ng$MTIzNDU2Nzg5MDEyMzQ1Ng",
		"$argon2id$v=19$m=999999,t=2,p=1$MTIzNDU2Nzg5MDEyMzQ1Ng$MTIzNDU2Nzg5MDEyMzQ1Ng",
		"$argon2id$v=18$m=8192,t=2,p=1$MTIzNDU2Nzg5MDEyMzQ1Ng$MTIzNDU2Nzg5MDEyMzQ1Ng",
	}
	for _, value := range values {
		digest, err := credential.NewPasswordDigest(value)
		if err != nil {
			t.Fatalf("NewPasswordDigest() error = %v", err)
		}
		if _, _, err := hasher.Verify([]byte("correct horse battery staple"), digest); !errors.Is(err, errInvalidDigest) {
			t.Fatalf("Verify(%q) error = %v", value, err)
		}
	}
}

func TestEqualizeMissingPerformsDummyVerification(t *testing.T) {
	t.Parallel()

	random := bytes.NewReader(bytes.Repeat([]byte{7}, 128))
	hasher, err := newWithRandom(testConfig(), random)
	if err != nil {
		t.Fatalf("newWithRandom() error = %v", err)
	}
	if err := hasher.EqualizeMissing([]byte("correct horse battery staple")); err != nil {
		t.Fatalf("EqualizeMissing() error = %v", err)
	}
}

func TestRandomFailuresAreSanitized(t *testing.T) {
	t.Parallel()

	secret := "raw-secret-must-not-escape"
	_, err := newWithRandom(testConfig(), errorReader{err: errors.New(secret)})
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("newWithRandom() error = %v", err)
	}
	hasher := &Hasher{config: testConfig(), random: errorReader{err: errors.New(secret)}}
	_, err = hasher.Hash([]byte("correct horse battery staple"))
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("Hash() error = %v", err)
	}
}

func TestConfigBounds(t *testing.T) {
	t.Parallel()

	invalid := []Config{
		{},
		{Memory: maxMemory + 1, Time: 1, Parallelism: 1, SaltLength: 16, KeyLength: 32},
		{Memory: 8192, Time: maxTime + 1, Parallelism: 1, SaltLength: 16, KeyLength: 32},
		{Memory: 8192, Time: 1, Parallelism: maxParallelism + 1, SaltLength: 16, KeyLength: 32},
	}
	for _, config := range invalid {
		if _, err := New(config); err == nil {
			t.Fatalf("New(%+v) error = nil", config)
		}
	}
}

func TestDefaultConfigIsValid(t *testing.T) {
	t.Parallel()

	config := DefaultConfig()
	if err := validateConfig(config); err != nil {
		t.Fatalf("DefaultConfig() is invalid: %v", err)
	}
}

type errorReader struct {
	err error
}

func (reader errorReader) Read([]byte) (int, error) {
	return 0, reader.err
}

func testConfig() Config {
	return Config{Memory: 8192, Time: 2, Parallelism: 1, SaltLength: 16, KeyLength: 32}
}
