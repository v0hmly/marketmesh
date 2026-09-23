package mailtime

import (
	"strings"
	"testing"
	"time"
)

func TestUntrustedDisplayPreferenceFallsBackToUTC(t *testing.T) {
	for _, name := range []string{"", "Local", "/etc/localtime", "../Europe/Moscow", "Europe/../Moscow", "Europe/Moscow\r\nx:y", "<script>", strings.Repeat("a", 129), "Not/AZone", "Europe/Moscow,UTC"} {
		if Location(name) != time.UTC {
			t.Errorf("unsafe preference accepted: %q", name)
		}
	}
	for _, name := range []string{"Europe/Moscow", "America/New_York", "Asia/Kathmandu", "UTC"} {
		if Location(name).String() != name {
			t.Errorf("valid zone rejected: %s", name)
		}
	}
}
