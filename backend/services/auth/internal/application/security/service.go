package security

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/url"
	"time"

	applicationsession "github.com/v0hmly/marketmesh/services/auth/internal/application/session"
	"github.com/v0hmly/marketmesh/services/auth/internal/domain/credential"
	domain "github.com/v0hmly/marketmesh/services/auth/internal/domain/security"
	"github.com/v0hmly/marketmesh/services/auth/internal/domain/session"
)

const codeTTL = 10 * time.Minute
const resendDelay = time.Minute

type Service struct {
	store    Store
	hasher   PasswordHasher
	sessions Sessions
	events   RegistrationEvents
	key      [32]byte
	origin   string
	clock    func() time.Time
}

type Config struct {
	Key    [32]byte
	Origin string
	Clock  func() time.Time
}

func New(store Store, hasher PasswordHasher, sessions Sessions, events RegistrationEvents, config Config) (*Service, error) {
	if store == nil || hasher == nil || sessions == nil || events == nil || config.Key == ([32]byte{}) {
		return nil, errors.New("auth security: missing dependencies")
	}
	u, err := url.Parse(config.Origin)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return nil, errors.New("auth security: invalid public HTTPS origin")
	}
	if config.Clock == nil {
		config.Clock = time.Now
	}
	return &Service{store: store, hasher: hasher, sessions: sessions, events: events, key: config.Key, origin: config.Origin, clock: config.Clock}, nil
}

func (s *Service) now() time.Time { return s.clock().UTC().Truncate(time.Second) }

func (s *Service) Register(ctx context.Context, email string, raw []byte) error {
	return s.register(ctx, email, raw, "")
}

// RegisterBrowser binds the first login to a separate browser secret. Duplicate
// addresses get an indistinguishable random secret, without changing their account.
func (s *Service) RegisterBrowser(ctx context.Context, email string, raw []byte) (string, error) {
	secret, err := newSecret()
	if err != nil {
		return "", err
	}
	if err := s.register(ctx, email, raw, secret); err != nil {
		return "", err
	}
	return secret, nil
}

func (s *Service) register(ctx context.Context, email string, raw []byte, browserSecret string) error {
	email, err := domain.Email(email)
	if err != nil {
		return err
	}
	pw, err := domain.NewPassword(raw)
	if err != nil {
		return err
	}
	defer pw.Destroy()
	plain := pw.Bytes()
	defer clear(plain)
	digest, err := s.hasher.Hash(plain)
	if err != nil {
		return domain.Unavailable
	}
	id, err := newID()
	if err != nil {
		return err
	}
	subject := credential.SubjectID(id)
	identifier, err := credential.NewIdentifier(email)
	if err != nil {
		return domain.InvalidInput
	}
	event, err := s.events.New(ctx, subject)
	if err != nil {
		return domain.Unavailable
	}
	account := domain.Account{Subject: subject, Email: email, PasswordDigest: digest, Revision: 1, CodeEnabled: true}
	challenge, mail, err := s.tokenChallenge(ctx, account, domain.VerifyEmail, email, 24*time.Hour)
	if err != nil {
		return err
	}
	if browserSecret != "" {
		challenge.RegistrationDigest = s.digest("registration-browser", domain.ID(subject), browserSecret)
	}
	return safe(s.store.Register(ctx, credential.New(subject, identifier, digest), event, challenge, mail))
}

type LoginResult struct {
	ChallengeID domain.ID
	ExpiresIn   time.Duration
	Tokens      applicationsession.Tokens
}

func (s *Service) Login(ctx context.Context, email string, raw []byte, allowChallenge bool) (LoginResult, error) {
	email, err := domain.Email(email)
	if err != nil {
		return LoginResult{}, domain.InvalidCredentials
	}
	pw, err := credential.NewPassword(raw)
	if err != nil {
		return LoginResult{}, domain.InvalidCredentials
	}
	defer pw.Destroy()
	plain := pw.Bytes()
	defer clear(plain)
	var result LoginResult
	var outcome error
	now := s.now()
	bucket := s.digest("login-limit", domain.ID{}, email)
	err = s.store.WithAccount(ctx, Selector{Email: email}, func(tx Unit) error {
		attempt, err := tx.ReserveAttempt(ctx, bucket, now)
		if err != nil {
			return err
		}
		if attempt > 5 {
			outcome = domain.LoginLocked
			return nil
		}
		account := tx.Account()
		valid, rehash := false, false
		if account == nil {
			err = s.hasher.EqualizeMissing(plain)
		} else {
			valid, rehash, err = s.hasher.Verify(plain, account.PasswordDigest)
		}
		if err != nil {
			return domain.Unavailable
		}
		if !valid || account == nil || account.DeletionAt != nil || account.DeletedAt != nil {
			outcome = domain.InvalidCredentials
			if attempt == 5 {
				outcome = domain.LoginLocked
				if account != nil && account.DeletedAt == nil {
					if err := s.notice(ctx, tx, "account_locked"); err != nil {
						return err
					}
				}
			}
			return nil
		}
		if err := tx.ResetAttempts(ctx, bucket); err != nil {
			return err
		}
		if !account.Verified {
			outcome = domain.EmailUnverified
			return nil
		}
		if rehash {
			account.PasswordDigest, err = s.hasher.Hash(plain)
			if err != nil {
				return domain.Unavailable
			}
		}
		if account.CodeEnabled {
			if !allowChallenge {
				outcome = domain.CodeRequired
				return nil
			}
			challenge, found, err := tx.LatestChallenge(ctx, domain.LoginCode)
			if err != nil {
				return err
			}
			if found && challenge.Check(*account, domain.LoginCode, now) == nil {
				if now.Sub(challenge.SentAt) < resendDelay {
					outcome = domain.RateLimited
					return nil
				}
				if err := s.replaceCode(ctx, tx, &challenge); err != nil {
					if domain.IsExpected(err) {
						outcome = err
						return nil
					}
					return err
				}
			} else {
				challenge, err = s.createCode(ctx, tx, domain.LoginCode)
				if err != nil {
					return err
				}
			}
			result.ChallengeID, result.ExpiresIn = challenge.ID, challenge.ExpiresAt.Sub(now)
			return nil
		}
		result.Tokens, err = s.issue(ctx, tx)
		return err
	})
	if err != nil {
		return LoginResult{}, safe(err)
	}
	if outcome != nil {
		return LoginResult{}, outcome
	}
	if result.Tokens.Record.ID != (session.ID{}) {
		result.Tokens, err = s.sessions.Activate(ctx, result.Tokens)
	}
	return result, safe(err)
}

func (s *Service) CompleteLogin(ctx context.Context, id domain.ID, code string) (applicationsession.Tokens, error) {
	var tokens applicationsession.Tokens
	err := s.withChallenge(ctx, id, func(tx Unit, challenge *domain.Challenge) error {
		if err := s.checkCode(ctx, tx, challenge, domain.LoginCode, code); err != nil {
			return err
		}
		account := tx.Account()
		if !account.Verified || !account.CodeEnabled || account.DeletionAt != nil {
			return domain.CodeExpired
		}
		now := s.now()
		challenge.UsedAt = &now
		if err := tx.SaveChallenge(ctx, *challenge); err != nil {
			return err
		}
		var err error
		tokens, err = s.issue(ctx, tx)
		return err
	})
	if err != nil {
		return applicationsession.Tokens{}, err
	}
	return s.sessions.Activate(ctx, tokens)
}

func (s *Service) ResendCode(ctx context.Context, id domain.ID) (time.Duration, error) {
	var ttl time.Duration
	err := s.withChallenge(ctx, id, func(tx Unit, c *domain.Challenge) error {
		if err := c.Check(*tx.Account(), domain.LoginCode, s.now()); err != nil {
			return domain.CodeExpired
		}
		if s.now().Sub(c.SentAt) < resendDelay {
			return domain.RateLimited
		}
		if err := s.replaceCode(ctx, tx, c); err != nil {
			return err
		}
		ttl = c.ExpiresAt.Sub(s.now())
		return nil
	})
	return ttl, err
}

func (s *Service) issue(ctx context.Context, tx Unit) (applicationsession.Tokens, error) {
	tokens, err := s.sessions.Prepare(tx.Account().Subject)
	if err != nil {
		return applicationsession.Tokens{}, domain.Unavailable
	}
	if err := tx.CreateSession(ctx, tokens.Record, tokens.Refresh.Digest()); err != nil {
		return applicationsession.Tokens{}, err
	}
	if err := s.notice(ctx, tx, "new_login"); err != nil {
		return applicationsession.Tokens{}, err
	}
	return tokens, nil
}

// withChallenge locks the owning account before reading/consuming the challenge.
// Expected rejections commit counters/reissued codes; infrastructure errors roll back.
func (s *Service) withChallenge(ctx context.Context, id domain.ID, apply func(Unit, *domain.Challenge) error) error {
	if id == (domain.ID{}) {
		return domain.TokenExpired
	}
	subject, err := s.store.ChallengeSubject(ctx, id)
	if err != nil {
		return safe(err)
	}
	var outcome error
	err = s.store.WithAccount(ctx, Selector{Subject: subject}, func(tx Unit) error {
		if tx.Account() == nil {
			outcome = domain.TokenExpired
			return nil
		}
		challenge, err := tx.Challenge(ctx, id)
		if err != nil {
			return err
		}
		err = apply(tx, &challenge)
		if domain.IsExpected(err) {
			outcome = err
			return nil
		}
		return err
	})
	if err != nil {
		return safe(err)
	}
	return outcome
}

func (s *Service) createCode(ctx context.Context, tx Unit, purpose domain.Purpose) (domain.Challenge, error) {
	id, err := newID()
	if err != nil {
		return domain.Challenge{}, err
	}
	c := domain.Challenge{ID: id, Subject: tx.Account().Subject, Purpose: purpose, Revision: tx.Account().Revision, ExpiresAt: s.now().Add(codeTTL)}
	err = s.replaceCode(ctx, tx, &c)
	return c, err
}
func (s *Service) replaceCode(ctx context.Context, tx Unit, c *domain.Challenge) error {
	if c.Sends >= 5 {
		return domain.CodeExpired
	}
	budget := s.digest("code-mail-budget", domain.ID(c.Subject), string(c.Purpose))
	n, err := tx.ReserveBudget(ctx, budget, s.now(), 5, codeTTL)
	if err != nil {
		return err
	}
	if n > 5 {
		return domain.RateLimited
	}
	code, err := newCode()
	if err != nil {
		return err
	}
	next := s.digest(string(c.Purpose), c.ID, code)
	for next == c.Digest {
		code, err = newCode()
		if err != nil {
			return err
		}
		next = s.digest(string(c.Purpose), c.ID, code)
	}
	c.Digest = next
	c.Attempts = 0
	c.Sends++
	c.SentAt = s.now()
	if err := tx.SaveChallenge(ctx, *c); err != nil {
		return err
	}
	id, err := newID()
	if err != nil {
		return err
	}
	return tx.Queue(ctx, Mail{TimeZone: mailTimeZone(ctx), ID: id, Subject: c.Subject, Kind: "code", Email: tx.Account().Email, Code: code, URL: s.origin + "/account/security/reset", At: s.now(), ExpiresAt: c.ExpiresAt})
}
func (s *Service) checkCode(ctx context.Context, tx Unit, c *domain.Challenge, purpose domain.Purpose, code string) error {
	if err := c.Check(*tx.Account(), purpose, s.now()); err != nil {
		return domain.CodeExpired
	}
	if len(code) != 6 {
		return domain.CodeMismatch
	}
	for _, char := range code {
		if char < '0' || char > '9' {
			return domain.CodeMismatch
		}
	}
	budget := s.digest("code-attempt-budget", domain.ID(c.Subject), string(purpose))
	n, err := tx.ReserveBudget(ctx, budget, s.now(), 15, codeTTL)
	if err != nil {
		return err
	}
	if n > 15 {
		return domain.RateLimited
	}
	digest := s.digest(string(purpose), c.ID, code)
	if subtle.ConstantTimeCompare(digest[:], c.Digest[:]) == 1 {
		return nil
	}
	c.Attempts++
	if c.Attempts >= 3 {
		if c.Sends >= 5 {
			now := s.now()
			c.UsedAt = &now
			if err := tx.SaveChallenge(ctx, *c); err != nil {
				return err
			}
			return domain.CodeExpired
		}
		if err := s.replaceCode(ctx, tx, c); err != nil {
			return err
		}
		return domain.CodeReissued
	}
	if err := tx.SaveChallenge(ctx, *c); err != nil {
		return err
	}
	return domain.CodeMismatch
}

func (s *Service) tokenChallenge(ctx context.Context, account domain.Account, purpose domain.Purpose, email string, ttl time.Duration) (domain.Challenge, Mail, error) {
	id, err := newID()
	if err != nil {
		return domain.Challenge{}, Mail{}, err
	}
	secret, err := newSecret()
	if err != nil {
		return domain.Challenge{}, Mail{}, err
	}
	mailID, err := newID()
	if err != nil {
		return domain.Challenge{}, Mail{}, err
	}
	now := s.now()
	c := domain.Challenge{ID: id, Subject: account.Subject, Purpose: purpose, Digest: s.digest(string(purpose), id, secret), Revision: account.Revision, Email: email, SentAt: now, ExpiresAt: now.Add(ttl), Sends: 1}
	m := Mail{TimeZone: mailTimeZone(ctx), ID: mailID, Subject: account.Subject, Kind: string(purpose), Email: email, URL: s.link(string(purpose), token(id, secret)), At: now, ExpiresAt: c.ExpiresAt}
	return c, m, nil
}
func (s *Service) notice(ctx context.Context, tx Unit, kind string) error {
	id, err := newID()
	if err != nil {
		return err
	}
	return tx.Queue(ctx, Mail{TimeZone: mailTimeZone(ctx), ID: id, Subject: tx.Account().Subject, Kind: kind, Email: tx.Account().Email, URL: s.origin + "/account/security/reset", At: s.now(), ExpiresAt: s.now().Add(24 * time.Hour)})
}
func safe(err error) error {
	if err == nil || domain.IsExpected(err) {
		return err
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return domain.Unavailable
}
