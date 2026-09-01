// Package randomid creates opaque subject identifiers from crypto/rand.
package randomid

import (
	"crypto/rand"
	"errors"
	"io"

	"github.com/v0hmly/marketmesh/services/auth/internal/domain/credential"
)

// Generator creates opaque subject identifiers.
type Generator struct {
	random io.Reader
}

// New constructs a generator backed by crypto/rand.Reader.
func New() *Generator {
	return &Generator{random: rand.Reader}
}

// NewSubjectID returns a fresh 128-bit subject identifier.
func (generator *Generator) NewSubjectID() (credential.SubjectID, error) {
	if generator == nil || generator.random == nil {
		return credential.SubjectID{}, errors.New("randomid: generator must not be nil")
	}
	var subjectID credential.SubjectID
	if _, err := io.ReadFull(generator.random, subjectID[:]); err != nil {
		return credential.SubjectID{}, errors.New("randomid: reading randomness")
	}

	return subjectID, nil
}
