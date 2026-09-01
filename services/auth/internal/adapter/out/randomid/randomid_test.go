package randomid

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestGeneratorUsesRandomSource(t *testing.T) {
	t.Parallel()

	generator := &Generator{random: bytes.NewReader(bytes.Repeat([]byte{9}, 16))}
	subjectID, err := generator.NewSubjectID()
	if err != nil {
		t.Fatalf("NewSubjectID() error = %v", err)
	}
	if !bytes.Equal(subjectID.Bytes(), bytes.Repeat([]byte{9}, 16)) {
		t.Fatalf("NewSubjectID() = %v", subjectID.Bytes())
	}
}

func TestNewUsesCryptographicRandomness(t *testing.T) {
	t.Parallel()

	first, err := New().NewSubjectID()
	if err != nil {
		t.Fatalf("NewSubjectID(first) error = %v", err)
	}
	second, err := New().NewSubjectID()
	if err != nil {
		t.Fatalf("NewSubjectID(second) error = %v", err)
	}
	if first == second || first == ([16]byte{}) || second == ([16]byte{}) {
		t.Fatalf("generated IDs are not distinct non-zero values: %v %v", first, second)
	}
}

func TestNilGeneratorFails(t *testing.T) {
	t.Parallel()

	var generator *Generator
	if _, err := generator.NewSubjectID(); err == nil {
		t.Fatal("NewSubjectID() error = nil")
	}
}

func TestGeneratorSanitizesRandomFailure(t *testing.T) {
	t.Parallel()

	secret := "random-backend-secret"
	generator := &Generator{random: failingReader{err: errors.New(secret)}}
	_, err := generator.NewSubjectID()
	if err == nil || strings.Contains(err.Error(), secret) {
		t.Fatalf("NewSubjectID() error = %v", err)
	}
}

type failingReader struct {
	err error
}

func (reader failingReader) Read([]byte) (int, error) {
	return 0, reader.err
}
