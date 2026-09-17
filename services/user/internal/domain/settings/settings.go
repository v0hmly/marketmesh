// Package settings defines the account's bounded presentation preferences.
package settings

import (
	"errors"
	"github.com/v0hmly/marketmesh/services/user/internal/domain/profile"
	"math"
)

var (
	ErrInvalid  = errors.New("invalid settings")
	ErrConflict = errors.New("settings version conflict")
)

type Theme string

const (
	System Theme = "system"
	Light  Theme = "light"
	Dark   Theme = "dark"
)

func (t Theme) Valid() bool              { return t == System || t == Light || t == Dark }
func ValidExpectedVersion(v uint64) bool { return v > 0 && v < math.MaxInt64 }

type Settings struct {
	SubjectID profile.SubjectID
	Version   uint64
	Theme     Theme
}

func (s Settings) Validate() error {
	if s.SubjectID == (profile.SubjectID{}) || s.Version == 0 || s.Version > math.MaxInt64 || !s.Theme.Valid() {
		return ErrInvalid
	}
	return nil
}
