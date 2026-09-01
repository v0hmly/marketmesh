// Package argon2id implements versioned Argon2id password digests.
package argon2id

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/v0hmly/marketmesh/services/auth/internal/domain/credential"
	"golang.org/x/crypto/argon2"
)

const (
	algorithm = "argon2id"
	version   = argon2.Version

	maxMemory      = 256 * 1024
	maxTime        = 10
	maxParallelism = 16
	maxSaltLength  = 64
	maxKeyLength   = 64
)

var errInvalidDigest = errors.New("argon2id: invalid password digest")

// Config contains the current Argon2id parameters used for new digests.
type Config struct {
	Memory      uint32
	Time        uint32
	Parallelism uint8
	SaltLength  uint32
	KeyLength   uint32
}

// DefaultConfig returns the production baseline: 64 MiB, three iterations, and two lanes.
func DefaultConfig() Config {
	return Config{Memory: 64 * 1024, Time: 3, Parallelism: 2, SaltLength: 16, KeyLength: 32}
}

// Hasher owns current hashing policy and a process-local dummy digest.
type Hasher struct {
	config      Config
	random      io.Reader
	dummyDigest credential.PasswordDigest
}

// New constructs a password hasher and prepares a randomized dummy digest for unknown identifiers.
func New(config Config) (*Hasher, error) {
	return newWithRandom(config, rand.Reader)
}

func newWithRandom(config Config, random io.Reader) (*Hasher, error) {
	if err := validateConfig(config); err != nil {
		return nil, err
	}
	if random == nil {
		return nil, errors.New("argon2id: random source must not be nil")
	}
	hasher := &Hasher{config: config, random: random}
	dummyPassword := make([]byte, 32)
	if _, err := io.ReadFull(random, dummyPassword); err != nil {
		return nil, errors.New("argon2id: reading dummy password randomness")
	}
	digest, err := hasher.Hash(dummyPassword)
	clear(dummyPassword)
	if err != nil {
		return nil, fmt.Errorf("argon2id: creating dummy digest: %w", err)
	}
	hasher.dummyDigest = digest

	return hasher, nil
}

// Hash derives a PHC-encoded digest with fresh cryptographic salt.
func (hasher *Hasher) Hash(password []byte) (credential.PasswordDigest, error) {
	if hasher == nil {
		return credential.PasswordDigest{}, errors.New("argon2id: hasher must not be nil")
	}
	salt := make([]byte, hasher.config.SaltLength)
	if _, err := io.ReadFull(hasher.random, salt); err != nil {
		return credential.PasswordDigest{}, errors.New("argon2id: reading salt randomness")
	}
	derived := argon2.IDKey(password, salt, hasher.config.Time, hasher.config.Memory, hasher.config.Parallelism, hasher.config.KeyLength)
	encoded := fmt.Sprintf(
		"$%s$v=%d$m=%d,t=%d,p=%d$%s$%s",
		algorithm,
		version,
		hasher.config.Memory,
		hasher.config.Time,
		hasher.config.Parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(derived),
	)
	clear(derived)

	return credential.NewPasswordDigest(encoded)
}

// Verify compares a password in constant time and reports whether policy requires a new digest.
func (hasher *Hasher) Verify(password []byte, digest credential.PasswordDigest) (bool, bool, error) {
	if hasher == nil {
		return false, false, errors.New("argon2id: hasher must not be nil")
	}
	parsed, salt, expected, err := parseDigest(digest.String())
	if err != nil {
		return false, false, err
	}
	actual := argon2.IDKey(password, salt, parsed.Time, parsed.Memory, parsed.Parallelism, parsed.KeyLength)
	valid := subtle.ConstantTimeCompare(actual, expected) == 1
	clear(actual)
	clear(expected)

	return valid, parsed != hasher.config, nil
}

// EqualizeMissing performs a complete Argon2id verification for an unknown identifier.
func (hasher *Hasher) EqualizeMissing(password []byte) error {
	_, _, err := hasher.Verify(password, hasher.dummyDigest)
	return err
}

func validateConfig(config Config) error {
	if config.Memory < 8*1024 || config.Memory > maxMemory ||
		config.Time == 0 || config.Time > maxTime ||
		config.Parallelism == 0 || config.Parallelism > maxParallelism ||
		config.SaltLength < 16 || config.SaltLength > maxSaltLength ||
		config.KeyLength < 16 || config.KeyLength > maxKeyLength {
		return errors.New("argon2id: parameters are outside safe limits")
	}
	return nil
}

func parseDigest(encoded string) (Config, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[0] != "" || parts[1] != algorithm || parts[2] != "v="+strconv.Itoa(version) {
		return Config{}, nil, nil, errInvalidDigest
	}
	parameters := strings.Split(parts[3], ",")
	if len(parameters) != 3 {
		return Config{}, nil, nil, errInvalidDigest
	}
	memoryValue, err := parseUint32Parameter(parameters[0], "m=")
	if err != nil {
		return Config{}, nil, nil, errInvalidDigest
	}
	timeValue, err := parseUint32Parameter(parameters[1], "t=")
	if err != nil {
		return Config{}, nil, nil, errInvalidDigest
	}
	parallelismValue, err := parseUint8Parameter(parameters[2], "p=")
	if err != nil {
		return Config{}, nil, nil, errInvalidDigest
	}
	salt, err := base64.RawStdEncoding.Strict().DecodeString(parts[4])
	if err != nil {
		return Config{}, nil, nil, errInvalidDigest
	}
	key, err := base64.RawStdEncoding.Strict().DecodeString(parts[5])
	if err != nil {
		return Config{}, nil, nil, errInvalidDigest
	}
	saltLength, err := lengthUint32(len(salt))
	if err != nil {
		clear(key)
		return Config{}, nil, nil, errInvalidDigest
	}
	keyLength, err := lengthUint32(len(key))
	if err != nil {
		clear(key)
		return Config{}, nil, nil, errInvalidDigest
	}
	config := Config{Memory: memoryValue, Time: timeValue, Parallelism: parallelismValue, SaltLength: saltLength, KeyLength: keyLength}
	if err := validateConfig(config); err != nil {
		clear(key)
		return Config{}, nil, nil, errInvalidDigest
	}

	return config, salt, key, nil
}

func parseUint32Parameter(value string, prefix string) (uint32, error) {
	if !strings.HasPrefix(value, prefix) || len(value) == len(prefix) {
		return 0, errInvalidDigest
	}
	raw := strings.TrimPrefix(value, prefix)
	var parsed uint32
	count, err := fmt.Sscanf(raw, "%d", &parsed)
	if err != nil || count != 1 || strconv.FormatUint(uint64(parsed), 10) != raw {
		return 0, errInvalidDigest
	}
	return parsed, nil
}

func parseUint8Parameter(value string, prefix string) (uint8, error) {
	if !strings.HasPrefix(value, prefix) || len(value) == len(prefix) {
		return 0, errInvalidDigest
	}
	raw := strings.TrimPrefix(value, prefix)
	var parsed uint8
	count, err := fmt.Sscanf(raw, "%d", &parsed)
	if err != nil || count != 1 || strconv.FormatUint(uint64(parsed), 10) != raw {
		return 0, errInvalidDigest
	}
	return parsed, nil
}

func lengthUint32(value int) (uint32, error) {
	raw := strconv.Itoa(value)
	var parsed uint32
	count, err := fmt.Sscanf(raw, "%d", &parsed)
	if err != nil || count != 1 || strconv.FormatUint(uint64(parsed), 10) != raw {
		return 0, errInvalidDigest
	}
	return parsed, nil
}
