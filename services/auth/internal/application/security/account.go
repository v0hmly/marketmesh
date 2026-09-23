package security

import (
	"context"
	"crypto/subtle"
	"math"
	"time"

	"github.com/v0hmly/marketmesh/services/auth/internal/domain/credential"
	domain "github.com/v0hmly/marketmesh/services/auth/internal/domain/security"
	"github.com/v0hmly/marketmesh/services/auth/internal/domain/session"
)

func (s *Service) RequestToken(ctx context.Context, email string, purpose domain.Purpose) error {
	if purpose != domain.VerifyEmail && purpose != domain.ResetPassword {
		return domain.InvalidInput
	}
	email, err := domain.Email(email)
	if err != nil {
		return domain.InvalidInput
	}
	return safe(s.store.WithAccount(ctx, Selector{Email: email}, func(tx Unit) error {
		account := tx.Account()
		if account == nil || account.DeletedAt != nil || account.DeletionAt != nil || purpose == domain.VerifyEmail && account.Verified {
			return nil
		}
		latest, found, err := tx.LatestChallenge(ctx, purpose)
		if err != nil {
			return err
		}
		if found && s.now().Sub(latest.SentAt) < resendDelay {
			return nil
		}
		if found {
			now := s.now()
			latest.UsedAt = &now
			if err := tx.SaveChallenge(ctx, latest); err != nil {
				return err
			}
		}
		ttl := 24 * time.Hour
		if purpose == domain.ResetPassword {
			ttl = 30 * time.Minute
		}
		c, m, err := s.tokenChallenge(*account, purpose, email, ttl)
		if err != nil {
			return err
		}
		if err := tx.SaveChallenge(ctx, c); err != nil {
			return err
		}
		return tx.Queue(ctx, m)
	}))
}

func (s *Service) ConfirmEmail(ctx context.Context, value string) error {
	return s.confirmToken(ctx, value, domain.VerifyEmail, func(tx Unit, c *domain.Challenge) error {
		if tx.Account().Email != c.Email || tx.Account().DeletionAt != nil {
			return domain.TokenExpired
		}
		tx.Account().Verified = true
		return nil
	})
}
func (s *Service) ResetPassword(ctx context.Context, value string, raw []byte) error {
	password, err := domain.NewPassword(raw)
	if err != nil {
		return err
	}
	defer password.Destroy()
	plain := password.Bytes()
	defer clear(plain)
	return s.confirmToken(ctx, value, domain.ResetPassword, func(tx Unit, c *domain.Challenge) error {
		account := tx.Account()
		if account.Email != c.Email || account.DeletionAt != nil {
			return domain.TokenExpired
		}
		if err := increment(account); err != nil {
			return err
		}
		digest, err := s.hasher.Hash(plain)
		if err != nil {
			return domain.Unavailable
		}
		account.PasswordDigest = digest
		account.Verified = true
		if err := tx.RevokeSessions(ctx, s.now()); err != nil {
			return err
		}
		return s.notice(ctx, tx, "password_changed")
	})
}

func (s *Service) confirmToken(ctx context.Context, value string, purpose domain.Purpose, apply func(Unit, *domain.Challenge) error) error {
	id, secret, err := parseToken(value)
	if err != nil {
		return err
	}
	return s.withChallenge(ctx, id, func(tx Unit, c *domain.Challenge) error {
		if err := c.Check(*tx.Account(), purpose, s.now()); err != nil {
			return err
		}
		digest := s.digest(string(purpose), id, secret)
		if subtle.ConstantTimeCompare(digest[:], c.Digest[:]) != 1 {
			return domain.TokenExpired
		}
		if err := apply(tx, c); err != nil {
			return err
		}
		now := s.now()
		c.UsedAt = &now
		return tx.SaveChallenge(ctx, *c)
	})
}

type Credentials struct {
	domain.Account
	RecoveryCodesRemaining int
}

func (s *Service) Credentials(ctx context.Context, actor session.Record) (Credentials, error) {
	var result Credentials
	err := s.withOwner(ctx, actor, func(tx Unit) error {
		result.Account = *tx.Account()
		var err error
		result.RecoveryCodesRemaining, err = tx.RecoveryCodesRemaining(ctx)
		return err
	})
	return result, err
}

func (s *Service) ChangePassword(ctx context.Context, actor session.Record, oldPassword, newPassword []byte) error {
	password, err := domain.NewPassword(newPassword)
	if err != nil {
		return err
	}
	defer password.Destroy()
	plain := password.Bytes()
	defer clear(plain)
	return s.withOwner(ctx, actor, func(tx Unit) error {
		if err := s.verifyPassword(ctx, tx, oldPassword); err != nil {
			return err
		}
		if err := increment(tx.Account()); err != nil {
			return err
		}
		digest, err := s.hasher.Hash(plain)
		if err != nil {
			return domain.Unavailable
		}
		tx.Account().PasswordDigest = digest
		if err := tx.RevokeSessions(ctx, s.now()); err != nil {
			return err
		}
		return s.notice(ctx, tx, "password_changed")
	})
}

func (s *Service) StartEmailChange(ctx context.Context, actor session.Record, newEmail string, password []byte) error {
	newEmail, err := domain.Email(newEmail)
	if err != nil {
		return err
	}
	return s.withOwner(ctx, actor, func(tx Unit) error {
		if err := s.verifyPassword(ctx, tx, password); err != nil {
			return err
		}
		account := tx.Account()
		if newEmail == account.Email {
			return domain.InvalidInput
		}
		if !account.Verified {
			return domain.EmailUnverified
		}
		previous, found, err := tx.LatestChallenge(ctx, domain.ChangeEmail)
		if err != nil {
			return err
		}
		if found && s.now().Sub(previous.SentAt) < resendDelay {
			return domain.RateLimited
		}
		if err := increment(account); err != nil {
			return err
		}
		confirm, letter, err := s.tokenChallenge(*account, domain.ChangeEmail, newEmail, 30*time.Minute)
		if err != nil {
			return err
		}
		cancel, alert, err := s.tokenChallenge(*account, domain.CancelEmailChange, account.Email, 30*time.Minute)
		if err != nil {
			return err
		}
		letter.OtherEmail = account.Email
		alert.OtherEmail = newEmail
		if err := tx.SaveChallenge(ctx, confirm); err != nil {
			return err
		}
		if err := tx.SaveChallenge(ctx, cancel); err != nil {
			return err
		}
		if err := tx.Queue(ctx, letter); err != nil {
			return err
		}
		return tx.Queue(ctx, alert)
	})
}
func (s *Service) ConfirmEmailChange(ctx context.Context, value string) error {
	return s.confirmToken(ctx, value, domain.ChangeEmail, func(tx Unit, c *domain.Challenge) error {
		if tx.Account().DeletionAt != nil {
			return domain.TokenExpired
		}
		if err := increment(tx.Account()); err != nil {
			return err
		}
		tx.Account().Email = c.Email
		tx.Account().Verified = true
		return tx.RevokeSessions(ctx, s.now())
	})
}
func (s *Service) CancelEmailChange(ctx context.Context, value string) error {
	return s.confirmToken(ctx, value, domain.CancelEmailChange, func(tx Unit, _ *domain.Challenge) error { return increment(tx.Account()) })
}

func (s *Service) StartCodeChange(ctx context.Context, actor session.Record, password []byte, enabled bool) (LoginResult, error) {
	var result LoginResult
	err := s.withOwner(ctx, actor, func(tx Unit) error {
		if err := s.verifyPassword(ctx, tx, password); err != nil {
			return err
		}
		if !tx.Account().Verified {
			return domain.EmailUnverified
		}
		purpose := domain.EnableCode
		if !enabled {
			purpose = domain.DisableCode
		}
		if tx.Account().CodeEnabled == enabled {
			return domain.InvalidInput
		}
		c, found, err := tx.LatestChallenge(ctx, purpose)
		if err != nil {
			return err
		}
		if found && c.Check(*tx.Account(), purpose, s.now()) == nil {
			if s.now().Sub(c.SentAt) < resendDelay {
				return domain.RateLimited
			}
			if err := s.replaceCode(ctx, tx, &c); err != nil {
				return err
			}
		} else {
			c, err = s.createCode(ctx, tx, purpose)
			if err != nil {
				return err
			}
		}
		result.ChallengeID = c.ID
		result.ExpiresIn = c.ExpiresAt.Sub(s.now())
		return nil
	})
	return result, err
}
func (s *Service) CompleteCodeChange(ctx context.Context, actor session.Record, id domain.ID, code string, enabled bool) error {
	var outcome error
	err := s.withOwner(ctx, actor, func(tx Unit) error {
		c, err := tx.Challenge(ctx, id)
		if err != nil {
			return err
		}
		purpose := domain.EnableCode
		if !enabled {
			purpose = domain.DisableCode
		}
		if err := s.checkCode(ctx, tx, &c, purpose, code); err != nil {
			if domain.IsExpected(err) {
				outcome = err
				return nil
			}
			return err
		}
		if err := increment(tx.Account()); err != nil {
			return err
		}
		tx.Account().CodeEnabled = enabled
		now := s.now()
		c.UsedAt = &now
		if err := tx.SaveChallenge(ctx, c); err != nil {
			return err
		}
		if err := tx.RevokeSessions(ctx, now); err != nil {
			return err
		}
		kind := "two_factor_on"
		if !enabled {
			kind = "two_factor_off"
		}
		return s.notice(ctx, tx, kind)
	})
	if err != nil {
		return err
	}
	return outcome
}

func (s *Service) ListSessions(ctx context.Context, actor session.Record) ([]session.Record, error) {
	var result []session.Record
	err := s.withOwner(ctx, actor, func(tx Unit) error { var err error; result, err = tx.Sessions(ctx, s.now()); return err })
	return result, err
}
func (s *Service) RevokeSession(ctx context.Context, actor session.Record, id session.ID) error {
	return s.withOwner(ctx, actor, func(tx Unit) error {
		if actor.ID != id && s.now().Before(actor.CreatedAt.Add(24*time.Hour)) {
			return domain.NewDeviceCooldown
		}
		return tx.RevokeSession(ctx, id, s.now())
	})
}
func (s *Service) LogoutAll(ctx context.Context, actor session.Record) error {
	return s.withOwner(ctx, actor, func(tx Unit) error {
		if s.now().Before(actor.CreatedAt.Add(24 * time.Hour)) {
			return domain.NewDeviceCooldown
		}
		if err := tx.RevokeSessions(ctx, s.now()); err != nil {
			return err
		}
		return s.notice(ctx, tx, "sessions_closed")
	})
}

func (s *Service) withOwner(ctx context.Context, actor session.Record, apply func(Unit) error) error {
	if actor.SubjectID == (credential.SubjectID{}) || actor.ID == (session.ID{}) {
		return domain.InvalidCredentials
	}
	var outcome error
	err := s.store.WithAccount(ctx, Selector{Subject: actor.SubjectID}, func(tx Unit) error {
		if err := tx.CheckSession(ctx, actor, s.now()); err != nil {
			return err
		}
		err := apply(tx)
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
func (s *Service) verifyPassword(ctx context.Context, tx Unit, raw []byte) error {
	account := tx.Account()
	bucket := s.digest("reauth-limit", domain.ID(account.Subject), "")
	attempt, err := tx.ReserveAttempt(ctx, bucket, s.now())
	if err != nil {
		return err
	}
	if attempt > 5 {
		return domain.LoginLocked
	}
	pw, err := credential.NewPassword(raw)
	if err != nil {
		return domain.InvalidCredentials
	}
	defer pw.Destroy()
	plain := pw.Bytes()
	defer clear(plain)
	valid, _, err := s.hasher.Verify(plain, account.PasswordDigest)
	if err != nil {
		return domain.Unavailable
	}
	if !valid {
		if attempt == 5 {
			return domain.LoginLocked
		}
		return domain.InvalidCredentials
	}
	return tx.ResetAttempts(ctx, bucket)
}
func increment(account *domain.Account) error {
	if account.Revision == math.MaxInt64 {
		return domain.Unavailable
	}
	account.Revision++
	return nil
}
