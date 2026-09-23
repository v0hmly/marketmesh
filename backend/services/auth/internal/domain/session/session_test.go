package session

import (
	"encoding/base64"
	"fmt"
	"strings"
	"testing"
)

func testID() ID {
	var id ID
	for i := range id {
		id[i] = byte(i + 1)
	}
	return id
}

func TestTokenCanonicalRoundTrip(t *testing.T) {
	id := testID()
	secret := make([]byte, 32)
	for i := range secret {
		secret[i] = byte(255 - i)
	}

	token, err := NewToken(id, secret)
	if err != nil {
		t.Fatalf("NewToken() error = %v", err)
	}
	if got := len(token.Reveal()); got != 80 {
		t.Fatalf("token length = %d, want 80", got)
	}
	want := "mm1." + id.String() + "." + base64.RawURLEncoding.EncodeToString(secret)
	if got := token.Reveal(); got != want {
		t.Fatalf("token = %q, want canonical %q", got, want)
	}

	parsed, err := ParseToken(token.Reveal())
	if err != nil {
		t.Fatalf("ParseToken() error = %v", err)
	}
	if parsed.ID() != id || parsed.Reveal() != want || parsed.Digest() != token.Digest() {
		t.Fatal("parsed token did not preserve ID, value, and digest")
	}
}

func TestIDRejectsZeroAndNonCanonicalValues(t *testing.T) {
	if _, err := NewToken(ID{}, make([]byte, 32)); err != ErrInvalidSession {
		t.Fatalf("NewToken(zero ID) error = %v, want ErrInvalidSession", err)
	}
	if _, err := NewToken(testID(), make([]byte, 31)); err != ErrInvalidSession {
		t.Fatalf("NewToken(short secret) error = %v, want ErrInvalidSession", err)
	}

	valid := testID().String()
	for _, value := range []string{"", strings.Repeat("0", 32), valid[:31], strings.ToUpper(valid)} {
		if _, err := ParseID(value); err != ErrInvalidSession {
			t.Errorf("ParseID(%q) error = %v, want ErrInvalidSession", value, err)
		}
	}
	parsed, err := ParseID(valid)
	if err != nil || parsed != testID() {
		t.Fatalf("ParseID(valid) = (%v, %v)", parsed, err)
	}
}

func TestParseTokenRejectsMalformedAndNonCanonicalValues(t *testing.T) {
	id := testID()
	valid, err := NewToken(id, make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	value := valid.Reveal()
	parts := strings.Split(value, ".")
	badPaddingBits := parts[0] + "." + parts[1] + "." + parts[2][:42] + "B"

	values := []string{
		"", value[:79], value + "x",
		"MM1." + parts[1] + "." + parts[2],
		"mm1." + strings.ToUpper(parts[1]) + "." + parts[2],
		"mm1." + parts[1] + "." + parts[2] + "=",
		"mm1." + parts[1] + "." + strings.Repeat("!", 43),
		badPaddingBits,
	}
	for _, malformed := range values {
		if _, err := ParseToken(malformed); err != ErrInvalidSession {
			t.Errorf("ParseToken(%q) error = %v, want ErrInvalidSession", malformed, err)
		}
	}
}

func TestDigestsAreCredentialSpecific(t *testing.T) {
	one, _ := NewToken(testID(), make([]byte, 32))
	secret := make([]byte, 32)
	secret[31] = 1
	two, _ := NewToken(testID(), secret)
	if one.Digest() == two.Digest() {
		t.Fatal("different bearer credentials produced the same digest")
	}
}

func TestSensitiveFormattingIsRedacted(t *testing.T) {
	token, _ := NewToken(testID(), make([]byte, 32))
	digest := token.Digest()

	for name, value := range map[string]any{
		"Token %v":          token,
		"Token %#v":         token,
		"Token pointer %v":  &token,
		"Digest %v":         digest,
		"Digest %#v":        digest,
		"Digest pointer %v": &digest,
	} {
		verb := "%v"
		if strings.Contains(name, "%#v") {
			verb = "%#v"
		}
		got := fmt.Sprintf(verb, value)
		if !strings.Contains(got, "[REDACTED]") || strings.Contains(got, token.Reveal()) {
			t.Errorf("%s exposed sensitive value: %q", name, got)
		}
	}
}
