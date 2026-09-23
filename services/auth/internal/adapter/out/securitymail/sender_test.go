package securitymail

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/v0hmly/marketmesh/platform/mail"
	application "github.com/v0hmly/marketmesh/services/auth/internal/application/security"
	domain "github.com/v0hmly/marketmesh/services/auth/internal/domain/security"
)

type transportStub struct {
	recipient, id string
	message       mail.Message
}

func (s *transportStub) Send(_ context.Context, recipient, id string, message mail.Message) error {
	s.recipient, s.id, s.message = recipient, id, message
	return nil
}
func TestSecurityNotificationsRenderRealActions(t *testing.T) {
	renderer, err := mail.NewRenderer(mail.Brand{Domain: "example.test", SupportEmail: "help@example.test", SellersSupportEmail: "sellers@example.test", ITSupportEmail: "it@example.test"})
	if err != nil {
		t.Fatal(err)
	}
	transport := &transportStub{}
	sender, err := New(renderer, transport, "https://example.test")
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"verify", "code", "recovery_code", "reset", "change_email", "cancel_email", "new_login", "password_changed", "account_locked", "sessions_closed", "two_factor_on", "two_factor_off", "backup_codes", "cancel_deletion"} {
		t.Run(kind, func(t *testing.T) {
			m := application.Mail{ID: domain.ID{1}, Kind: kind, Email: "buyer@example.test", OtherEmail: "previous@example.test", Code: "123456", URL: "https://example.test/account/security/reset#token=opaque", At: time.Now(), ExpiresAt: time.Now().Add(time.Minute)}
			if err := sender.Send(t.Context(), m); err != nil {
				t.Fatal(err)
			}
			if transport.recipient != m.Email || transport.id != "01000000000000000000000000000000" || len(transport.message.HTML) == 0 || len(transport.message.Text) == 0 {
				t.Fatal("invalid delivery envelope")
			}
			body := string(transport.message.Text)
			if strings.Contains(body, "/account/login") || strings.Contains(body, "кроме текущей") || strings.Contains(body, "Раньше с этого устройства") {
				t.Fatal("notification contradicts actual behavior")
			}
			if kind == "recovery_code" && (transport.message.Subject != "Подтвердите замену резервных кодов" || !strings.Contains(body, "настройках безопасности") || strings.Contains(body, "странице входа") || !strings.Contains(body, "прежние резервные коды")) {
				t.Fatal("recovery proof describes the wrong security action")
			}
			if kind == "backup_codes" && (strings.Contains(body, m.Code) || strings.Contains(body, "откройте их") || !strings.Contains(body, "один раз")) {
				t.Fatal("recovery notice contains a code or misleading replay promise")
			}
			if kind == "two_factor_on" && (strings.Contains(body, "резервные коды") || !strings.Contains(body, "https://example.test/account/security")) {
				t.Fatal("unavailable action advertised")
			}
		})
	}
	if err := sender.Send(t.Context(), application.Mail{Kind: "unknown"}); err == nil || !sender.Permanent(err) {
		t.Fatal("unknown template not rejected permanently")
	}
}
