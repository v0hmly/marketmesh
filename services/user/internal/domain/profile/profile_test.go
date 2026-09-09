package profile

import (
	"strings"
	"testing"
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
