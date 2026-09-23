// Package securitymail renders typed Auth notifications and sends them to a relay.
package securitymail

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/v0hmly/marketmesh/platform/mail"
	application "github.com/v0hmly/marketmesh/services/auth/internal/application/security"
)

var errInvalidMail = errors.New("auth mail: invalid queued message")

type Transport interface {
	Send(context.Context, string, string, mail.Message) error
}
type Sender struct {
	renderer  *mail.Renderer
	transport Transport
	origin    string
}

func New(renderer *mail.Renderer, transport Transport, origin string) (*Sender, error) {
	if renderer == nil || transport == nil || origin == "" {
		return nil, errInvalidMail
	}
	return &Sender{renderer, transport, origin}, nil
}
func (s *Sender) Permanent(err error) bool {
	var delivery *mail.DeliveryError
	return errors.Is(err, errInvalidMail) || errors.As(err, &delivery) && !delivery.Retryable
}
func (s *Sender) Send(ctx context.Context, m application.Mail) error {
	message, err := s.Render(m)
	if err != nil {
		return errInvalidMail
	}
	return s.transport.Send(ctx, m.Email, hex.EncodeToString(m.ID[:]), message)
}
func (s *Sender) Render(m application.Mail) (mail.Message, error) {
	var kind mail.Template
	var data any
	stamp := m.At.UTC().Format("02.01.2006 15:04 UTC")
	ttl := fmt.Sprintf("до %s", m.ExpiresAt.UTC().Format("02.01.2006 15:04 UTC"))
	// The browser bridge intentionally carries no spoofable IP/location claims.
	const unknown = "Не определяется"
	switch m.Kind {
	case "verify":
		kind = mail.TemplateVerify
		data = mail.VerifyData{RecipientEmail: m.Email, ConfirmURL: m.URL, LinkTTL: ttl}
	case "code":
		kind = mail.TemplateCode
		data = mail.CodeData{RecipientEmail: m.Email, Code: m.Code, CodeTTL: ttl, PasswordResetURL: m.URL}
	case "reset":
		kind = mail.TemplatePasswordReset
		data = mail.PasswordResetData{RecipientEmail: m.Email, ResetURL: m.URL, LinkTTL: ttl}
	case "change_email":
		kind = mail.TemplateEmailChangeConfirm
		data = mail.EmailChangeConfirmData{OldEmail: m.OtherEmail, NewEmail: m.Email, ConfirmURL: m.URL, LinkTTL: ttl}
	case "cancel_email":
		kind = mail.TemplateEmailChangeAlert
		data = mail.EmailChangeAlertData{OldEmail: m.Email, NewEmail: m.OtherEmail, Timestamp: stamp, IP: unknown, CancelURL: m.URL, CancelTTL: ttl}
	case "new_login":
		kind = mail.TemplateNewLogin
		data = mail.NewLoginData{RecipientEmail: m.Email, Timestamp: stamp, Device: unknown, Browser: unknown, IP: unknown, Location: unknown, PasswordResetURL: m.URL}
	case "password_changed":
		kind = mail.TemplatePasswordChanged
		data = mail.PasswordChangedData{RecipientEmail: m.Email, Timestamp: stamp, DeviceBrowser: unknown, IP: unknown, RecoveryURL: m.URL}
	case "account_locked":
		kind = mail.TemplateAccountLocked
		data = mail.AccountLockedData{RecipientEmail: m.Email, Timestamp: stamp, LockDuration: "до 15 минут", Attempts: 5, IP: unknown, Location: unknown, PasswordResetURL: m.URL}
	case "sessions_closed":
		kind = mail.TemplateSessionsClosed
		data = mail.SessionsClosedData{RecipientEmail: m.Email, Timestamp: stamp, LoginURL: s.origin + "/login"}
	case "two_factor_on":
		kind = mail.TemplateTwoFactorOn
		data = mail.TwoFactorOnData{RecipientEmail: m.Email, SecurityURL: s.origin + "/account/security"}
	case "two_factor_off":
		kind = mail.TemplateTwoFactorOff
		data = mail.TwoFactorOffData{RecipientEmail: m.Email, Timestamp: stamp, DeviceBrowser: unknown, IP: unknown, EnableURL: s.origin + "/account/security"}
	case "backup_codes":
		kind = mail.TemplateBackupCodes
		data = mail.BackupCodesData{RecipientEmail: m.Email, CodesCount: application.RecoveryCodeCount, BackupCodesURL: s.origin + "/account/security"}
	case "cancel_deletion":
		kind = mail.TemplateAccountDeleted
		data = mail.AccountDeletedData{RecipientEmail: m.Email, DeletionDate: m.ExpiresAt.UTC().Format(time.DateOnly), CancelURL: m.URL}
	default:
		return mail.Message{}, errInvalidMail
	}
	return s.renderer.Render(kind, data)
}
