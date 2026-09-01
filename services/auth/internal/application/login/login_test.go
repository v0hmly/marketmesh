package login_test

import (
	"context"
	"errors"
	"testing"

	"github.com/v0hmly/marketmesh/services/auth/internal/application/login"
	"github.com/v0hmly/marketmesh/services/auth/internal/domain/credential"
)

var errDependency = errors.New("dependency failed")

func TestExecuteReturnsSubjectAndAuditsSuccess(t *testing.T) {
	t.Parallel()

	stored := storedCredential(t)
	reader := &readerStub{value: stored, found: true}
	updater := &updaterStub{}
	hasher := &hasherStub{valid: true}
	audit := &auditStub{}
	useCase := mustUseCase(t, reader, updater, hasher, audit)

	got, err := useCase.Execute(context.Background(), "USER@example.com", []byte("correct horse battery staple"))
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if got != stored.SubjectID() {
		t.Fatal("Execute() returned wrong subject ID")
	}
	if audit.successes != 1 || len(audit.failures) != 0 {
		t.Fatalf("audit = successes %d, failures %v", audit.successes, audit.failures)
	}
	if reader.identifier.String() != "user@example.com" {
		t.Fatalf("reader identifier = %q", reader.identifier.String())
	}
}

func TestExecuteUsesSameFailureForUnknownAndWrongPassword(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		reader *readerStub
		hasher *hasherStub
	}{
		{name: "unknown", reader: &readerStub{}, hasher: &hasherStub{}},
		{name: "wrong password", reader: &readerStub{value: storedCredential(t), found: true}, hasher: &hasherStub{}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			audit := &auditStub{}
			useCase := mustUseCase(t, test.reader, &updaterStub{}, test.hasher, audit)
			_, err := useCase.Execute(context.Background(), "user@example.com", []byte("correct horse battery staple"))
			if !errors.Is(err, login.ErrInvalidCredentials) || err.Error() != login.ErrInvalidCredentials.Error() {
				t.Fatalf("Execute() error = %v", err)
			}
			if len(audit.failures) != 1 || audit.failures[0] != login.FailureReasonRejected {
				t.Fatalf("audit failures = %v", audit.failures)
			}
		})
	}
	if tests[0].hasher.equalizeCalls != 1 {
		t.Fatalf("unknown equalize calls = %d", tests[0].hasher.equalizeCalls)
	}
	if tests[1].hasher.verifyCalls != 1 {
		t.Fatalf("wrong password verify calls = %d", tests[1].hasher.verifyCalls)
	}
}

func TestExecuteRehashesWithCompareAndSwap(t *testing.T) {
	t.Parallel()

	stored := storedCredential(t)
	next := mustDigest(t, "next-digest")
	updater := &updaterStub{}
	hasher := &hasherStub{valid: true, needsRehash: true, next: next}
	useCase := mustUseCase(t, &readerStub{value: stored, found: true}, updater, hasher, &auditStub{})

	if _, err := useCase.Execute(context.Background(), "user@example.com", []byte("correct horse battery staple")); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if updater.calls != 1 || updater.subjectID != stored.SubjectID() || updater.previous != stored.PasswordDigest() || updater.next != next {
		t.Fatal("digest compare-and-swap arguments are incorrect")
	}
}

func TestExecuteAuditsInputAndSystemFailures(t *testing.T) {
	t.Parallel()

	t.Run("input", func(t *testing.T) {
		audit := &auditStub{}
		useCase := mustUseCase(t, &readerStub{}, &updaterStub{}, &hasherStub{}, audit)
		_, err := useCase.Execute(context.Background(), "bad value", []byte("correct horse battery staple"))
		if !errors.Is(err, login.ErrInvalidCredentials) {
			t.Fatalf("Execute() error = %v", err)
		}
		if len(audit.failures) != 1 || audit.failures[0] != login.FailureReasonInvalidInput {
			t.Fatalf("audit failures = %v", audit.failures)
		}
	})

	t.Run("system", func(t *testing.T) {
		audit := &auditStub{}
		useCase := mustUseCase(t, &readerStub{err: errDependency}, &updaterStub{}, &hasherStub{}, audit)
		_, err := useCase.Execute(context.Background(), "user@example.com", []byte("correct horse battery staple"))
		if !errors.Is(err, errDependency) {
			t.Fatalf("Execute() error = %v", err)
		}
		if len(audit.failures) != 1 || audit.failures[0] != login.FailureReasonSystemError {
			t.Fatalf("audit failures = %v", audit.failures)
		}
	})
}

func TestExecuteAuditsHasherAndUpdateSystemFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		reader  *readerStub
		updater *updaterStub
		hasher  *hasherStub
	}{
		{name: "missing equalizer", reader: &readerStub{}, updater: &updaterStub{}, hasher: &hasherStub{equalizeErr: errDependency}},
		{name: "verify", reader: &readerStub{value: storedCredential(t), found: true}, updater: &updaterStub{}, hasher: &hasherStub{verifyErr: errDependency}},
		{name: "rehash", reader: &readerStub{value: storedCredential(t), found: true}, updater: &updaterStub{}, hasher: &hasherStub{valid: true, needsRehash: true, hashErr: errDependency}},
		{name: "update", reader: &readerStub{value: storedCredential(t), found: true}, updater: &updaterStub{err: errDependency}, hasher: &hasherStub{valid: true, needsRehash: true, next: mustDigest(t, "next")}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			audit := &auditStub{}
			useCase := mustUseCase(t, test.reader, test.updater, test.hasher, audit)
			_, err := useCase.Execute(context.Background(), "user@example.com", []byte("correct horse battery staple"))
			if !errors.Is(err, errDependency) {
				t.Fatalf("Execute() error = %v", err)
			}
			if len(audit.failures) != 1 || audit.failures[0] != login.FailureReasonSystemError {
				t.Fatalf("audit failures = %v", audit.failures)
			}
		})
	}
}

func TestNewRejectsNilDependenciesAndExecuteRejectsNilContext(t *testing.T) {
	t.Parallel()

	reader := &readerStub{}
	updater := &updaterStub{}
	hasher := &hasherStub{}
	audit := &auditStub{}
	if _, err := login.New(nil, updater, hasher, audit); err == nil {
		t.Fatal("New(nil reader) error = nil")
	}
	useCase := mustUseCase(t, reader, updater, hasher, audit)
	if _, err := useCase.Execute(nil, "user@example.com", []byte("correct horse battery staple")); err == nil {
		t.Fatal("Execute(nil context) error = nil")
	}
}

type readerStub struct {
	value      credential.Credential
	found      bool
	err        error
	identifier credential.Identifier
}

func (stub *readerStub) FindByIdentifier(_ context.Context, identifier credential.Identifier) (credential.Credential, bool, error) {
	stub.identifier = identifier
	return stub.value, stub.found, stub.err
}

type updaterStub struct {
	subjectID credential.SubjectID
	previous  credential.PasswordDigest
	next      credential.PasswordDigest
	err       error
	calls     int
}

func (stub *updaterStub) UpdatePasswordDigest(_ context.Context, subjectID credential.SubjectID, previous, next credential.PasswordDigest) error {
	stub.calls++
	stub.subjectID = subjectID
	stub.previous = previous
	stub.next = next
	return stub.err
}

type hasherStub struct {
	valid         bool
	needsRehash   bool
	verifyErr     error
	equalizeErr   error
	hashErr       error
	next          credential.PasswordDigest
	verifyCalls   int
	equalizeCalls int
}

func (stub *hasherStub) Verify(_ []byte, _ credential.PasswordDigest) (bool, bool, error) {
	stub.verifyCalls++
	return stub.valid, stub.needsRehash, stub.verifyErr
}

func (stub *hasherStub) Hash(_ []byte) (credential.PasswordDigest, error) {
	return stub.next, stub.hashErr
}

func (stub *hasherStub) EqualizeMissing(_ []byte) error {
	stub.equalizeCalls++
	return stub.equalizeErr
}

type auditStub struct {
	successes int
	failures  []login.FailureReason
}

func (stub *auditStub) LoginSucceeded(context.Context) {
	stub.successes++
}

func (stub *auditStub) LoginFailed(_ context.Context, reason login.FailureReason) {
	stub.failures = append(stub.failures, reason)
}

func mustUseCase(t *testing.T, reader login.CredentialReader, updater login.PasswordDigestUpdater, hasher login.PasswordHasher, audit login.Audit) *login.UseCase {
	t.Helper()
	useCase, err := login.New(reader, updater, hasher, audit)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return useCase
}

func storedCredential(t *testing.T) credential.Credential {
	t.Helper()
	identifier, err := credential.NewIdentifier("user@example.com")
	if err != nil {
		t.Fatalf("NewIdentifier() error = %v", err)
	}
	return credential.New(subjectID(9), identifier, mustDigest(t, "stored-digest"))
}

func subjectID(first byte) credential.SubjectID {
	var result credential.SubjectID
	result[0] = first
	return result
}

func mustDigest(t *testing.T, value string) credential.PasswordDigest {
	t.Helper()
	digest, err := credential.NewPasswordDigest(value)
	if err != nil {
		t.Fatalf("NewPasswordDigest() error = %v", err)
	}
	return digest
}
