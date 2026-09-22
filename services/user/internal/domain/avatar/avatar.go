// Package avatar defines an independently versioned, private Files association.
package avatar

import (
	"errors"
	"math"

	"github.com/v0hmly/marketmesh/services/user/internal/domain/profile"
)

var (
	ErrInvalid     = errors.New("invalid avatar")
	ErrConflict    = errors.New("avatar version conflict")
	ErrUnavailable = errors.New("avatar unavailable")
)

type FileID [16]byte

func ParseFileID(raw []byte) (FileID, error) {
	var id FileID
	if len(raw) != len(id) {
		return id, ErrInvalid
	}
	copy(id[:], raw)
	if id == (FileID{}) {
		return id, ErrInvalid
	}
	return id, nil
}

func ValidExpectedVersion(v uint64) bool { return v > 0 && v < math.MaxInt64 }

type Avatar struct {
	SubjectID profile.SubjectID
	Version   uint64
	FileID    FileID // Zero denotes no selected avatar.
}

func (a Avatar) Validate() error {
	if a.SubjectID == (profile.SubjectID{}) || a.Version == 0 || a.Version > math.MaxInt64 {
		return ErrInvalid
	}
	return nil
}
