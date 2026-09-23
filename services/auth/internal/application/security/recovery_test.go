package security

import (
	"fmt"
	"strings"
	"testing"
)

func TestRecoveryCodeInputHasAnUnambiguousBoundedEncoding(t *testing.T) {
	const raw = "abcdef0123456789abcdef0123456789"
	for _, value := range []string{raw, strings.ToUpper(raw), " ABCDEF01-23456789-ABCDEF01-23456789\n"} {
		got, ok := normalizeRecoveryCode(value)
		if !ok || got != raw {
			t.Fatal("valid recovery representation rejected")
		}
	}
	for _, value := range []string{"", "123456", raw + "0", raw[:31], "abcdef0-123456789-abcdef01-23456789", "abcdef01_23456789-abcdef01-23456789", "abcdef01-23456789-abcdef01-2345678g", strings.Repeat("а", 32)} {
		if _, ok := normalizeRecoveryCode(value); ok {
			t.Fatal("ambiguous recovery representation accepted")
		}
	}
}

func TestRecoveryCodesDoNotRevealSecretsThroughFormatting(t *testing.T) {
	codes := RecoveryCodes{"sensitive-value"}
	for _, format := range []string{"%s", "%v", "%+v", "%#v"} {
		if strings.Contains(fmt.Sprintf(format, codes), "sensitive-value") {
			t.Fatal("format leaked recovery code")
		}
	}
}
