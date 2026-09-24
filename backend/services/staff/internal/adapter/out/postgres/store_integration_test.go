//go:build integration

package postgres

import (
	"encoding/json"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/v0hmly/marketmesh/services/staff/internal/application"
)

// Uses explicit fixture credentials and removes only its own randomly named rows.
func TestAtomicStaffBindings(t *testing.T) {
	dsn := os.Getenv("STAFF_TEST_DSN")
	if file := os.Getenv("STAFF_TEST_CONFIG"); file != "" {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal("read test config")
		}
		var cfg struct{ DSN string }
		if err = json.Unmarshal(raw, &cfg); err != nil {
			t.Fatal("parse test config")
		}
		dsn = cfg.DSN
	}
	if dsn == "" {
		t.Skip("STAFF_TEST_DSN required")
	}
	pool, err := pgxpool.New(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	s := New(pool)
	nonce := time.Now().Format("20060102150405.000000000")
	p := application.Principal{Issuer: "https://integration-" + nonce + ".test", Subject: nonce, Email: "test-" + nonce + "@example.test", Name: "Test"}
	state := "state-" + nonce
	session := "session-" + nonce
	invite := "invite-" + nonce
	defer func() {
		ctx := t.Context()
		_, _ = pool.Exec(ctx, `DELETE FROM staff.sessions WHERE token_hash=$1`, session)
		_, _ = pool.Exec(ctx, `DELETE FROM staff.invites WHERE token_hash=$1`, invite)
		_, _ = pool.Exec(ctx, `DELETE FROM staff.members WHERE issuer=$1`, p.Issuer)
		_, _ = pool.Exec(ctx, `DELETE FROM staff.login_attempts WHERE state_hash=$1`, state)
	}()
	now := time.Now()
	if err = s.SaveLogin(t.Context(), application.Login{StateHash: state, BrowserHash: "browser", Nonce: "nonce", Verifier: "verifier", ExpiresAt: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.ConsumeLogin(t.Context(), state, "other", now); !errors.Is(err, application.ErrInvalidLogin) {
		t.Fatal("wrong browser consumed challenge")
	}
	var wg sync.WaitGroup
	results := make(chan error, 2)
	for range 2 {
		wg.Go(func() { _, err := s.ConsumeLogin(t.Context(), state, "browser", now); results <- err })
	}
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		} else if !errors.Is(err, application.ErrInvalidLogin) {
			t.Fatal(err)
		}
	}
	if successes != 1 {
		t.Fatalf("challenge consumed %d times", successes)
	}
	if err = s.SaveSession(t.Context(), session, p, now.Add(time.Hour), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Session(t.Context(), session, now.Add(2*time.Minute), now.Add(3*time.Minute)); !errors.Is(err, application.ErrUnauthenticated) {
		t.Fatal("expired idle session accepted")
	}
	_, err = pool.Exec(t.Context(), `INSERT INTO staff.invites VALUES ($1,$2,'support','Test inviter',$3,NULL)`, invite, p.Email, now.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	other := p
	other.Email = "other@example.test"
	got, err := s.Invite(t.Context(), invite, other, now, true)
	if err != nil || got.State != "wrong_account" || got.Role != "" || got.Inviter != "" {
		t.Fatal("wrong identity gained invite information or access")
	}
	outcomes := make(chan string, 2)
	for range 2 {
		wg.Go(func() {
			got, err := s.Invite(t.Context(), invite, p, now, true)
			if err != nil {
				outcomes <- "error"
				return
			}
			outcomes <- got.State
		})
	}
	wg.Wait()
	close(outcomes)
	active, used := 0, 0
	for outcome := range outcomes {
		switch outcome {
		case "active":
			active++
		case "used":
			used++
		default:
			t.Fatalf("unexpected outcome %s", outcome)
		}
	}
	if active != 1 || used != 1 {
		t.Fatal("invite not one-use")
	}
	// Role was bound to the immutable issuer/subject, never to a client-selected email.
	if err = s.SaveSession(t.Context(), session+"-second", p, now.Add(time.Hour), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_, _ = pool.Exec(t.Context(), `DELETE FROM staff.sessions WHERE token_hash=$1`, session+"-second")
	}()
	current, err := s.Session(t.Context(), session+"-second", now, now.Add(time.Minute))
	if err != nil || current.Role != "support" {
		t.Fatal("membership missing")
	}
}
