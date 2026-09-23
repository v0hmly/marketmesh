package security_test

import (
	"testing"
	"time"

	"github.com/v0hmly/marketmesh/services/auth/internal/domain/credential"
	"github.com/v0hmly/marketmesh/services/auth/internal/domain/security"
)

func TestEmailAndPasswordPolicy(t *testing.T) {
	if got, err := security.Email(" Buyer@Example.Test "); err != nil || got != "buyer@example.test" {
		t.Fatal("normalization failed")
	}
	for _, email := range []string{"buyer", "name <buyer@example.test>", "a@example.test\r\nBcc:b@example.test", "a b@example.test", "покупатель@example.test", "a@@example.test"} {
		if _, err := security.Email(email); err == nil {
			t.Fatal("invalid email accepted")
		}
	}
	for _, raw := range []string{"weakpass", "PASSWORD123!", "password123!", "Password!!!!", "Password1234", "Aa1!\x00long"} {
		if _, err := security.NewPassword([]byte(raw)); err == nil {
			t.Fatal("weak new password accepted")
		}
	}
	password, err := security.NewPassword([]byte("CorrectHorse9!"))
	if err != nil {
		t.Fatal(err)
	}
	password.Destroy()
}

func TestChallengeBoundaries(t *testing.T) {
	now := time.Now().UTC()
	account := security.Account{Subject: credential.SubjectID{1}, Revision: 1}
	c := security.Challenge{ID: security.ID{1}, Subject: account.Subject, Purpose: security.ResetPassword, Revision: 1, ExpiresAt: now.Add(time.Minute)}
	if err := c.Check(account, security.ResetPassword, now); err != nil {
		t.Fatal(err)
	}
	if err := c.Check(account, security.ResetPassword, c.ExpiresAt); err != security.TokenExpired {
		t.Fatal("expiry is not exclusive")
	}
	if err := c.Check(account, security.VerifyEmail, now); err != security.TokenExpired {
		t.Fatal("purpose was not bound")
	}
	account.Revision++
	if err := c.Check(account, security.ResetPassword, now); err != security.TokenExpired {
		t.Fatal("old credential revision accepted")
	}
	account.Revision--
	c.UsedAt = &now
	if err := c.Check(account, security.ResetPassword, now); err != security.TokenUsed {
		t.Fatal("used token accepted")
	}
}
