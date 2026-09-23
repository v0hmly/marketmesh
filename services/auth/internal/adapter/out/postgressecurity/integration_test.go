//go:build integration

package postgressecurity_test

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	pg "github.com/v0hmly/marketmesh/platform/postgres"
	passwordbcrypt "github.com/v0hmly/marketmesh/services/auth/internal/adapter/out/bcrypt"
	"github.com/v0hmly/marketmesh/services/auth/internal/adapter/out/postgressecurity"
	"github.com/v0hmly/marketmesh/services/auth/internal/adapter/out/postgressession"
	"github.com/v0hmly/marketmesh/services/auth/internal/adapter/out/registrationevent"
	security "github.com/v0hmly/marketmesh/services/auth/internal/application/security"
	applicationsession "github.com/v0hmly/marketmesh/services/auth/internal/application/session"
	domain "github.com/v0hmly/marketmesh/services/auth/internal/domain/security"
	session "github.com/v0hmly/marketmesh/services/auth/internal/domain/session"
	"github.com/v0hmly/marketmesh/services/auth/migrations"
)

type database struct{ pool *pgxpool.Pool }

func (d database) RW() pg.Executor { return d.pool }
func (d database) WithinTransaction(ctx context.Context, _ pg.TransactionOptions, fn pg.TransactionFunc) error {
	tx, err := d.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := fn(ctx, tx); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

type accessStore struct {
	mu     sync.Mutex
	values map[session.ID]session.Access
}

func (a *accessStore) Put(_ context.Context, r session.Record, d session.Digest, _ time.Time) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.values[r.ID] = session.Access{Digest: d, Version: r.Version, ExpiresAt: r.AccessExpiresAt}
	return nil
}
func (a *accessStore) Get(_ context.Context, id session.ID) (session.Access, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.values[id], nil
}

type issuer struct{}

func (issuer) Issue(context.Context, session.Record, string, time.Time) (string, error) {
	return "", errors.New("not used")
}

func TestSecurityLifecycle(t *testing.T) {
	dsn := os.Getenv("AUTH_SECURITY_TEST_DSN")
	if dsn == "" {
		t.Skip("set AUTH_SECURITY_TEST_DSN for the disposable database")
	}
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil || cfg.ConnConfig.Database != "auth_security_test" {
		t.Fatal("requires the dedicated auth_security_test database")
	}
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal("connect test PostgreSQL")
	}
	defer pool.Close()
	if _, err := pool.Exec(t.Context(), `DROP SCHEMA IF EXISTS auth CASCADE`); err != nil {
		t.Fatal("reset test schema")
	}
	for _, sql := range []string{migrations.CredentialsUp, migrations.SessionsUp, migrations.RegistrationOutboxUp, migrations.SecurityUp, migrations.RecoveryUp} {
		if _, err := pool.Exec(t.Context(), sql); err != nil {
			t.Fatal(err)
		}
	}
	db := database{pool}
	store, err := postgressecurity.New(db, [32]byte{1})
	if err != nil {
		t.Fatal(err)
	}
	hasher, err := passwordbcrypt.New(10)
	if err != nil {
		t.Fatal(err)
	}
	sessionStore, err := postgressession.New(db)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	sessions, err := applicationsession.New(sessionStore, &accessStore{values: make(map[session.ID]session.Access)}, issuer{}, applicationsession.Config{AccessTTL: time.Minute, IdleTTL: 48 * time.Hour, AbsoluteTTL: 72 * time.Hour, AssertionTTL: 30 * time.Second, Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := security.New(store, hasher, sessions, registrationevent.New(), security.Config{Key: [32]byte{1}, Origin: "https://localhost:18443", Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	const password = "CorrectHorse9!"
	inbox := make([]security.Mail, 0)
	take := func(t *testing.T, kind, email string) security.Mail {
		t.Helper()
		for {
			lease, found, err := store.ClaimMail(t.Context(), now)
			if err != nil {
				t.Fatal("claim mail", err)
			}
			if !found {
				break
			}
			inbox = append(inbox, lease.Mail)
			if err := store.FinishMail(t.Context(), lease, now, "delivered", time.Time{}); err != nil {
				t.Fatal(err)
			}
		}
		for i := len(inbox) - 1; i >= 0; i-- {
			if inbox[i].Kind == kind && inbox[i].Email == email {
				m := inbox[i]
				inbox = append(inbox[:i], inbox[i+1:]...)
				return m
			}
		}
		t.Fatal("expected mail was not queued")
		return security.Mail{}
	}
	token := func(m security.Mail) string {
		t.Helper()
		u, err := url.Parse(m.URL)
		if err != nil {
			t.Fatal("invalid token link")
		}
		values, err := url.ParseQuery(u.Fragment)
		if err != nil {
			t.Fatal("invalid fragment")
		}
		return values.Get("token")
	}
	register := func(t *testing.T, email string) {
		t.Helper()
		if err := svc.Register(t.Context(), email, []byte(password)); err != nil {
			t.Fatal(err)
		}
		if err := svc.ConfirmEmail(t.Context(), token(take(t, "verify", email))); err != nil {
			t.Fatal(err)
		}
	}
	login := func(t *testing.T, email string) applicationsession.Tokens {
		t.Helper()
		result, err := svc.Login(t.Context(), email, []byte(password), true)
		if err != nil || result.Tokens.Record.ID == (session.ID{}) {
			t.Fatal("password login failed", err)
		}
		if _, err := sessions.Authenticate(t.Context(), result.Tokens.Access.Reveal()); err != nil {
			t.Fatal("new session is not authenticatable")
		}
		return result.Tokens
	}

	t.Run("registration is non-enumerating and verification is one use", func(t *testing.T) {
		const email = "register@example.test"
		if err := svc.Register(t.Context(), email, []byte(password)); err != nil {
			t.Fatal(err)
		}
		if err := svc.Register(t.Context(), email, []byte(password)); err != nil {
			t.Fatal(err)
		}
		var n int
		if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM auth.registration_outbox`).Scan(&n); err != nil || n != 1 {
			t.Fatal("duplicate registration event")
		}
		if _, err := svc.Login(t.Context(), email, []byte(password), true); err != domain.EmailUnverified {
			t.Fatal("unverified login accepted")
		}
		link := token(take(t, "verify", email))
		if err := svc.ConfirmEmail(t.Context(), link); err != nil {
			t.Fatal(err)
		}
		if err := svc.ConfirmEmail(t.Context(), link); err != domain.TokenUsed {
			t.Fatal("token reused")
		}
		login(t, email)
	})
	t.Run("code change login race legacy bypass and attempts", func(t *testing.T) {
		const email = "code@example.test"
		register(t, email)
		current := login(t, email)
		change, err := svc.StartCodeChange(t.Context(), current.Record, []byte(password), true)
		if err != nil {
			t.Fatal(err)
		}
		m := take(t, "code", email)
		if err := svc.CompleteCodeChange(t.Context(), current.Record, change.ChallengeID, m.Code, true); err != nil {
			t.Fatal(err)
		}
		if _, err := sessions.Authenticate(t.Context(), current.Access.Reveal()); err == nil {
			t.Fatal("enabling code did not revoke old session")
		}
		if _, err := svc.Login(t.Context(), email, []byte(password), false); err != domain.CodeRequired {
			t.Fatal("legacy Login bypasses MFA")
		}
		result, err := svc.Login(t.Context(), email, []byte(password), true)
		if err != nil {
			t.Fatal(err)
		}
		m = take(t, "code", email)
		if _, err := svc.ResendCode(t.Context(), result.ChallengeID); err != domain.RateLimited {
			t.Fatal("resend was not limited")
		}
		wrong := "000000"
		if wrong == m.Code {
			wrong = "000001"
		}
		for i := range 3 {
			_, err := svc.CompleteLogin(t.Context(), result.ChallengeID, wrong)
			want := domain.CodeMismatch
			if i == 2 {
				want = domain.CodeReissued
			}
			if err != want {
				t.Fatal("failed-code outcome", err)
			}
		}
		newMail := take(t, "code", email)
		if newMail.Code == m.Code {
			t.Fatal("reissued code repeated its predecessor")
		}
		if _, err := svc.CompleteLogin(t.Context(), result.ChallengeID, m.Code); err != domain.CodeMismatch {
			t.Fatal("old code still works")
		}
		var winners atomic.Int32
		var wg sync.WaitGroup
		errs := make(chan error, 16)
		for range 16 {
			wg.Go(func() {
				_, err := svc.CompleteLogin(t.Context(), result.ChallengeID, newMail.Code)
				if err == nil {
					winners.Add(1)
				} else if err != domain.CodeExpired {
					errs <- err
				}
			})
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatal(err)
		}
		if winners.Load() != 1 {
			t.Fatal("code has more or fewer than one winner")
		}
	})
	t.Run("reset revokes sessions and consumes token atomically", func(t *testing.T) {
		const email = "reset@example.test"
		register(t, email)
		current := login(t, email)
		if err := svc.RequestToken(t.Context(), email, domain.ResetPassword); err != nil {
			t.Fatal(err)
		}
		link := token(take(t, "reset", email))
		if err := svc.ResetPassword(t.Context(), link, []byte("NewCorrectHorse7!")); err != nil {
			t.Fatal(err)
		}
		if _, err := sessions.Authenticate(t.Context(), current.Access.Reveal()); err == nil {
			t.Fatal("old session survived reset")
		}
		if _, err := svc.Login(t.Context(), email, []byte(password), true); err != domain.InvalidCredentials {
			t.Fatal("old password survived reset")
		}
		if err := svc.ResetPassword(t.Context(), link, []byte("AnotherPassword5!")); err != domain.TokenExpired {
			t.Fatal("reset token replayed")
		}
		if _, err := svc.Login(t.Context(), email, []byte("NewCorrectHorse7!"), true); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("exhausted codes cannot be reset by a new login", func(t *testing.T) {
		const email = "budget@example.test"
		register(t, email)
		current := login(t, email)
		change, err := svc.StartCodeChange(t.Context(), current.Record, []byte(password), true)
		if err != nil {
			t.Fatal(err)
		}
		if err := svc.CompleteCodeChange(t.Context(), current.Record, change.ChallengeID, take(t, "code", email).Code, true); err != nil {
			t.Fatal(err)
		}
		challenge, err := svc.Login(t.Context(), email, []byte(password), true)
		if err != nil {
			t.Fatal(err)
		}
		for generation := range 5 {
			letter := take(t, "code", email)
			wrong := "000000"
			if wrong == letter.Code {
				wrong = "000001"
			}
			for attempt := range 3 {
				_, err := svc.CompleteLogin(t.Context(), challenge.ChallengeID, wrong)
				want := domain.CodeMismatch
				if attempt == 2 {
					want = domain.CodeReissued
					if generation == 4 {
						want = domain.CodeExpired
					}
				}
				if err != want {
					t.Fatalf("generation %d attempt %d: %v", generation, attempt, err)
				}
			}
		}
		if _, err := svc.Login(t.Context(), email, []byte(password), true); err != domain.RateLimited {
			t.Fatal("login resets exhausted budget", err)
		}
		now = now.Add(11 * time.Minute)
		if _, err := svc.Login(t.Context(), email, []byte(password), true); err != nil {
			t.Fatal("budget never recovers", err)
		}
	})
	t.Run("reauthentication budget is shared by security mutations", func(t *testing.T) {
		const email = "reauth@example.test"
		register(t, email)
		current := login(t, email)
		for i := range 5 {
			var err error
			switch i % 3 {
			case 0:
				err = svc.ChangePassword(t.Context(), current.Record, []byte("WrongPassword1!"), []byte("NewPassword1!"))
			case 1:
				_, err = svc.StartCodeChange(t.Context(), current.Record, []byte("WrongPassword1!"), true)
			case 2:
				err = svc.StartEmailChange(t.Context(), current.Record, "changed@example.test", []byte("WrongPassword1!"))
			}
			want := domain.InvalidCredentials
			if i == 4 {
				want = domain.LoginLocked
			}
			if err != want {
				t.Fatal("reauthentication counter lost", err)
			}
		}
		if err := svc.ChangePassword(t.Context(), current.Record, []byte(password), []byte("NewPassword1!")); err != domain.LoginLocked {
			t.Fatal("reauthentication lock bypassed", err)
		}
		if _, err := sessions.Authenticate(t.Context(), current.Access.Reveal()); err != nil {
			t.Fatal("failed proof revoked session")
		}
	})
	t.Run("logout all respects new session cooldown and sessions stay owned", func(t *testing.T) {
		register(t, "cooldown@example.test")
		register(t, "other-owner@example.test")
		current := login(t, "cooldown@example.test")
		other := login(t, "other-owner@example.test")
		if err := svc.LogoutAll(t.Context(), current.Record); err != domain.NewDeviceCooldown {
			t.Fatal("logout all bypasses cooldown", err)
		}
		if err := svc.RevokeSession(t.Context(), current.Record, other.Record.ID); err != domain.NewDeviceCooldown {
			t.Fatal("revoke bypasses cooldown", err)
		}
		if _, err := sessions.Authenticate(t.Context(), current.Access.Reveal()); err != nil {
			t.Fatal("cooldown revoked own session")
		}
		if _, err := sessions.Authenticate(t.Context(), other.Access.Reveal()); err != nil {
			t.Fatal("cooldown revoked another owner's session")
		}
		if err := svc.RevokeSession(t.Context(), current.Record, current.Record.ID); err != nil {
			t.Fatal("cannot revoke own current session", err)
		}
	})
	t.Run("email cancellation invalidates confirmation and confirmation revokes sessions", func(t *testing.T) {
		const oldEmail = "old-email@example.test"
		const newEmail = "new-email@example.test"
		register(t, oldEmail)
		actor := login(t, oldEmail)
		if err := svc.StartEmailChange(t.Context(), actor.Record, newEmail, []byte(password)); err != nil {
			t.Fatal(err)
		}
		confirm := token(take(t, "change_email", newEmail))
		cancel := token(take(t, "cancel_email", oldEmail))
		if err := svc.CancelEmailChange(t.Context(), cancel); err != nil {
			t.Fatal(err)
		}
		if err := svc.ConfirmEmailChange(t.Context(), confirm); err != domain.TokenExpired {
			t.Fatal("cancelled confirmation accepted", err)
		}
		now = now.Add(61 * time.Second)
		actor, err = sessions.Refresh(t.Context(), actor.Refresh.Reveal())
		if err != nil {
			t.Fatal(err)
		}
		if err := svc.StartEmailChange(t.Context(), actor.Record, newEmail, []byte(password)); err != nil {
			t.Fatal(err)
		}
		confirm = token(take(t, "change_email", newEmail))
		if err := svc.ConfirmEmailChange(t.Context(), confirm); err != nil {
			t.Fatal(err)
		}
		if _, err := sessions.Authenticate(t.Context(), actor.Access.Reveal()); err == nil {
			t.Fatal("email change kept session")
		}
		if _, err := svc.Login(t.Context(), oldEmail, []byte(password), true); err != domain.InvalidCredentials {
			t.Fatal("old email still authenticates")
		}
		login(t, newEmail)
	})
	t.Run("session revocation stays owner scoped after cooldown", func(t *testing.T) {
		register(t, "established@example.test")
		register(t, "foreign-session@example.test")
		actor := login(t, "established@example.test")
		own := login(t, "established@example.test")
		foreign := login(t, "foreign-session@example.test")
		now = now.Add(25 * time.Hour)
		actor, err = sessions.Refresh(t.Context(), actor.Refresh.Reveal())
		if err != nil {
			t.Fatal(err)
		}
		foreign, err = sessions.Refresh(t.Context(), foreign.Refresh.Reveal())
		if err != nil {
			t.Fatal(err)
		}
		if err := svc.RevokeSession(t.Context(), actor.Record, foreign.Record.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := sessions.Authenticate(t.Context(), foreign.Access.Reveal()); err != nil {
			t.Fatal("revoked foreign session")
		}
		if err := svc.RevokeSession(t.Context(), actor.Record, own.Record.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := sessions.Refresh(t.Context(), own.Refresh.Reveal()); err == nil {
			t.Fatal("owned revoked session refreshes")
		}
		if err := svc.LogoutAll(t.Context(), actor.Record); err != nil {
			t.Fatal(err)
		}
		if _, err := sessions.Authenticate(t.Context(), actor.Access.Reveal()); err == nil {
			t.Fatal("logout all kept actor")
		}
		if _, err := sessions.Authenticate(t.Context(), foreign.Access.Reveal()); err != nil {
			t.Fatal("logout all revoked foreign session")
		}
	})
	t.Run("failed password lockout known and unknown", func(t *testing.T) {
		register(t, "locked@example.test")
		for _, email := range []string{"locked@example.test", "missing@example.test"} {
			for i := range 6 {
				_, err := svc.Login(t.Context(), email, []byte("WrongPassword1!"), true)
				want := domain.InvalidCredentials
				if i >= 4 {
					want = domain.LoginLocked
				}
				if err != want {
					t.Fatal("lockout outcome", err)
				}
			}
		}
		if err := svc.RequestToken(t.Context(), "absent@example.test", domain.ResetPassword); err != nil {
			t.Fatal("unknown email enumeration")
		}
	})
	t.Run("cleanup does not delete a concurrently renewed budget", func(t *testing.T) {
		bucket := domain.Digest{99}
		if _, err := pool.Exec(t.Context(), `INSERT INTO auth.login_limits(bucket,attempts,expires_at) VALUES($1,5,$2)`, bucket[:], now.Add(-time.Minute)); err != nil {
			t.Fatal(err)
		}
		locked := make(chan struct{})
		release := make(chan struct{})
		result := make(chan error, 1)
		go func() {
			result <- store.WithAccount(t.Context(), security.Selector{Email: "no-account@example.test"}, func(tx security.Unit) error {
				n, err := tx.ReserveBudget(t.Context(), bucket, now, 5, 15*time.Minute)
				if err != nil {
					close(locked)
					return err
				}
				if n != 1 {
					close(locked)
					return errors.New("budget not reset")
				}
				close(locked)
				<-release
				return nil
			})
		}()
		<-locked
		ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
		_, _, cleanupErr := store.ClaimMail(ctx, now)
		cancel()
		close(release)
		if err := <-result; err != nil {
			t.Fatal(err)
		}
		if cleanupErr != nil {
			t.Fatal("cleanup blocked on active budget", cleanupErr)
		}
		var n int
		var expires time.Time
		if err := pool.QueryRow(t.Context(), `SELECT attempts,expires_at FROM auth.login_limits WHERE bucket=$1`, bucket[:]).Scan(&n, &expires); err != nil || n != 1 || !expires.Equal(now.Add(15*time.Minute)) {
			t.Fatal("renewed budget was deleted or reset")
		}
	})
	t.Run("queue payload encrypted and lease fenced", func(t *testing.T) {
		const email = "sealed@example.test"
		if err := svc.Register(t.Context(), email, []byte(password)); err != nil {
			t.Fatal(err)
		}
		var raw []byte
		if err := pool.QueryRow(t.Context(), `SELECT m.payload FROM auth.mail_outbox m JOIN auth.credentials c USING(subject_id) WHERE c.identifier=$1`, email).Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), email) || strings.Contains(string(raw), "https://") {
			t.Fatal("queue contains cleartext")
		}
		lease, found, err := store.ClaimMail(t.Context(), now)
		if err != nil || !found {
			t.Fatal("claim", err)
		}
		other := lease
		other.Token[0] ^= 1
		if err := store.FinishMail(t.Context(), other, now, "delivered", time.Time{}); err == nil {
			t.Fatal("unowned lease completed")
		}
		if err := store.FinishMail(t.Context(), lease, now, "delivered", time.Time{}); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("mail failure rolls back registration", func(t *testing.T) {
		if _, err := pool.Exec(t.Context(), `ALTER TABLE auth.mail_outbox ADD CONSTRAINT reject_insert CHECK (false) NOT VALID`); err != nil {
			t.Fatal(err)
		}
		err := svc.Register(t.Context(), "rollback@example.test", []byte(password))
		if err == nil {
			t.Fatal("expected queue failure")
		}
		if _, err := pool.Exec(t.Context(), `ALTER TABLE auth.mail_outbox DROP CONSTRAINT reject_insert`); err != nil {
			t.Fatal(err)
		}
		var count int
		if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM auth.credentials WHERE identifier='rollback@example.test'`).Scan(&count); err != nil || count != 0 {
			t.Fatal("credential committed without mail")
		}
	})
	t.Run("recovery codes are single display owner scoped atomic and revision bound", func(t *testing.T) {
		const email = "recovery@example.test"
		register(t, email)
		actor := login(t, email)
		change, err := svc.StartCodeChange(t.Context(), actor.Record, []byte(password), true)
		if err != nil {
			t.Fatal(err)
		}
		if err := svc.CompleteCodeChange(t.Context(), actor.Record, change.ChallengeID, take(t, "code", email).Code, true); err != nil {
			t.Fatal(err)
		}
		pending, err := svc.Login(t.Context(), email, []byte(password), true)
		if err != nil {
			t.Fatal(err)
		}
		actor, err = svc.CompleteLogin(t.Context(), pending.ChallengeID, take(t, "code", email).Code)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := svc.StartRecoveryCodes(t.Context(), actor.Record, []byte("wrong")); err != domain.InvalidCredentials {
			t.Fatal("generation without password proof")
		}
		start, err := svc.StartRecoveryCodes(t.Context(), actor.Record, []byte(password))
		if err != nil {
			t.Fatal(err)
		}
		otp := take(t, "recovery_code", email).Code
		register(t, "recovery-foreign@example.test")
		foreign := login(t, "recovery-foreign@example.test")
		if _, err := svc.CompleteRecoveryCodes(t.Context(), foreign.Record, start.ChallengeID, otp); err == nil {
			t.Fatal("foreign generation accepted")
		}
		if _, err := svc.CompleteRecoveryCodes(t.Context(), actor.Record, pending.ChallengeID, otp); err == nil {
			t.Fatal("wrong purpose accepted")
		}
		codes, err := svc.CompleteRecoveryCodes(t.Context(), actor.Record, start.ChallengeID, otp)
		if err != nil || len(codes) != 8 {
			t.Fatal("generation failed", err)
		}
		if _, err := svc.CompleteRecoveryCodes(t.Context(), actor.Record, start.ChallengeID, otp); err == nil {
			t.Fatal("set revealed twice")
		}
		letter := take(t, "backup_codes", email)
		if letter.Code != "" || strings.Contains(letter.URL, codes[0]) {
			t.Fatal("recovery secret queued as mail")
		}
		var digests string
		if err := pool.QueryRow(t.Context(), `SELECT string_agg(encode(secret_digest,'hex'),',') FROM auth.recovery_codes WHERE subject_id=$1`, actor.Record.SubjectID.Bytes()).Scan(&digests); err != nil {
			t.Fatal(err)
		}
		for _, code := range codes {
			if strings.Contains(digests, strings.ReplaceAll(code, "-", "")) {
				t.Fatal("raw code persisted")
			}
		}
		status, err := svc.Credentials(t.Context(), actor.Record)
		if err != nil || status.RecoveryCodesRemaining != 8 {
			t.Fatal("wrong initial count", err)
		}
		// Distinct password-approved challenges compete for the same code.
		// Store fixtures preserve the live owner/revision and use separate IDs.
		var challenges [16]domain.ID
		for i := range challenges {
			challenges[i][0] = byte(i + 1)
			challenges[i][15] = 91
			if _, err := pool.Exec(t.Context(), `INSERT INTO auth.security_challenges(challenge_id,subject_id,purpose,secret_digest,revision,expires_at,sent_at) SELECT $1,subject_id,'login',$2,revision,$3,$4 FROM auth.account_security WHERE subject_id=$5`, challenges[i][:], make([]byte, 32), now.Add(time.Minute), now, actor.Record.SubjectID.Bytes()); err != nil {
				t.Fatal(err)
			}
		}
		var won atomic.Int32
		var wg sync.WaitGroup
		for _, id := range challenges {
			wg.Go(func() {
				result, err := svc.CompleteRecoveryLogin(t.Context(), id, codes[0])
				if err == nil {
					won.Add(1)
					if _, err := sessions.Authenticate(t.Context(), result.Access.Reveal()); err != nil {
						t.Error("unusable recovery session")
					}
				} else if err != domain.CodeMismatch && err != domain.RateLimited {
					t.Error("unexpected recovery race rejection", err)
				}
			})
		}
		wg.Wait()
		if won.Load() != 1 {
			t.Fatal("same recovery code created multiple sessions")
		}
		status, err = svc.Credentials(t.Context(), actor.Record)
		if err != nil || status.RecoveryCodesRemaining != 7 {
			t.Fatal("wrong count after race", err)
		}
		pending, err = svc.Login(t.Context(), email, []byte(password), true)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := svc.CompleteRecoveryLogin(t.Context(), pending.ChallengeID, codes[1]); err != domain.RateLimited {
			t.Fatal("new challenge reset subject budget")
		}
		now = now.Add(16 * time.Minute)
		actor, err = sessions.Refresh(t.Context(), actor.Refresh.Reveal())
		if err != nil {
			t.Fatal(err)
		}
		// Session and email failures must roll consumption and challenge use back.
		pending, err = svc.Login(t.Context(), email, []byte(password), true)
		if err != nil {
			t.Fatal(err)
		}
		for _, table := range []string{"sessions", "mail_outbox"} {
			if _, err := pool.Exec(t.Context(), `ALTER TABLE auth.`+table+` ADD CONSTRAINT reject_recovery CHECK(false) NOT VALID`); err != nil {
				t.Fatal(err)
			}
			_, consumeErr := svc.CompleteRecoveryLogin(t.Context(), pending.ChallengeID, codes[1])
			if _, err := pool.Exec(t.Context(), `ALTER TABLE auth.`+table+` DROP CONSTRAINT reject_recovery`); err != nil {
				t.Fatal(err)
			}
			if consumeErr != domain.Unavailable {
				t.Fatal("injected failure not returned")
			}
			status, err = svc.Credentials(t.Context(), actor.Record)
			if err != nil || status.RecoveryCodesRemaining != 7 {
				t.Fatal("code committed without session/mail", err)
			}
		}
		if _, err := svc.CompleteRecoveryLogin(t.Context(), pending.ChallengeID, strings.ToUpper(codes[1])); err != nil {
			t.Fatal("rolled-back code no longer usable", err)
		}
		// A failure while replacing the set leaves the previous set intact.
		start, err = svc.StartRecoveryCodes(t.Context(), actor.Record, []byte(password))
		if err != nil {
			t.Fatal(err)
		}
		otp = take(t, "recovery_code", email).Code
		if _, err := pool.Exec(t.Context(), `ALTER TABLE auth.mail_outbox ADD CONSTRAINT reject_recovery CHECK(false) NOT VALID`); err != nil {
			t.Fatal(err)
		}
		_, generateErr := svc.CompleteRecoveryCodes(t.Context(), actor.Record, start.ChallengeID, otp)
		if _, err := pool.Exec(t.Context(), `ALTER TABLE auth.mail_outbox DROP CONSTRAINT reject_recovery`); err != nil {
			t.Fatal(err)
		}
		if generateErr != domain.Unavailable {
			t.Fatal("replacement did not fail")
		}
		status, err = svc.Credentials(t.Context(), actor.Record)
		if err != nil || status.RecoveryCodesRemaining != 6 {
			t.Fatal("replacement destroyed previous set", err)
		}
		fresh, err := svc.CompleteRecoveryCodes(t.Context(), actor.Record, start.ChallengeID, otp)
		if err != nil || len(fresh) != 8 {
			t.Fatal("replacement failed", err)
		}
		pending, err = svc.Login(t.Context(), email, []byte(password), true)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := svc.CompleteRecoveryLogin(t.Context(), pending.ChallengeID, codes[2]); err != domain.CodeMismatch {
			t.Fatal("old set survived replacement")
		}
		if _, err := svc.CompleteRecoveryLogin(t.Context(), pending.ChallengeID, fresh[0]); err != nil {
			t.Fatal("replacement code failed", err)
		}
		if err := svc.ChangePassword(t.Context(), actor.Record, []byte(password), []byte("ChangedHorse8!")); err != nil {
			t.Fatal(err)
		}
		pending, err = svc.Login(t.Context(), email, []byte("ChangedHorse8!"), true)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := svc.CompleteRecoveryLogin(t.Context(), pending.ChallengeID, fresh[1]); err != domain.CodeMismatch {
			t.Fatal("password change did not revoke recovery set")
		}
		if _, err := svc.Login(t.Context(), email, []byte("ChangedHorse8!"), false); err != domain.CodeRequired {
			t.Fatal("legacy password-only bypass")
		}
	})

	t.Run("recovery migration down and up preserve account security", func(t *testing.T) {
		if _, err := pool.Exec(t.Context(), migrations.RecoveryDown); err != nil {
			t.Fatal(err)
		}
		var absent bool
		if err := pool.QueryRow(t.Context(), `SELECT to_regclass('auth.recovery_codes') IS NULL`).Scan(&absent); err != nil || !absent {
			t.Fatal("recovery storage remains after downgrade")
		}
		if _, err := pool.Exec(t.Context(), migrations.RecoveryUp); err != nil {
			t.Fatal(err)
		}
		var count int
		if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM auth.recovery_codes`).Scan(&count); err != nil || count != 0 {
			t.Fatal("old recovery codes survived downgrade")
		}
		if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM auth.account_security WHERE email_verified`).Scan(&count); err != nil || count == 0 {
			t.Fatal("downgrade removed account security")
		}
	})

}
