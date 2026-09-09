// Package register implements credential registration.
package register

import (
	"context"
	"errors"
	"fmt"

	"github.com/v0hmly/marketmesh/services/auth/internal/domain/credential"
	"github.com/v0hmly/marketmesh/services/auth/internal/domain/registrationevent"
)

// CredentialWriter persists a new credential atomically and reports whether it won the identifier race.
type CredentialWriter interface {
	Create(ctx context.Context, value credential.Credential) (bool, error)
}

// RegistrationWriter atomically stores a new credential and its registration fact.
// Losing the duplicate identifier race must store neither a credential nor an event.
type RegistrationWriter interface {
	CreateRegistration(context.Context, credential.Credential, registrationevent.Event) (bool, error)
}
type EventFactory interface {
	New(context.Context, credential.SubjectID) (registrationevent.Event, error)
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
	writer             CredentialWriter
	registrationWriter RegistrationWriter
	factory            EventFactory
	hasher             PasswordHasher
	generator          SubjectIDGenerator
}

// New constructs a registration use case from explicit ports.
func New(writer CredentialWriter, hasher PasswordHasher, generator SubjectIDGenerator) (*UseCase, error) {
	if writer == nil || hasher == nil || generator == nil {
		return nil, errors.New("register: dependencies must not be nil")
	}

	return &UseCase{writer: writer, hasher: hasher, generator: generator}, nil
}

// NewWithEvents enables the atomic credential/outbox registration boundary.
func NewWithEvents(writer RegistrationWriter, hasher PasswordHasher, generator SubjectIDGenerator, factory EventFactory) (*UseCase, error) {
	if writer == nil || hasher == nil || generator == nil || factory == nil {
		return nil, errors.New("register: dependencies must not be nil")
	}
	return &UseCase{registrationWriter: writer, hasher: hasher, generator: generator, factory: factory}, nil
}

// Execute validates, hashes, and atomically stores a credential. Existing identifiers are indistinguishable from success.
func (useCase *UseCase) Execute(ctx context.Context, identifierValue string, passwordValue []byte) error {
	if ctx == nil {
		return errors.New("register: context must not be nil")
	}
	if err := ctx.Err(); err != nil {
		return err
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

	value := credential.New(subjectID, identifier, digest)
	if useCase.registrationWriter != nil {
		event, eventErr := useCase.factory.New(ctx, subjectID)
		if eventErr != nil {
			if errors.Is(eventErr, context.Canceled) || errors.Is(eventErr, context.DeadlineExceeded) {
				return eventErr
			}
			return errors.New("register: creating event failed")
		}
		if event.Validate() != nil || event.SubjectID != subjectID {
			return errors.New("register: invalid registration event")
		}
		_, err = useCase.registrationWriter.CreateRegistration(ctx, value, event)
	} else {
		_, err = useCase.writer.Create(ctx, value)
	}
	if err != nil {
		return fmt.Errorf("register: storing credential: %w", err)
	}

	return nil
}
