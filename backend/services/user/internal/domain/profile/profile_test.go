package profile

import (
	"strings"
	"testing"
	"time"
)

func TestFieldsBoundsAndNormalization(t *testing.T) {
	for _, tc := range []struct {
		name, display, bio string
		valid              bool
	}{
		{"empty", "", "", true}, {"trim", "  Alice  ", "line\n\ttwo", true},
		{"unicode boundary", strings.Repeat("界", 80), strings.Repeat("界", 1000), true},
		{"display too long", strings.Repeat("界", 81), "", false}, {"bio too long", "", strings.Repeat("x", 1001), false},
		{"display control", "a\nb", "", false}, {"bio control", "", "secret\x00", false},
		{"invalid utf8", "\xff", "", false}, {"raw bytes bounded", strings.Repeat(" ", 321), "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, e := NewFields(tc.display, tc.bio)
			if (e == nil) != tc.valid {
				t.Fatalf("valid=%v error=%v", tc.valid, e)
			}
			if tc.name == "trim" && f.DisplayName != "Alice" {
				t.Fatal("display not normalized")
			}
		})
	}
}

func TestIdentityFieldsCalendarBoundsAndNormalization(t *testing.T) {
	now := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)
	valid := Fields{DisplayName: " Вера ", LastName: " Ильина ", BirthDate: "2000-02-29", Gender: GenderFemale, Phone: " +7 (921) 000-11-22 ", City: " Санкт-Петербург ", ShowAge: true}
	got, err := NormalizeFields(valid, now)
	if err != nil || got.DisplayName != "Вера" || got.LastName != "Ильина" || got.City != "Санкт-Петербург" || got.Phone != "+7 (921) 000-11-22" || got.BirthDate != valid.BirthDate || !got.ShowAge {
		t.Fatal("identity normalization failed")
	}
	for _, date := range []string{"", "0001-01-01", "2026-09-22"} {
		if _, err := NormalizeFields(Fields{BirthDate: date}, now); err != nil {
			t.Fatalf("valid calendar date rejected: %s", date)
		}
	}
	for _, date := range []string{"0000-01-01", "1900-02-29", "2026-02-30", "2026-9-02", "2026-09-23", "2026-00-01", "2026-01-00", "2026-09-22T00:00:00Z"} {
		if _, err := NormalizeFields(Fields{BirthDate: date}, now); err == nil {
			t.Fatalf("invalid calendar date accepted: %s", date)
		}
	}
	for name, fields := range map[string]Fields{
		"last name control": {LastName: "\tИмя"}, "name control before trim": {DisplayName: "\nИмя"},
		"last name bytes": {LastName: strings.Repeat(" ", 321)}, "last name runes": {LastName: strings.Repeat("😀", 81)},
		"city control": {City: "x\u0085y"}, "city bytes": {City: strings.Repeat(" ", 481)}, "city runes": {City: strings.Repeat("界", 121)},
		"malformed UTF8": {City: "\xff"}, "gender negative": {Gender: -1}, "gender unknown": {Gender: 65536},
		"phone letters": {Phone: "1234567доб"}, "phone unicode": {Phone: "１２３４５６７"}, "phone control": {Phone: "1234567\n"},
		"phone too short": {Phone: "123456"}, "phone too long": {Phone: "1234567890123456"}, "phone bytes": {Phone: strings.Repeat(" ", 26) + "1234567"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := NormalizeFields(fields, now); err == nil {
				t.Fatal("invalid private fields accepted")
			}
		})
	}
	if _, err := NormalizeFields(Fields{LastName: strings.Repeat("😀", 80), City: strings.Repeat("😀", 120), Phone: "123456789012345"}, now); err != nil {
		t.Fatal("Unicode and phone boundary rejected")
	}
	// A local calendar may still show the preceding day; the boundary is UTC.
	if _, err := NormalizeFields(Fields{BirthDate: "2026-09-23"}, time.Date(2026, 9, 22, 23, 0, 0, 0, time.FixedZone("test", -12*3600))); err != nil {
		t.Fatal("date boundary ignored UTC")
	}
}
func TestSubjectIDCopiesAndRejectsZero(t *testing.T) {
	if _, e := NewSubjectID(make([]byte, 16)); e == nil {
		t.Fatal("zero accepted")
	}
	if _, e := NewSubjectID([]byte{1}); e == nil {
		t.Fatal("short accepted")
	}
	raw := make([]byte, 16)
	raw[0] = 1
	id, e := NewSubjectID(raw)
	if e != nil {
		t.Fatal(e)
	}
	raw[0] = 2
	out := id.Bytes()
	out[0] = 3
	if id[0] != 1 {
		t.Fatal("subject aliases caller bytes")
	}
}
