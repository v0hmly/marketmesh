package register_test

import (
	"context"
	"errors"
	"testing"

	"github.com/v0hmly/marketmesh/services/auth/internal/application/register"
	"github.com/v0hmly/marketmesh/services/auth/internal/domain/credential"
)

func TestExecuteHashesAndStoresNormalizedCredential(t *testing.T) {
	t.Parallel()

	writer := &writerStub{created: true}
	hasher := &hasherStub{digest: mustDigest(t, "encoded")}
	generator := &generatorStub{subjectID: subjectID(7)}
	useCase, err := register.New(writer, hasher, generator)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	password := []byte("correct horse battery staple")
	if err := useCase.Execute(context.Background(), " Alice@Example.COM ", password); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if writer.value.Identifier().String() != "alice@example.com" {
		t.Fatalf("stored identifier = %q", writer.value.Identifier().String())
	}
	if writer.value.SubjectID() != generator.subjectID || writer.value.PasswordDigest() != hasher.digest {
		t.Fatal("stored credential does not contain generated values")
	}
	if string(hasher.password) != string(password) {
		t.Fatal("hasher did not receive password")
	}
	if string(password) != "correct horse battery staple" {
		t.Fatal("Execute() modified caller-owned password")
	}
}

func TestExecuteDoesNotRevealDuplicate(t *testing.T) {
	t.Parallel()

	writer := &writerStub{created: false}
	useCase, err := register.New(writer, &hasherStub{digest: mustDigest(t, "encoded")}, &generatorStub{subjectID: subjectID(1)})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := useCase.Execute(context.Background(), "duplicate@example.com", []byte("correct horse battery staple")); err != nil {
		t.Fatalf("Execute(duplicate) error = %v", err)
	}
	if writer.calls != 1 {
		t.Fatalf("writer calls = %d", writer.calls)
	}
}

func TestExecuteRejectsInputBeforeDependencies(t *testing.T) {
	t.Parallel()

	writer := &writerStub{}
	hasher := &hasherStub{}
	generator := &generatorStub{}
	useCase, err := register.New(writer, hasher, generator)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	err = useCase.Execute(context.Background(), "bad value", []byte("correct horse battery staple"))
	if !errors.Is(err, credential.ErrInvalidIdentifier) {
		t.Fatalf("Execute() error = %v", err)
	}
	if writer.calls != 0 || hasher.calls != 0 || generator.calls != 0 {
		t.Fatal("dependencies were called for invalid input")
	}
}

func TestExecutePreservesSanitizedDependencyErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		writer    *writerStub
		hasher    *hasherStub
		generator *generatorStub
	}{
		{name: "hash", writer: &writerStub{}, hasher: &hasherStub{err: errors.New("hash failed")}, generator: &generatorStub{}},
		{name: "identifier", writer: &writerStub{}, hasher: &hasherStub{digest: mustDigest(t, "encoded")}, generator: &generatorStub{err: errors.New("random failed")}},
		{name: "write", writer: &writerStub{err: errors.New("write failed")}, hasher: &hasherStub{digest: mustDigest(t, "encoded")}, generator: &generatorStub{subjectID: subjectID(1)}},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			useCase, err := register.New(test.writer, test.hasher, test.generator)
			if err != nil {
				t.Fatalf("New() error = %v", err)
			}
			if err := useCase.Execute(context.Background(), "user@example.com", []byte("correct horse battery staple")); err == nil {
				t.Fatal("Execute() error = nil")
			}
		})
	}
}

func TestNewRejectsNilDependenciesAndExecuteRejectsNilContext(t *testing.T) {
	t.Parallel()

	if _, err := register.New(nil, &hasherStub{}, &generatorStub{}); err == nil {
		t.Fatal("New(nil writer) error = nil")
	}
	useCase, err := register.New(&writerStub{}, &hasherStub{}, &generatorStub{})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := useCase.Execute(nil, "user@example.com", []byte("correct horse battery staple")); err == nil {
		t.Fatal("Execute(nil context) error = nil")
	}
}

type writerStub struct {
	created bool
	err     error
	value   credential.Credential
	calls   int
}

func (stub *writerStub) Create(_ context.Context, value credential.Credential) (bool, error) {
	stub.calls++
	stub.value = value
	return stub.created, stub.err
}

type hasherStub struct {
	digest   credential.PasswordDigest
	err      error
	password []byte
	calls    int
}

func (stub *hasherStub) Hash(password []byte) (credential.PasswordDigest, error) {
	stub.calls++
	stub.password = append([]byte(nil), password...)
	return stub.digest, stub.err
}

type generatorStub struct {
	subjectID credential.SubjectID
	err       error
	calls     int
}

func (stub *generatorStub) NewSubjectID() (credential.SubjectID, error) {
	stub.calls++
	return stub.subjectID, stub.err
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
