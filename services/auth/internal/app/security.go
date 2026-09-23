package app

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/v0hmly/marketmesh/platform/logger"
	"github.com/v0hmly/marketmesh/platform/mail"
	pg "github.com/v0hmly/marketmesh/platform/postgres"
	runtime "github.com/v0hmly/marketmesh/platform/runtime"
	"github.com/v0hmly/marketmesh/services/auth/internal/adapter/out/postgressecurity"
	"github.com/v0hmly/marketmesh/services/auth/internal/adapter/out/registrationevent"
	"github.com/v0hmly/marketmesh/services/auth/internal/adapter/out/securitymail"
	application "github.com/v0hmly/marketmesh/services/auth/internal/application/security"
)

type securityConfig struct {
	enabled                 bool
	keyFile, origin, domain string
	smtp                    mail.SMTPConfig
}

func loadSecurityConfig(env runtime.Env, c config) (securityConfig, error) {
	var result securityConfig
	var err error
	result.enabled, err = env.Bool("AUTH_EMAIL_ENABLED", false)
	if err != nil || !result.enabled {
		return result, err
	}
	if !c.sessions.enabled || !c.registration.enabled {
		return result, errors.New("auth email: sessions and registration outbox must be enabled")
	}
	if result.keyFile, err = env.RequiredString("AUTH_EMAIL_KEY_FILE"); err != nil {
		return result, err
	}
	if !filepath.IsAbs(result.keyFile) {
		return result, errors.New("auth email: key file must be absolute")
	}
	if result.origin, err = env.RequiredString("AUTH_EMAIL_PUBLIC_ORIGIN"); err != nil {
		return result, err
	}
	if !slices.Contains(c.sessions.allowedOrigins, result.origin) {
		return result, errors.New("auth email: public origin must match the browser allowlist")
	}
	if result.domain, err = env.String("AUTH_EMAIL_DOMAIN", "marketmesh.test"); err != nil {
		return result, err
	}
	if result.smtp.Address, err = env.RequiredString("AUTH_SMTP_ADDRESS"); err != nil {
		return result, err
	}
	if result.smtp.Username, err = env.String("AUTH_SMTP_USERNAME", ""); err != nil {
		return result, err
	}
	password, err := env.Secret("AUTH_SMTP_PASSWORD", false)
	if err != nil {
		return result, err
	}
	result.smtp.Password = password.Reveal()
	if result.smtp.PlaintextReason, err = env.String("AUTH_SMTP_PLAINTEXT_REASON", ""); err != nil {
		return result, err
	}
	result.smtp.Environment = c.environment
	result.smtp.Timeout = 10 * time.Second
	if _, err := mail.NewSMTP(result.smtp); err != nil {
		return result, err
	}
	return result, nil
}

type securityResources struct {
	service    *application.Service
	component  runtime.Component
	dependency runtime.CriticalDependency
}

func newSecurityResources(ctx context.Context, c config, db *pg.Database, hasher application.PasswordHasher, sessions *sessionResources, log *logger.Logger) (*securityResources, error) {
	if !c.security.enabled {
		// Once the security schema is deployed, disabling the runtime must not
		// restore password-only Login and bypass verification or an enabled code.
		var installed bool
		checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := db.RW().QueryRow(checkCtx, `SELECT to_regclass('auth.account_security') IS NOT NULL`).Scan(&installed); err != nil {
			return nil, errors.New("auth email: cannot verify legacy mode compatibility")
		}
		if installed {
			return nil, errors.New("auth email: security schema requires AUTH_EMAIL_ENABLED")
		}
		return nil, nil
	}
	key, err := readSecurityKey(c.security.keyFile)
	if err != nil {
		return nil, err
	}
	defer clear(key[:])
	store, err := postgressecurity.New(db, key)
	if err != nil {
		return nil, err
	}
	service, err := application.New(store, hasher, sessions.service, registrationevent.New(), application.Config{Key: key, Origin: c.security.origin})
	if err != nil {
		return nil, err
	}
	transport, err := mail.NewSMTP(c.security.smtp)
	if err != nil {
		return nil, err
	}
	domain := c.security.domain
	renderer, err := mail.NewRenderer(mail.Brand{Domain: domain, SupportEmail: "help@" + domain, SellersSupportEmail: "sellers@" + domain, ITSupportEmail: "it@" + domain})
	if err != nil {
		return nil, err
	}
	sender, err := securitymail.New(renderer, transport, c.security.origin)
	if err != nil {
		return nil, err
	}
	worker, err := application.NewMailWorker(store, sender, mailObserver{log})
	if err != nil {
		return nil, err
	}
	return &securityResources{service: service, component: runtime.Component{Name: "auth-mail-worker", Run: worker.Run, Shutdown: func(context.Context) error { return nil }}, dependency: runtime.CriticalDependency{Name: "auth-security-schema", Check: func(ctx context.Context) error {
		var ready bool
		err := db.RW().QueryRow(ctx, `SELECT bool_and(has_table_privilege(current_user, 'auth.' || t.name, p.permission))
FROM (VALUES ('account_security'), ('security_challenges'), ('mail_outbox'), ('login_limits'), ('recovery_codes')) AS t(name)
CROSS JOIN (VALUES ('SELECT'), ('INSERT'), ('UPDATE'), ('DELETE')) AS p(permission)`).Scan(&ready)
		if err != nil || !ready {
			return errors.New("auth email: security schema unavailable")
		}
		return nil
	}}}, nil
}

type mailObserver struct{ log *logger.Logger }

func (o mailObserver) MailResult(outcome string) {
	if outcome != "delivered" {
		o.log.Warn("доставка письма Auth", logger.String("outcome", outcome))
	}
}
func readSecurityKey(path string) ([32]byte, error) {
	var key [32]byte
	file, err := os.Open(path)
	if err != nil {
		return key, errors.New("auth email: key unavailable")
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() != 32 {
		return key, errors.New("auth email: key must be a private regular 32-byte file")
	}
	if _, err := io.ReadFull(file, key[:]); err != nil || key == ([32]byte{}) {
		return [32]byte{}, errors.New("auth email: invalid key")
	}
	return key, nil
}
