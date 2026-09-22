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
	LastName    string
	BirthDate   string
	Gender      Gender
	Phone       string
	City        string
	ShowAge     bool
}

type Gender int32

const (
	GenderUnspecified Gender = iota
	GenderFemale
	GenderMale
)

func NewFields(displayName, bio string) (Fields, error) {
	return NormalizeFields(Fields{DisplayName: displayName, Bio: bio}, time.Now())
}

// NormalizeFields bounds private profile input before persistence. BirthDate is
// a calendar date, independent of the client's timezone; future dates use UTC.
func NormalizeFields(f Fields, now time.Time) (Fields, error) {
	for _, item := range []struct {
		value *string
		limit int
	}{
		{&f.DisplayName, 80}, {&f.LastName, 80}, {&f.City, 120},
	} {
		raw := *item.value
		if len(raw) > item.limit*4 || !utf8.ValidString(raw) || strings.ContainsFunc(raw, unicode.IsControl) {
			return Fields{}, ErrInvalidProfile
		}
		*item.value = strings.TrimSpace(raw)
		if utf8.RuneCountInString(*item.value) > item.limit {
			return Fields{}, ErrInvalidProfile
		}
	}
	if len(f.Bio) > 4000 || !utf8.ValidString(f.Bio) || utf8.RuneCountInString(f.Bio) > 1000 {
		return Fields{}, ErrInvalidProfile
	}
	for _, r := range f.Bio {
		if unicode.IsControl(r) && r != '\n' && r != '\t' {
			return Fields{}, ErrInvalidProfile
		}
	}
	if f.Gender < GenderUnspecified || f.Gender > GenderMale {
		return Fields{}, ErrInvalidProfile
	}
	if f.BirthDate != "" {
		if len(f.BirthDate) != len(time.DateOnly) {
			return Fields{}, ErrInvalidProfile
		}
		date, err := time.Parse(time.DateOnly, f.BirthDate)
		if err != nil || date.Year() < 1 || date.Format(time.DateOnly) != f.BirthDate || f.BirthDate > now.UTC().Format(time.DateOnly) {
			return Fields{}, ErrInvalidProfile
		}
	}
	if len(f.Phone) > 32 {
		return Fields{}, ErrInvalidProfile
	}
	digits := 0
	for _, r := range f.Phone {
		if r >= '0' && r <= '9' {
			digits++
		} else if !strings.ContainsRune(" +-.()", r) {
			return Fields{}, ErrInvalidProfile
		}
	}
	f.Phone = strings.TrimSpace(f.Phone)
	if f.Phone != "" && (digits < 7 || digits > 15) {
		return Fields{}, ErrInvalidProfile
	}
	return f, nil
}

type Profile struct {
	SubjectID            SubjectID
	Fields               Fields
	Version              uint64
	CreatedAt, UpdatedAt time.Time
}
