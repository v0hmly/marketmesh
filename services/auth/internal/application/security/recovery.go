package security

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"

	applicationsession "github.com/v0hmly/marketmesh/services/auth/internal/application/session"
	domain "github.com/v0hmly/marketmesh/services/auth/internal/domain/security"
	"github.com/v0hmly/marketmesh/services/auth/internal/domain/session"
)

// RecoveryCodeCount is the size of a replaceable, single-display set.
const RecoveryCodeCount = 8

// RecoveryCodes must never be logged, persisted, cached, or queued as mail.
type RecoveryCodes []string

func (RecoveryCodes) String() string     { return "security.RecoveryCodes{[REDACTED]}" }
func (r RecoveryCodes) GoString() string { return r.String() }

func (s *Service) StartRecoveryCodes(ctx context.Context, actor session.Record, password []byte) (LoginResult, error) {
	var result LoginResult
	err := s.withOwner(ctx, actor, func(tx Unit) error {
		if err := s.verifyPassword(ctx, tx, password); err != nil {
			return err
		}
		if !tx.Account().Verified || !tx.Account().CodeEnabled {
			return domain.CodeRequired
		}
		c, found, err := tx.LatestChallenge(ctx, domain.GenerateRecoveryCodes)
		if err != nil {
			return err
		}
		if found && s.now().Sub(c.SentAt) < resendDelay {
			return domain.RateLimited
		}
		if found && c.Check(*tx.Account(), domain.GenerateRecoveryCodes, s.now()) == nil {
			err = s.replaceCode(ctx, tx, &c)
		} else {
			c, err = s.createCode(ctx, tx, domain.GenerateRecoveryCodes)
		}
		if err != nil {
			return err
		}
		result.ChallengeID, result.ExpiresIn = c.ID, c.ExpiresAt.Sub(s.now())
		return nil
	})
	return result, err
}

func (s *Service) CompleteRecoveryCodes(ctx context.Context, actor session.Record, id domain.ID, code string) (RecoveryCodes, error) {
	var codes RecoveryCodes
	err := s.withOwner(ctx, actor, func(tx Unit) error {
		if !tx.Account().Verified || !tx.Account().CodeEnabled {
			return domain.CodeRequired
		}
		c, err := tx.Challenge(ctx, id)
		if err != nil {
			return err
		}
		if err := s.checkCode(ctx, tx, &c, domain.GenerateRecoveryCodes, code); err != nil {
			return err
		}
		digests := make([]domain.Digest, RecoveryCodeCount)
		codes = make(RecoveryCodes, RecoveryCodeCount)
		for i := range codes {
			var secret [16]byte // 128 bits of entropy, independent of the email OTP.
			if _, err := rand.Read(secret[:]); err != nil {
				return domain.Unavailable
			}
			raw := hex.EncodeToString(secret[:])
			clear(secret[:])
			codes[i] = raw[:8] + "-" + raw[8:16] + "-" + raw[16:24] + "-" + raw[24:]
			digests[i] = s.digest("recovery-code-v1", domain.ID(tx.Account().Subject), raw)
		}
		// From the first write onward every failure must roll back, including a
		// port returning a domain rejection. withOwner otherwise commits rejections.
		if err := tx.ReplaceRecoveryCodes(ctx, digests); err != nil {
			return domain.Unavailable
		}
		now := s.now()
		c.UsedAt = &now
		if err := tx.SaveChallenge(ctx, c); err != nil {
			return domain.Unavailable
		}
		if err := s.notice(ctx, tx, "backup_codes"); err != nil {
			return domain.Unavailable
		}
		return nil
	})
	if err != nil {
		clear(codes)
		return nil, err
	}
	return codes, nil
}

func (s *Service) CompleteRecoveryLogin(ctx context.Context, id domain.ID, raw string) (applicationsession.Tokens, error) {
	var tokens applicationsession.Tokens
	err := s.withChallenge(ctx, id, func(tx Unit, c *domain.Challenge) error {
		account := tx.Account()
		if c.Check(*account, domain.LoginCode, s.now()) != nil || !account.Verified || !account.CodeEnabled || account.DeletionAt != nil {
			return domain.CodeExpired
		}
		// Subject budget survives new login challenges and email-code resends.
		bucket := s.digest("recovery-attempt-budget", domain.ID(account.Subject), "")
		n, err := tx.ReserveAttempt(ctx, bucket, s.now())
		if err != nil {
			return err
		}
		if n > 5 {
			return domain.RateLimited
		}
		code, ok := normalizeRecoveryCode(raw)
		if !ok {
			return domain.CodeMismatch
		}
		consumed, err := tx.ConsumeRecoveryCode(ctx, s.digest("recovery-code-v1", domain.ID(account.Subject), code))
		if err != nil {
			return domain.Unavailable
		}
		if !consumed {
			return domain.CodeMismatch
		}
		now := s.now()
		c.UsedAt = &now
		if err := tx.SaveChallenge(ctx, *c); err != nil {
			return domain.Unavailable
		}
		tokens, err = s.issue(ctx, tx)
		if err != nil {
			return domain.Unavailable
		}
		return nil
	})
	if err != nil {
		return applicationsession.Tokens{}, err
	}
	// A lost response or failed activation never resurrects a consumed code.
	return s.sessions.Activate(ctx, tokens)
}

func normalizeRecoveryCode(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if len(value) == 35 {
		if value[8] != '-' || value[17] != '-' || value[26] != '-' {
			return "", false
		}
		value = value[:8] + value[9:17] + value[18:26] + value[27:]
	}
	if len(value) != 32 {
		return "", false
	}
	value = strings.ToLower(value)
	for _, ch := range value {
		if !(ch >= '0' && ch <= '9' || ch >= 'a' && ch <= 'f') {
			return "", false
		}
	}
	return value, true
}
