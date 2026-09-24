package oidc

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	jose "github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"golang.org/x/oauth2"
)

func TestExchangeRejectsInvalidIdentity(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	other, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	for _, scenario := range []string{"valid", "wrong issuer", "wrong audience", "expired", "wrong nonce", "unverified email", "wrong signature", "missing id token"} {
		t.Run(scenario, func(t *testing.T) {
			var issuer string
			mux := http.NewServeMux()
			mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"issuer": issuer, "authorization_endpoint": issuer + "/authorize", "token_endpoint": issuer + "/token", "jwks_uri": issuer + "/keys", "id_token_signing_alg_values_supported": []string{"RS256"}})
			})
			mux.HandleFunc("/keys", func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{{Key: &key.PublicKey, KeyID: "test", Algorithm: "RS256", Use: "sig"}}})
			})
			mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
				if err := r.ParseForm(); err != nil {
					t.Error(err)
				}
				client, secret, _ := r.BasicAuth()
				if client != "staff-test" || secret != "test-client-secret" || r.PostForm.Get("code") != "one-time-code" || r.PostForm.Get("code_verifier") != "verifier" || r.PostForm.Get("redirect_uri") != "https://staff.test/sso/callback" {
					t.Error("exchange lost client binding or PKCE")
				}
				signKey := key
				if scenario == "wrong signature" {
					signKey = other
				}
				signer, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.RS256, Key: signKey}, (&jose.SignerOptions{}).WithType("JWT").WithHeader("kid", "test"))
				if err != nil {
					t.Error(err)
					return
				}
				claims := jwt.Claims{Issuer: issuer, Subject: "immutable-subject", Audience: jwt.Audience{"staff-test"}, Expiry: jwt.NewNumericDate(time.Now().Add(time.Hour))}
				extra := map[string]any{"nonce": "nonce", "email": "staff@example.test", "email_verified": true, "name": "Employee"}
				switch scenario {
				case "wrong issuer":
					claims.Issuer = "https://other-idp.test"
				case "wrong audience":
					claims.Audience = jwt.Audience{"buyer"}
				case "expired":
					claims.Expiry = jwt.NewNumericDate(time.Now().Add(-time.Hour))
				case "wrong nonce":
					extra["nonce"] = "another-login"
				case "unverified email":
					extra["email_verified"] = false
				}
				raw, err := jwt.Signed(signer).Claims(claims).Claims(extra).Serialize()
				if err != nil {
					t.Error(err)
					return
				}
				result := map[string]any{"access_token": "unused-by-staff", "token_type": "Bearer", "id_token": raw}
				if scenario == "missing id token" {
					delete(result, "id_token")
				}
				w.Header().Set("Content-Type", "application/json")
				_ = json.NewEncoder(w).Encode(result)
			})
			server := httptest.NewServer(mux)
			defer server.Close()
			issuer = server.URL
			provider, err := New(t.Context(), issuer, "staff-test", "test-client-secret", "https://staff.test/sso/callback", server.Client())
			if err != nil {
				t.Fatal(err)
			}
			redirect, err := url.Parse(provider.Authorize("state", "nonce", "verifier"))
			if err != nil {
				t.Fatal(err)
			}
			if redirect.Query().Get("state") != "state" || redirect.Query().Get("nonce") != "nonce" || redirect.Query().Get("code_challenge") != oauth2.S256ChallengeFromVerifier("verifier") || redirect.Query().Get("code_challenge_method") != "S256" {
				t.Fatal("authorization request lost binding")
			}
			principal, err := provider.Exchange(context.Background(), "one-time-code", "verifier", "nonce")
			if scenario == "valid" {
				if err != nil || principal.Subject != "immutable-subject" || principal.Email != "staff@example.test" {
					t.Fatalf("valid identity rejected: %v", err)
				}
			} else if err == nil {
				t.Fatal("invalid identity accepted")
			}
		})
	}
}
