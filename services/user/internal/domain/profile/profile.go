// Package profile defines the User-owned profile and its bounded editable fields.
package profile

import (
	"errors"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

var (
	ErrInvalidProfile = errors.New("invalid profile")
	ErrNotReady       = errors.New("profile not ready")
	ErrConflict       = errors.New("profile version conflict")
)

type SubjectID [16]byte

func NewSubjectID(raw []byte) (SubjectID, error) {
	var id SubjectID
	if len(raw) != len(id) {
		return id, ErrInvalidProfile
	}
	copy(id[:], raw)
	if id == (SubjectID{}) {
		return id, ErrInvalidProfile
	}
	return id, nil
}
func (id SubjectID) Bytes() []byte { return append([]byte(nil), id[:]...) }

type Fields struct {
	DisplayName string
	Bio         string
}

func NewFields(displayName, bio string) (Fields, error) {
	if len(displayName) > 320 || len(bio) > 4000 || !utf8.ValidString(displayName) || !utf8.ValidString(bio) {
		return Fields{}, ErrInvalidProfile
	}
	displayName = strings.TrimSpace(displayName)
	if utf8.RuneCountInString(displayName) > 80 || utf8.RuneCountInString(bio) > 1000 {
		return Fields{}, ErrInvalidProfile
	}
	for _, r := range displayName {
		if unicode.IsControl(r) {
			return Fields{}, ErrInvalidProfile
		}
	}
	for _, r := range bio {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return Fields{}, ErrInvalidProfile
		}
	}
	return Fields{DisplayName: displayName, Bio: bio}, nil
}

type Profile struct {
	SubjectID            SubjectID
	Fields               Fields
	Version              uint64
	CreatedAt, UpdatedAt time.Time
}
