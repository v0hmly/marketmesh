// Package login implements credential verification and transparent digest upgrades.
package login

import (
	"context"
	"errors"
	"fmt"

	"github.com/v0hmly/marketmesh/services/auth/internal/domain/credential"
)

// ErrInvalidCredentials is the only expected authentication failure exposed to incoming adapters.
var ErrInvalidCredentials = errors.New("login: invalid credentials")

// CredentialReader reads security state from a strongly consistent source.
type CredentialReader interface {
	FindByIdentifier(ctx context.Context, identifier credential.Identifier) (credential.Credential, bool, error)
}

// PasswordDigestUpdater replaces a digest only if it still equals the previously verified value.
type PasswordDigestUpdater interface {
	UpdatePasswordDigest(ctx context.Context, subjectID credential.SubjectID, previous, next credential.PasswordDigest) error
}

// PasswordHasher verifies and upgrades versioned password digests.
type PasswordHasher interface {
	Verify(password []byte, digest credential.PasswordDigest) (valid bool, needsRehash bool, err error)
	Hash(password []byte) (credential.PasswordDigest, error)
	EqualizeMissing(password []byte) error
}

// Audit records a finite login outcome without credential or subject data.
type Audit interface {
	LoginSucceeded(ctx context.Context)
	LoginFailed(ctx context.Context, reason FailureReason)
}

// FailureReason is a bounded, non-PII audit category.
type FailureReason string

const (
	// FailureReasonInvalidInput indicates structurally invalid input.
	FailureReasonInvalidInput FailureReason = "invalid_input"
	// FailureReasonRejected intentionally combines unknown identifiers and wrong passwords.
	FailureReasonRejected FailureReason = "rejected"
	// FailureReasonSystemError indicates that verification could not complete because a dependency failed.
	FailureReasonSystemError FailureReason = "system_error"
)

// UseCase verifies a credential and returns an opaque subject identifier.
type UseCase struct {
	reader  CredentialReader
	updater PasswordDigestUpdater
	hasher  PasswordHasher
	audit   Audit
}

// New constructs a login use case from explicit ports.
func New(reader CredentialReader, updater PasswordDigestUpdater, hasher PasswordHasher, audit Audit) (*UseCase, error) {
	if reader == nil || updater == nil || hasher == nil || audit == nil {
		return nil, errors.New("login: dependencies must not be nil")
	}

	return &UseCase{reader: reader, updater: updater, hasher: hasher, audit: audit}, nil
}

// Execute validates and verifies a credential using a non-enumerating failure path.
func (useCase *UseCase) Execute(ctx context.Context, identifierValue string, passwordValue []byte) (credential.SubjectID, error) {
	if ctx == nil {
		return credential.SubjectID{}, errors.New("login: context must not be nil")
	}
	identifier, err := credential.NewIdentifier(identifierValue)
	if err != nil {
		useCase.audit.LoginFailed(ctx, FailureReasonInvalidInput)
		return credential.SubjectID{}, ErrInvalidCredentials
	}
	password, err := credential.NewPassword(passwordValue)
	if err != nil {
		useCase.audit.LoginFailed(ctx, FailureReasonInvalidInput)
		return credential.SubjectID{}, ErrInvalidCredentials
	}
	defer password.Destroy()

	stored, found, err := useCase.reader.FindByIdentifier(ctx, identifier)
	if err != nil {
		useCase.audit.LoginFailed(ctx, FailureReasonSystemError)
		return credential.SubjectID{}, fmt.Errorf("login: reading credential: %w", err)
	}
	passwordBytes := password.Bytes()
	defer clear(passwordBytes)
	if !found {
		if err := useCase.hasher.EqualizeMissing(passwordBytes); err != nil {
			useCase.audit.LoginFailed(ctx, FailureReasonSystemError)
			return credential.SubjectID{}, fmt.Errorf("login: equalizing missing credential: %w", err)
		}
		useCase.audit.LoginFailed(ctx, FailureReasonRejected)
		return credential.SubjectID{}, ErrInvalidCredentials
	}

	valid, needsRehash, err := useCase.hasher.Verify(passwordBytes, stored.PasswordDigest())
	if err != nil {
		useCase.audit.LoginFailed(ctx, FailureReasonSystemError)
		return credential.SubjectID{}, fmt.Errorf("login: verifying password: %w", err)
	}
	if !valid {
		useCase.audit.LoginFailed(ctx, FailureReasonRejected)
		return credential.SubjectID{}, ErrInvalidCredentials
	}
	if needsRehash {
		nextDigest, err := useCase.hasher.Hash(passwordBytes)
		if err != nil {
			useCase.audit.LoginFailed(ctx, FailureReasonSystemError)
			return credential.SubjectID{}, fmt.Errorf("login: rehashing password: %w", err)
		}
		if err := useCase.updater.UpdatePasswordDigest(ctx, stored.SubjectID(), stored.PasswordDigest(), nextDigest); err != nil {
			useCase.audit.LoginFailed(ctx, FailureReasonSystemError)
			return credential.SubjectID{}, fmt.Errorf("login: updating password digest: %w", err)
		}
	}

	useCase.audit.LoginSucceeded(ctx)
	return stored.SubjectID(), nil
}
