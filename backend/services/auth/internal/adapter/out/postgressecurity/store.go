// Package postgressecurity implements atomic account security on PostgreSQL RW.
package postgressecurity

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	pg "github.com/v0hmly/marketmesh/platform/postgres"
	"github.com/v0hmly/marketmesh/services/auth/internal/adapter/out/registrationwire"
	application "github.com/v0hmly/marketmesh/services/auth/internal/application/security"
	"github.com/v0hmly/marketmesh/services/auth/internal/domain/credential"
	"github.com/v0hmly/marketmesh/services/auth/internal/domain/registrationevent"
	domain "github.com/v0hmly/marketmesh/services/auth/internal/domain/security"
)

type Database interface {
	RW() pg.Executor
	WithinTransaction(context.Context, pg.TransactionOptions, pg.TransactionFunc) error
}
type Store struct {
	db   Database
	aead cipher.AEAD
}
type unit struct {
	executor pg.Executor
	store    *Store
	account  *domain.Account
}

func New(db Database, key [32]byte) (*Store, error) {
	if db == nil || key == ([32]byte{}) {
		return nil, domain.Unavailable
	}
	mac := hmac.New(sha256.New, key[:])
	_, _ = mac.Write([]byte("marketmesh.auth.mail-outbox.v1"))
	derived := mac.Sum(nil)
	defer clear(derived)
	block, err := aes.NewCipher(derived)
	if err != nil {
		return nil, domain.Unavailable
	}
	aead, err := cipher.NewGCMWithRandomNonce(block)
	if err != nil {
		return nil, domain.Unavailable
	}
	return &Store{db: db, aead: aead}, nil
}

func (s *Store) WithAccount(ctx context.Context, selector application.Selector, apply func(application.Unit) error) error {
	if apply == nil || (selector.Email == "") == (selector.Subject == (credential.SubjectID{})) {
		return domain.InvalidInput
	}
	return sanitize(s.db.WithinTransaction(ctx, pg.TransactionOptions{}, func(ctx context.Context, ex pg.Executor) error {
		var account domain.Account
		var id []byte
		var password string
		query := `SELECT subject_id,identifier,password_digest FROM auth.credentials WHERE subject_id=$1 FOR UPDATE`
		var key any = selector.Subject.Bytes()
		if selector.Email != "" {
			query = `SELECT subject_id,identifier,password_digest FROM auth.credentials WHERE identifier=$1 FOR UPDATE`
			key = selector.Email
		}
		err := ex.QueryRow(ctx, query, key).Scan(&id, &account.Email, &password)
		if errors.Is(err, pgx.ErrNoRows) {
			return apply(&unit{executor: ex, store: s})
		}
		if err != nil {
			return err
		}
		if len(id) != 16 {
			return domain.Unavailable
		}
		copy(account.Subject[:], id)
		account.PasswordDigest, err = credential.NewPasswordDigest(password)
		if err != nil {
			return err
		}
		if _, err := ex.Exec(ctx, `INSERT INTO auth.account_security(subject_id) VALUES($1) ON CONFLICT DO NOTHING`, id); err != nil {
			return err
		}
		if err := ex.QueryRow(ctx, `SELECT email_verified,login_code_enabled,revision,deletion_at,deleted_at FROM auth.account_security WHERE subject_id=$1`, id).Scan(&account.Verified, &account.CodeEnabled, &account.Revision, &account.DeletionAt, &account.DeletedAt); err != nil {
			return err
		}
		before := account
		if err := apply(&unit{executor: ex, store: s, account: &account}); err != nil {
			return err
		}
		if account.Subject != before.Subject {
			return domain.Unavailable
		}
		if account.Email != before.Email || account.PasswordDigest != before.PasswordDigest {
			if _, err := ex.Exec(ctx, `UPDATE auth.credentials SET identifier=$2,password_digest=$3,updated_at=now() WHERE subject_id=$1`, id, account.Email, account.PasswordDigest.String()); err != nil {
				return err
			}
		}
		if account != before {
			_, err := ex.Exec(ctx, `UPDATE auth.account_security SET email_verified=$2,login_code_enabled=$3,revision=$4,deletion_at=$5,deleted_at=$6 WHERE subject_id=$1`, id, account.Verified, account.CodeEnabled, account.Revision, account.DeletionAt, account.DeletedAt)
			return err
		}
		return nil
	}))
}

func (u *unit) Account() *domain.Account { return u.account }

func (s *Store) Register(ctx context.Context, value credential.Credential, event registrationevent.Event, challenge domain.Challenge, message application.Mail) error {
	if event.SubjectID != value.SubjectID() || event.Validate() != nil || challenge.Subject != value.SubjectID() || message.Subject != value.SubjectID() {
		return domain.InvalidInput
	}
	payload, err := registrationwire.Marshal(event)
	if err != nil {
		return domain.Unavailable
	}
	return sanitize(s.db.WithinTransaction(ctx, pg.TransactionOptions{}, func(ctx context.Context, ex pg.Executor) error {
		tag, err := ex.Exec(ctx, `INSERT INTO auth.credentials(subject_id,identifier,password_digest) VALUES($1,$2,$3) ON CONFLICT(identifier) DO NOTHING`, value.SubjectID().Bytes(), value.Identifier().String(), value.PasswordDigest().String())
		if err != nil || tag.RowsAffected() == 0 {
			return err
		}
		if _, err := ex.Exec(ctx, `INSERT INTO auth.registration_outbox(event_id,subject_id,occurred_at,payload) VALUES($1,$2,$3,$4)`, event.ID[:], event.SubjectID.Bytes(), event.OccurredAt, payload); err != nil {
			return err
		}
		if _, err := ex.Exec(ctx, `INSERT INTO auth.account_security(subject_id) VALUES($1)`, value.SubjectID().Bytes()); err != nil {
			return err
		}
		u := &unit{executor: ex, store: s, account: &domain.Account{Subject: value.SubjectID(), Email: value.Identifier().String(), PasswordDigest: value.PasswordDigest(), Revision: 1}}
		if err := u.SaveChallenge(ctx, challenge); err != nil {
			return err
		}
		return u.Queue(ctx, message)
	}))
}

func (s *Store) ChallengeSubject(ctx context.Context, id domain.ID) (credential.SubjectID, error) {
	var raw []byte
	err := s.db.RW().QueryRow(ctx, `SELECT subject_id FROM auth.security_challenges WHERE challenge_id=$1`, id[:]).Scan(&raw)
	if errors.Is(err, pgx.ErrNoRows) {
		return credential.SubjectID{}, domain.TokenExpired
	}
	if err != nil || len(raw) != 16 {
		return credential.SubjectID{}, domain.Unavailable
	}
	var subject credential.SubjectID
	copy(subject[:], raw)
	return subject, nil
}

const challengeColumns = `challenge_id,subject_id,purpose,secret_digest,revision,email,expires_at,sent_at,attempts,sends,used_at`

func scanChallenge(row pgx.Row) (domain.Challenge, error) {
	var c domain.Challenge
	var id, subject, digest []byte
	err := row.Scan(&id, &subject, &c.Purpose, &digest, &c.Revision, &c.Email, &c.ExpiresAt, &c.SentAt, &c.Attempts, &c.Sends, &c.UsedAt)
	if err != nil {
		return c, err
	}
	if len(id) != 16 || len(subject) != 16 || len(digest) != 32 {
		return c, domain.Unavailable
	}
	copy(c.ID[:], id)
	copy(c.Subject[:], subject)
	copy(c.Digest[:], digest)
	return c, nil
}
func (u *unit) Challenge(ctx context.Context, id domain.ID) (domain.Challenge, error) {
	if u.account == nil {
		return domain.Challenge{}, domain.TokenExpired
	}
	c, err := scanChallenge(u.executor.QueryRow(ctx, `SELECT `+challengeColumns+` FROM auth.security_challenges WHERE challenge_id=$1 AND subject_id=$2`, id[:], u.account.Subject.Bytes()))
	if errors.Is(err, pgx.ErrNoRows) {
		return c, domain.TokenExpired
	}
	return c, err
}
func (u *unit) LatestChallenge(ctx context.Context, purpose domain.Purpose) (domain.Challenge, bool, error) {
	if u.account == nil {
		return domain.Challenge{}, false, nil
	}
	c, err := scanChallenge(u.executor.QueryRow(ctx, `SELECT `+challengeColumns+` FROM auth.security_challenges WHERE subject_id=$1 AND purpose=$2 ORDER BY sent_at DESC,challenge_id DESC LIMIT 1`, u.account.Subject.Bytes(), string(purpose)))
	if errors.Is(err, pgx.ErrNoRows) {
		return c, false, nil
	}
	return c, err == nil, err
}
func (u *unit) SaveChallenge(ctx context.Context, c domain.Challenge) error {
	if u.account == nil || c.Subject != u.account.Subject {
		return domain.Unavailable
	}
	_, err := u.executor.Exec(ctx, `INSERT INTO auth.security_challenges (`+challengeColumns+`) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
ON CONFLICT(challenge_id) DO UPDATE SET secret_digest=EXCLUDED.secret_digest,expires_at=EXCLUDED.expires_at,sent_at=EXCLUDED.sent_at,attempts=EXCLUDED.attempts,sends=EXCLUDED.sends,used_at=EXCLUDED.used_at
WHERE auth.security_challenges.subject_id=EXCLUDED.subject_id AND auth.security_challenges.purpose=EXCLUDED.purpose`, c.ID[:], c.Subject.Bytes(), string(c.Purpose), c.Digest[:], c.Revision, c.Email, c.ExpiresAt, c.SentAt, c.Attempts, c.Sends, c.UsedAt)
	return err
}
func (u *unit) ReserveAttempt(ctx context.Context, bucket domain.Digest, now time.Time) (int, error) {
	return u.ReserveBudget(ctx, bucket, now, 5, 15*time.Minute)
}
func (u *unit) ReserveBudget(ctx context.Context, bucket domain.Digest, now time.Time, limit int, window time.Duration) (int, error) {
	if limit < 1 || limit > 100 || window < time.Second {
		return 0, domain.Unavailable
	}
	_, err := u.executor.Exec(ctx, `INSERT INTO auth.login_limits(bucket,attempts,expires_at) VALUES($1,0,$2) ON CONFLICT DO NOTHING`, bucket[:], now.Add(window))
	if err != nil {
		return 0, err
	}
	var n int
	var expires time.Time
	if err := u.executor.QueryRow(ctx, `SELECT attempts,expires_at FROM auth.login_limits WHERE bucket=$1 FOR UPDATE`, bucket[:]).Scan(&n, &expires); err != nil {
		return 0, err
	}
	if !now.Before(expires) {
		n = 0
		expires = now.Add(window)
	}
	if n >= limit {
		return limit + 1, nil
	}
	n++
	_, err = u.executor.Exec(ctx, `UPDATE auth.login_limits SET attempts=$2,expires_at=$3 WHERE bucket=$1`, bucket[:], n, expires)
	return n, err
}
func (u *unit) ResetAttempts(ctx context.Context, bucket domain.Digest) error {
	_, err := u.executor.Exec(ctx, `DELETE FROM auth.login_limits WHERE bucket=$1`, bucket[:])
	return err
}

func (u *unit) Queue(ctx context.Context, m application.Mail) error {
	if u.account == nil || m.Subject != u.account.Subject || m.ID == (domain.ID{}) || m.Email == "" || !m.At.Before(m.ExpiresAt) {
		return domain.Unavailable
	}
	payload, err := json.Marshal(m)
	if err != nil {
		return domain.Unavailable
	}
	defer clear(payload)
	if len(payload) > 16384 {
		return domain.Unavailable
	}
	sealed := u.store.aead.Seal(nil, nil, payload, m.ID[:])
	_, err = u.executor.Exec(ctx, `INSERT INTO auth.mail_outbox(message_id,subject_id,payload,created_at,expires_at,available_at) VALUES($1,$2,$3,$4,$5,$4)`, m.ID[:], m.Subject.Bytes(), sealed, m.At, m.ExpiresAt)
	return err
}

func sanitize(err error) error {
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
