package securitymail

import (
	"html"
	"strings"
	"testing"
	"time"

	"github.com/v0hmly/marketmesh/platform/mail"
	application "github.com/v0hmly/marketmesh/services/auth/internal/application/security"
)

func TestLocalTimestamp(t *testing.T) {
	for _, tt := range []struct{ instant, zone, want string }{
		{"2026-09-24T08:32:00Z", "Europe/Moscow", "24 сентября 2026, 11:32 (UTC+3)"},
		{"2026-09-24T23:32:00Z", "Asia/Kathmandu", "25 сентября 2026, 05:17 (UTC+5:45)"},
		{"2026-01-24T08:32:00Z", "America/New_York", "24 января 2026, 03:32 (UTC−5)"},
		{"2026-09-24T08:32:00Z", "America/New_York", "24 сентября 2026, 04:32 (UTC−4)"},
		{"2026-10-25T00:30:00Z", "Europe/Berlin", "25 октября 2026, 02:30 (UTC+2)"},
		{"2026-10-25T01:30:00Z", "Europe/Berlin", "25 октября 2026, 02:30 (UTC+1)"},
		{"2026-09-24T08:32:00Z", "", "24 сентября 2026, 08:32 (UTC)"},
		{"2026-09-24T08:32:00Z", "Local", "24 сентября 2026, 08:32 (UTC)"},
		{"2026-09-24T08:32:00Z", "../../etc/passwd", "24 сентября 2026, 08:32 (UTC)"},
	} {
		at, err := time.Parse(time.RFC3339, tt.instant)
		if err != nil {
			t.Fatal(err)
		}
		if got := localTimestamp(at, tt.zone); got != tt.want {
			t.Errorf("%s: %q != %q", tt.zone, got, tt.want)
		}
	}
}

func TestRequestLifetimeDoesNotRoundUpOrRestart(t *testing.T) {
	start := time.Date(2026, 9, 23, 8, 32, 0, 0, time.UTC)
	for _, tt := range []struct {
		duration time.Duration
		want     string
	}{
		{24 * time.Hour, "24 часа"}, {time.Hour, "1 час"}, {11 * time.Hour, "11 часов"},
		{10 * time.Minute, "10 минут"}, {time.Minute, "1 минуту"}, {21 * time.Minute, "21 минуту"},
		{2 * time.Minute, "2 минуты"}, {11 * time.Minute, "11 минут"},
		{8*time.Minute + 59*time.Second, "8 минут 59 секунд"}, {61 * time.Second, "1 минуту 1 секунду"},
	} {
		if got := requestLifetime(start, start.Add(tt.duration)); got != tt.want+" с момента запроса" {
			t.Errorf("%s: %s", tt.duration, got)
		}
	}
}

func TestMailDeadlineIsFrozenInBothAlternatives(t *testing.T) {
	renderer, err := mail.NewRenderer(mail.Brand{Domain: "example.test", SupportEmail: "help@example.test", SellersSupportEmail: "sellers@example.test", ITSupportEmail: "it@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	sender, err := New(renderer, &transportStub{}, "https://example.test")
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 23, 8, 32, 0, 0, time.UTC)
	for _, kind := range []string{"verify", "reset", "change_email", "cancel_email", "code"} {
		m := application.Mail{Kind: kind, Email: "buyer@example.test", OtherEmail: "old@example.test", Code: "123456", URL: "https://example.test/account/security/verify#token=opaque", At: at, ExpiresAt: at.Add(24 * time.Hour), TimeZone: "Europe/Moscow"}
		message, err := sender.Render(m)
		if err != nil {
			t.Fatal(err)
		}
		repeated, err := sender.Render(m)
		if err != nil {
			t.Fatal(err)
		}
		if string(message.Text) != string(repeated.Text) || string(message.HTML) != string(repeated.HTML) {
			t.Fatal("retry changed deadline")
		}
		for _, body := range []string{html.UnescapeString(string(message.HTML)), string(message.Text)} {
			if !strings.Contains(body, "24 часа с момента запроса") || !strings.Contains(body, "24 сентября 2026, 11:32 (UTC+3)") || strings.Contains(body, "осталось") {
				t.Fatalf("%s: duration or exact local deadline missing", kind)
			}
		}
	}
}
