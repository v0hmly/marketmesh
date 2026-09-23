// Package address defines the bounded, private address book.
package address

import (
	"errors"
	"math"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/v0hmly/marketmesh/services/user/internal/domain/profile"
)

const MaxAddresses = 20

var (
	ErrInvalid  = errors.New("invalid address")
	ErrNotFound = errors.New("address not found")
	ErrLimit    = errors.New("address limit reached")
	ErrConflict = errors.New("address book version conflict")
)

type ID [16]byte

func NewID(raw []byte) (ID, error) {
	var id ID
	if len(raw) != 16 {
		return id, ErrInvalid
	}
	copy(id[:], raw)
	if id == (ID{}) {
		return id, ErrInvalid
	}
	return id, nil
}
func (id ID) Bytes() []byte { return append([]byte(nil), id[:]...) }

type Fields struct{ Recipient, Phone, Country, PostalCode, City, StreetHouse, Apartment, Comment string }

func NewFields(f Fields) (Fields, error) {
	for _, item := range []struct {
		value               *string
		limit               int
		required, multiline bool
	}{
		{&f.Recipient, 120, true, false}, {&f.Phone, 32, true, false}, {&f.Country, 80, true, false}, {&f.PostalCode, 20, false, false}, {&f.City, 120, true, false}, {&f.StreetHouse, 240, true, false}, {&f.Apartment, 40, false, false}, {&f.Comment, 500, false, true},
	} {
		raw := *item.value
		maxBytes := item.limit * 4
		if item.value == &f.Phone {
			maxBytes = 32
			for _, r := range raw {
				if !(r >= '0' && r <= '9') && !strings.ContainsRune(" +-.()", r) {
					return Fields{}, ErrInvalid
				}
			}
		}
		if len(raw) > maxBytes || !utf8.ValidString(raw) {
			return Fields{}, ErrInvalid
		}
		// Reject controls before trimming so malformed input cannot normalize into valid input.
		for _, r := range raw {
			if unicode.IsControl(r) && !(item.multiline && (r == '\n' || r == '\t')) {
				return Fields{}, ErrInvalid
			}
		}
		value := strings.TrimSpace(raw)
		if utf8.RuneCountInString(value) > item.limit || (item.required && value == "") {
			return Fields{}, ErrInvalid
		}
		*item.value = value
	}
	digits := 0
	for _, r := range f.Phone {
		if r >= '0' && r <= '9' {
			digits++
			continue
		}
		if !strings.ContainsRune(" +-.()", r) {
			return Fields{}, ErrInvalid
		}
	}
	if digits < 7 || digits > 15 {
		return Fields{}, ErrInvalid
	}
	return f, nil
}
func ValidVersion(v uint64) bool { return v > 0 && v < math.MaxInt64 }

type Address struct {
	ID        ID
	Fields    Fields
	IsDefault bool
}
type Book struct {
	SubjectID profile.SubjectID
	Version   uint64
	Addresses []Address
}

func (b Book) Validate() error {
	if b.SubjectID == (profile.SubjectID{}) || b.Version == 0 || b.Version > math.MaxInt64 || len(b.Addresses) > MaxAddresses {
		return ErrInvalid
	}
	seen := map[ID]bool{}
	defaults := 0
	for _, a := range b.Addresses {
		f, err := NewFields(a.Fields)
		if err != nil || f != a.Fields || a.ID == (ID{}) || seen[a.ID] {
			return ErrInvalid
		}
		seen[a.ID] = true
		if a.IsDefault {
			defaults++
		}
	}
	if defaults > 1 {
		return ErrInvalid
	}
	return nil
}
