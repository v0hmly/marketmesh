package sessionassert

import (
	"crypto/ed25519"
	"crypto/rand"
	"testing"
	"time"
)

// FuzzVerify проверяет, что разбор произвольного ввода не приводит к панике
// и всегда завершается ошибкой или валидными claims.
func FuzzVerify(f *testing.F) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		f.Fatalf("generate key: %v", err)
	}
	iss, err := NewIssuer(priv, "k1", "auth.marketmesh")
	if err != nil {
		f.Fatalf("NewIssuer: %v", err)
	}
	validToken, err := iss.Issue(IssueParams{
		Audience:  "user-service",
		Subject:   "u-123",
		SessionID: "s-456",
		TTL:       time.Minute,
		AuthTime:  time.Now(),
	})
	if err != nil {
		f.Fatalf("Issue: %v", err)
	}
	ks := NewStaticKeySet()
	if err := ks.Add("k1", pub); err != nil {
		f.Fatalf("Add: %v", err)
	}
	v, err := NewVerifier("auth.marketmesh", "user-service", ks)
	if err != nil {
		f.Fatalf("NewVerifier: %v", err)
	}

	f.Add(validToken)
	f.Add("")
	f.Add("a.b.c")
	f.Add("..")
	f.Add("###.###.###")
	f.Add("eyJhbGciOiJub25lIn0.e30.")

	f.Fuzz(func(t *testing.T, token string) {
		claims, err := v.Verify(token)
		if err == nil && claims == nil {
			t.Fatal("nil claims without error")
		}
	})
}
