package postgressecurity

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	application "github.com/v0hmly/marketmesh/services/auth/internal/application/security"
	domain "github.com/v0hmly/marketmesh/services/auth/internal/domain/security"
)

func (s *Store) ClaimMail(ctx context.Context, now time.Time) (application.MailLease, bool, error) {
	// Bounded cleanup clears stale secrets and keeps anonymous throttling finite.
	for _, statement := range []string{
		`DELETE FROM auth.login_limits WHERE expires_at<$1 AND bucket IN (SELECT bucket FROM auth.login_limits WHERE expires_at<$1 FOR UPDATE SKIP LOCKED LIMIT 100)`,
		`DELETE FROM auth.security_challenges WHERE challenge_id IN (SELECT challenge_id FROM auth.security_challenges WHERE expires_at<$1::timestamptz-interval '1 day' LIMIT 100)`,
		`DELETE FROM auth.mail_outbox WHERE message_id IN (SELECT message_id FROM auth.mail_outbox WHERE completed_at<$1::timestamptz-interval '7 days' LIMIT 100)`,
		`UPDATE auth.mail_outbox SET payload=NULL,completed_at=$1,outcome='expired',lease_token=NULL,lease_until=NULL WHERE message_id IN (SELECT message_id FROM auth.mail_outbox WHERE completed_at IS NULL AND expires_at<=$1 AND (lease_until IS NULL OR lease_until<=$1) LIMIT 100)`,
	} {
		if _, err := s.db.RW().Exec(ctx, statement, now); err != nil {
			return application.MailLease{}, false, domain.Unavailable
		}
	}
	var lease application.MailLease
	if _, err := rand.Read(lease.Token[:]); err != nil {
		return lease, false, domain.Unavailable
	}
	var id, payload []byte
	err := s.db.RW().QueryRow(ctx, `WITH candidate AS (
 SELECT message_id FROM auth.mail_outbox WHERE completed_at IS NULL AND available_at<=$1 AND expires_at>$1 AND attempts<12 AND (lease_until IS NULL OR lease_until<=$1)
 ORDER BY available_at,created_at FOR UPDATE SKIP LOCKED LIMIT 1
) UPDATE auth.mail_outbox m SET lease_token=$2,lease_until=$3,attempts=attempts+1 FROM candidate c WHERE m.message_id=c.message_id RETURNING m.message_id,m.payload,m.attempts`, now, lease.Token[:], now.Add(30*time.Second)).Scan(&id, &payload, &lease.Attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		return lease, false, nil
	}
	if err != nil || len(id) != 16 {
		return lease, false, domain.Unavailable
	}
	cleartext, err := s.aead.Open(nil, nil, payload, id)
	if err != nil {
		return lease, false, domain.Unavailable
	}
	defer clear(cleartext)
	if len(cleartext) > 16384 || json.Unmarshal(cleartext, &lease.Mail) != nil || string(lease.Mail.ID[:]) != string(id) {
		return application.MailLease{}, false, domain.Unavailable
	}
	return lease, true, nil
}
func (s *Store) FinishMail(ctx context.Context, lease application.MailLease, now time.Time, outcome string, next time.Time) error {
	if outcome == "retry" {
		tag, err := s.db.RW().Exec(ctx, `UPDATE auth.mail_outbox SET available_at=$4,lease_token=NULL,lease_until=NULL WHERE message_id=$1 AND lease_token=$2 AND lease_until>$3 AND completed_at IS NULL`, lease.Mail.ID[:], lease.Token[:], now, next)
		if err != nil || tag.RowsAffected() != 1 {
			return domain.Unavailable
		}
		return nil
	}
	if outcome != "delivered" && outcome != "expired" && outcome != "rejected" && outcome != "exhausted" {
		return domain.InvalidInput
	}
	tag, err := s.db.RW().Exec(ctx, `UPDATE auth.mail_outbox SET payload=NULL,completed_at=$3,outcome=$4,lease_token=NULL,lease_until=NULL WHERE message_id=$1 AND lease_token=$2 AND lease_until>$3 AND completed_at IS NULL`, lease.Mail.ID[:], lease.Token[:], now, outcome)
	if err != nil || tag.RowsAffected() != 1 {
		return domain.Unavailable
	}
	return nil
}
