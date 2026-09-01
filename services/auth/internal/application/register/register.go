// Package register implements credential registration.
package register

import (
	"context"
	"errors"
	"fmt"

	"github.com/v0hmly/marketmesh/services/auth/internal/domain/credential"
)

// CredentialWriter persists a new credential atomically and reports whether it won the identifier race.
type CredentialWriter interface {
	Create(ctx context.Context, value credential.Credential) (bool, error)
}

// PasswordHasher derives a non-reversible versioned digest.
type PasswordHasher interface {
	Hash(password []byte) (credential.PasswordDigest, error)
}

// SubjectIDGenerator creates opaque subject identifiers.
type SubjectIDGenerator interface {
	NewSubjectID() (credential.SubjectID, error)
}

// UseCase registers credentials without revealing duplicate identifiers.
type UseCase struct {
	writer    CredentialWriter
	hasher    PasswordHasher
	generator SubjectIDGenerator
}

// New constructs a registration use case from explicit ports.
func New(writer CredentialWriter, hasher PasswordHasher, generator SubjectIDGenerator) (*UseCase, error) {
	if writer == nil || hasher == nil || generator == nil {
		return nil, errors.New("register: dependencies must not be nil")
	}

	return &UseCase{writer: writer, hasher: hasher, generator: generator}, nil
}

// Execute validates, hashes, and atomically stores a credential. Existing identifiers are indistinguishable from success.
func (useCase *UseCase) Execute(ctx context.Context, identifierValue string, passwordValue []byte) error {
	if ctx == nil {
		return errors.New("register: context must not be nil")
	}
	identifier, err := credential.NewIdentifier(identifierValue)
	if err != nil {
		return err
	}
	password, err := credential.NewPassword(passwordValue)
	if err != nil {
		return err
	}
	defer password.Destroy()

	passwordBytes := password.Bytes()
	digest, err := useCase.hasher.Hash(passwordBytes)
	clear(passwordBytes)
	if err != nil {
		return fmt.Errorf("register: hashing password: %w", err)
	}
	subjectID, err := useCase.generator.NewSubjectID()
	if err != nil {
		return fmt.Errorf("register: generating subject ID: %w", err)
	}

	_, err = useCase.writer.Create(ctx, credential.New(subjectID, identifier, digest))
	if err != nil {
		return fmt.Errorf("register: storing credential: %w", err)
	}

	return nil
}
