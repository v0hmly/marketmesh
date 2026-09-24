package postgres

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/v0hmly/marketmesh/services/staff/internal/application"
)

type Store struct{ pool *pgxpool.Pool }

func New(pool *pgxpool.Pool) *Store { return &Store{pool: pool} }
func (s *Store) SaveLogin(ctx context.Context, l application.Login) error {
	// Bound abandoned login state to its ten-minute lifetime.
	_, err := s.pool.Exec(ctx, `DELETE FROM staff.login_attempts WHERE expires_at < now()`)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO staff.login_attempts VALUES ($1,$2,$3,$4,$5,$6)`, l.StateHash, l.BrowserHash, l.Nonce, l.Verifier, l.Invite, l.ExpiresAt)
	return err
}
func (s *Store) ConsumeLogin(ctx context.Context, state, browser string, now time.Time) (application.Login, error) {
	var l application.Login
	err := s.pool.QueryRow(ctx, `DELETE FROM staff.login_attempts WHERE state_hash=$1 AND browser_hash=$2 AND expires_at>$3 RETURNING nonce,verifier,invite`, state, browser, now).Scan(&l.Nonce, &l.Verifier, &l.Invite)
	if errors.Is(err, pgx.ErrNoRows) {
		err = application.ErrInvalidLogin
	}
	return l, err
}
func (s *Store) SaveSession(ctx context.Context, hash string, p application.Principal, absolute, idle time.Time) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM staff.sessions WHERE expires_at < now() OR idle_until < now()`)
	if err != nil {
		return err
	}
	_, err = s.pool.Exec(ctx, `INSERT INTO staff.sessions VALUES ($1,$2,$3,$4,$5,$6,$7)`, hash, p.Issuer, p.Subject, p.Email, p.Name, absolute, idle)
	return err
}
func (s *Store) Session(ctx context.Context, hash string, now, idle time.Time) (application.Session, error) {
	var v application.Session
	err := s.pool.QueryRow(ctx, `WITH active AS (
 UPDATE staff.sessions SET idle_until=LEAST($3,expires_at) WHERE token_hash=$1 AND expires_at>$2 AND idle_until>$2 RETURNING *
 ) SELECT a.issuer,a.subject,a.email,a.display_name,COALESCE(m.role,''),LEAST(a.expires_at,a.idle_until)
 FROM active a LEFT JOIN staff.members m ON (m.issuer,m.subject)=(a.issuer,a.subject)`, hash, now, idle).Scan(&v.Issuer, &v.Subject, &v.Email, &v.Name, &v.Role, &v.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		err = application.ErrUnauthenticated
	}
	return v, err
}
func (s *Store) DeleteSession(ctx context.Context, hash string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM staff.sessions WHERE token_hash=$1`, hash)
	return err
}
func (s *Store) Invite(ctx context.Context, hash string, p application.Principal, now time.Time, accept bool) (application.Invite, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return application.Invite{}, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var email, role, inviter string
	var expiry time.Time
	var used *time.Time
	err = tx.QueryRow(ctx, `SELECT email,role,inviter_name,expires_at,used_at FROM staff.invites WHERE token_hash=$1 FOR UPDATE`, hash).Scan(&email, &role, &inviter, &expiry, &used)
	if errors.Is(err, pgx.ErrNoRows) {
		return application.Invite{State: "expired"}, nil
	}
	if err != nil {
		return application.Invite{}, err
	}
	// Do not reveal role, inviter or consumption state to a different verified identity.
	if email != p.Email {
		return application.Invite{State: "wrong_account"}, nil
	}
	if used != nil {
		return application.Invite{State: "used"}, nil
	}
	if !now.Before(expiry) {
		return application.Invite{State: "expired"}, nil
	}
	if accept {
		// Invitations cannot silently change an existing member's privileges.
		_, err = tx.Exec(ctx, `INSERT INTO staff.members (issuer,subject,role) VALUES ($1,$2,$3) ON CONFLICT DO NOTHING`, p.Issuer, p.Subject, role)
		if err != nil {
			return application.Invite{}, err
		}
		_, err = tx.Exec(ctx, `UPDATE staff.invites SET used_at=$2 WHERE token_hash=$1`, hash, now)
		if err != nil {
			return application.Invite{}, err
		}
	}
	if err = tx.Commit(ctx); err != nil {
		return application.Invite{}, err
	}
	return application.Invite{State: "active", Role: role, Inviter: inviter}, nil
}
