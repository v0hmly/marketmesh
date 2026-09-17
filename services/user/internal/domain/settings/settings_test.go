package settings

import (
	"github.com/v0hmly/marketmesh/services/user/internal/domain/profile"
	"math"
	"testing"
)

func TestThemeAndVersionBounds(t *testing.T) {
	for _, theme := range []Theme{System, Light, Dark} {
		if !theme.Valid() {
			t.Fatal(theme)
		}
	}
	for _, theme := range []Theme{"", "SYSTEM", "blue", " system"} {
		if theme.Valid() {
			t.Fatal("unknown theme accepted")
		}
	}
	for _, v := range []uint64{0, math.MaxInt64, math.MaxUint64} {
		if ValidExpectedVersion(v) {
			t.Fatal("invalid expected version", v)
		}
	}
	if !ValidExpectedVersion(1) || !ValidExpectedVersion(math.MaxInt64-1) {
		t.Fatal("valid version rejected")
	}
	s := Settings{SubjectID: profile.SubjectID{1}, Version: math.MaxInt64, Theme: System}
	if s.Validate() != nil {
		t.Fatal("terminal readable version rejected")
	}
	s.SubjectID = profile.SubjectID{}
	if s.Validate() == nil {
		t.Fatal("empty owner accepted")
	}
}
