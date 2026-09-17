package authsession

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	authv1 "github.com/v0hmly/marketmesh/api/gen/go/auth/v1"
	"github.com/v0hmly/marketmesh/platform/sessionassert"
	"github.com/v0hmly/marketmesh/services/user/internal/application/identity"
	"google.golang.org/grpc"
)

type fakeAuth struct {
	authv1.AuthInternalServiceClient
	jwks                   string
	response               *authv1.VerifyAssertionResponse
	err                    error
	keysCalls, verifyCalls int
	wait                   bool
	after                  func()
}

func (f *fakeAuth) GetSigningKeys(ctx context.Context, _ *authv1.GetSigningKeysRequest, _ ...grpc.CallOption) (*authv1.GetSigningKeysResponse, error) {
	f.keysCalls++
	if f.wait {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	return &authv1.GetSigningKeysResponse{JwksJson: f.jwks}, nil
}
func (f *fakeAuth) VerifyAssertion(context.Context, *authv1.VerifyAssertionRequest, ...grpc.CallOption) (*authv1.VerifyAssertionResponse, error) {
	f.verifyCalls++
	if f.after != nil {
		f.after()
	}
	return f.response, f.err
}
func fixture(t *testing.T) (*fakeAuth, *Verifier, string, func(string, string, []string) string, *time.Time) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte("0123456789abcdef")
	issue := func(issuer, audience string, scopes []string) string {
		signer, err := sessionassert.NewIssuer(priv, "current", issuer, sessionassert.WithIssuerClock(func() time.Time { return now }))
		if err != nil {
			t.Fatal(err)
		}
		token, err := signer.Issue(sessionassert.IssueParams{Audience: audience, Subject: base64.RawURLEncoding.EncodeToString(raw), SessionID: "session", TTL: 30 * time.Second, AuthTime: now, ACR: "password", AMR: []string{"pwd"}, Scopes: scopes})
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	payload, _ := json.Marshal(struct {
		Keys []publicKey `json:"keys"`
	}{[]publicKey{{Kty: "OKP", Crv: "Ed25519", Alg: "EdDSA", Use: "sig", Kid: "current", X: base64.RawURLEncoding.EncodeToString(pub), VerifyUntil: now.Add(time.Minute)}}})
	client := &fakeAuth{jwks: string(payload), response: &authv1.VerifyAssertionResponse{SubjectId: raw, SessionId: "session", ExpiresAtUnix: now.Add(30 * time.Second).Unix()}}
	verifier, err := New(client, Config{Issuer: "auth", MaxTTL: 30 * time.Second, Timeout: time.Second, Clock: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	return client, verifier, issue("auth", "user", []string{readScope, writeScope}), issue, &now
}
func TestVerifyFreshRevocationAndScopes(t *testing.T) {
	client, v, token, issue, _ := fixture(t)
	p, err := v.Verify(context.Background(), token)
	if err != nil || !p.CanRead || !p.CanWrite {
		t.Fatalf("verification = %+v %v", p, err)
	}
	token = issue("auth", "user", []string{readScope})
	p, err = v.Verify(context.Background(), token)
	if err != nil || !p.CanRead || p.CanWrite {
		t.Fatalf("read scope = %+v %v", p, err)
	}
	client.err = errors.New("revoked")
	if _, err = v.Verify(context.Background(), token); !errors.Is(err, identity.ErrUnauthenticated) {
		t.Fatal(err)
	}
	if client.keysCalls != 3 || client.verifyCalls != 3 {
		t.Fatalf("cached authorization: keys=%d checks=%d", client.keysCalls, client.verifyCalls)
	}
}
func TestVerifyRejectsInvalidAssertions(t *testing.T) {
	for _, name := range []string{"signature", "audience", "issuer", "key expired", "unknown kid", "subject mismatch", "session mismatch", "expiry mismatch", "expired during rpc", "timeout", "external bearer", "external cookie", "oversize", "duplicate kid", "too many keys"} {
		t.Run(name, func(t *testing.T) {
			client, v, token, issue, now := fixture(t)
			switch name {
			case "signature":
				parts := strings.Split(token, ".")
				sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
				sig[0] ^= 1
				parts[2] = base64.RawURLEncoding.EncodeToString(sig)
				token = strings.Join(parts, ".")
			case "audience":
				token = issue("auth", "orders", nil)
			case "issuer":
				token = issue("other", "user", nil)
			case "key expired":
				client.jwks = strings.ReplaceAll(client.jwks, now.Add(time.Minute).Format(time.RFC3339), now.Format(time.RFC3339))
			case "unknown kid":
				client.jwks = strings.ReplaceAll(client.jwks, "current", "other")
			case "subject mismatch":
				client.response.SubjectId = []byte("fedcba9876543210")
			case "session mismatch":
				client.response.SessionId = "other"
			case "expiry mismatch":
				client.response.ExpiresAtUnix++
			case "expired during rpc":
				client.after = func() { *now = now.Add(30 * time.Second) }
			case "timeout":
				client.wait = true
				v.config.Timeout = time.Millisecond
			case "external bearer":
				token = "Bearer " + token
			case "external cookie":
				token = "__Host-mm-session=value"
			case "oversize":
				token = strings.Repeat("x", MaxAssertionBytes+1)
			case "duplicate kid", "too many keys":
				var document struct {
					Keys []publicKey `json:"keys"`
				}
				_ = json.Unmarshal([]byte(client.jwks), &document)
				count := 2
				if name == "too many keys" {
					count = maxKeys + 1
				}
				for len(document.Keys) < count {
					document.Keys = append(document.Keys, document.Keys[0])
				}
				encoded, _ := json.Marshal(document)
				client.jwks = string(encoded)
			}
			if _, err := v.Verify(context.Background(), token); !errors.Is(err, identity.ErrUnauthenticated) {
				t.Fatalf("accepted invalid input: %v", err)
			}
		})
	}
}
func TestKeyTrustExpiresWithoutRefresh(t *testing.T) {
	client, _, _, _, now := fixture(t)
	keys, err := parseKeys(client.jwks, func() time.Time { return *now })
	if err != nil {
		t.Fatal(err)
	}
	if _, err = keys.Key("current"); err != nil {
		t.Fatal(err)
	}
	*now = now.Add(time.Minute)
	if _, err = keys.Key("current"); !errors.Is(err, sessionassert.ErrUnknownKeyID) {
		t.Fatal(err)
	}
}

func TestReadyRequiresUsableKeysAndAuthAvailability(t *testing.T) {
	client, v, _, _, now := fixture(t)
	if err := v.Ready(context.Background()); err != nil {
		t.Fatal(err)
	}
	if client.verifyCalls != 0 {
		t.Fatal("readiness checked a user session")
	}
	*now = now.Add(time.Minute)
	if err := v.Ready(context.Background()); err == nil {
		t.Fatal("expired keys reported ready")
	}
	client.wait = true
	v.config.Timeout = time.Millisecond
	if err := v.Ready(context.Background()); err == nil {
		t.Fatal("unavailable auth reported ready")
	}
}

// Auth publishes up to 64 trusted keys during planned rotations.
func TestAuthRotationKeyCountCompatibility(t *testing.T) {
	for _, count := range []int{33, 64, 65} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			client, v, token, _, now := fixture(t)
			var document struct {
				Keys []publicKey `json:"keys"`
			}
			if err := json.Unmarshal([]byte(client.jwks), &document); err != nil {
				t.Fatal(err)
			}
			for len(document.Keys) < count {
				pub, _, err := ed25519.GenerateKey(rand.Reader)
				if err != nil {
					t.Fatal(err)
				}
				document.Keys = append(document.Keys, publicKey{Kty: "OKP", Crv: "Ed25519", Alg: "EdDSA", Use: "sig", Kid: fmt.Sprintf("rotation-%03d", len(document.Keys)), X: base64.RawURLEncoding.EncodeToString(pub), VerifyUntil: now.Add(time.Hour)})
			}
			raw, err := json.Marshal(document)
			if err != nil {
				t.Fatal(err)
			}
			client.jwks = string(raw)
			_, err = v.Verify(context.Background(), token)
			if count <= 64 && err != nil {
				t.Fatalf("valid Auth rotation rejected: %v", err)
			}
			if count > 64 && !errors.Is(err, identity.ErrUnauthenticated) {
				t.Fatalf("excess keys accepted: %v", err)
			}
		})
	}
}

// Auth limits the source key file itself to 64 KiB. Its public document omits
// private material and signing timestamps, so even heavily escaped valid kids
// near the source-file limit fit the same public response budget.
func TestAuthMaximumKeyFileFitsJWKSByteLimit(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	var largestJWKS []byte
	for escaped := 0; escaped <= 256; escaped++ {
		var public []publicKey
		var source []map[string]any
		for index := 0; index < 64; index++ {
			suffix := fmt.Sprintf("-%03d", index)
			if escaped > 256-len(suffix) {
				break
			}
			kid := strings.Repeat("\x01", escaped) + strings.Repeat("k", 256-len(suffix)-escaped) + suffix
			pub := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{byte(index + 1)}, ed25519.SeedSize)).Public().(ed25519.PublicKey)
			x := base64.RawURLEncoding.EncodeToString(pub)
			from := now.Add(time.Duration(index) * time.Hour)
			until := from.Add(time.Hour)
			verifyUntil := until.Add(time.Minute)
			public = append(public, publicKey{Kty: "OKP", Crv: "Ed25519", Alg: "EdDSA", Use: "sig", Kid: kid, X: x, VerifyUntil: verifyUntil})
			item := map[string]any{"kid": kid, "public_key": x, "sign_from": from, "sign_until": until, "verify_until": verifyUntil}
			if index == 0 {
				item["private_key"] = base64.RawURLEncoding.EncodeToString(ed25519.NewKeyFromSeed(bytes.Repeat([]byte{1}, ed25519.SeedSize)))
			}
			source = append(source, item)
		}
		if len(source) != 64 {
			break
		}
		sourceJSON, err := json.Marshal(map[string]any{"keys": source})
		if err != nil {
			t.Fatal(err)
		}
		if len(sourceJSON) > 64*1024 {
			break
		}
		largestJWKS, err = json.Marshal(map[string]any{"keys": public})
		if err != nil {
			t.Fatal(err)
		}
		if len(largestJWKS) > maxJWKSBytes {
			t.Fatalf("valid Auth file exceeds response bound: %d", len(largestJWKS))
		}
	}
	if len(largestJWKS) < 50*1024 {
		t.Fatalf("fixture did not exercise source limit: %d", len(largestJWKS))
	}
	keys, err := parseKeys(string(largestJWKS), func() time.Time { return now })
	if err != nil || len(keys.keys) != 64 {
		t.Fatalf("bounded maximum document rejected: %v", err)
	}
	t.Logf("near-limit Auth source produces %d-byte 64-key JWKS", len(largestJWKS))
}

func TestAddressScopesAreIndependent(t *testing.T) {
	_, v, _, issue, _ := fixture(t)
	for _, tc := range []struct {
		scopes                                               []string
		profileRead, profileWrite, addressRead, addressWrite bool
	}{
		{[]string{readScope, writeScope}, true, true, false, false},
		{[]string{"user:addresses:read"}, false, false, true, false},
		{[]string{"user:addresses:write"}, false, false, false, true},
	} {
		token := issue(v.config.Issuer, "user", tc.scopes)
		p, e := v.Verify(context.Background(), token)
		if e != nil || p.CanRead != tc.profileRead || p.CanWrite != tc.profileWrite || p.CanReadAddresses != tc.addressRead || p.CanWriteAddresses != tc.addressWrite {
			t.Fatal(p, e)
		}
	}
}

func TestSettingsScopesAreIndependent(t *testing.T) {
	_, v, _, issue, _ := fixture(t)
	for _, tc := range []struct {
		scopes      []string
		read, write bool
	}{
		{[]string{readScope, writeScope, "user:addresses:read", "user:addresses:write"}, false, false},
		{[]string{"user:settings:read"}, true, false},
		{[]string{"user:settings:write"}, false, true},
	} {
		p, e := v.Verify(context.Background(), issue(v.config.Issuer, "user", tc.scopes))
		if e != nil || p.CanReadSettings != tc.read || p.CanWriteSettings != tc.write {
			t.Fatal(p, e)
		}
		if (tc.read || tc.write) && (p.CanRead || p.CanWrite || p.CanReadAddresses || p.CanWriteAddresses) {
			t.Fatal("settings scope widened other capabilities")
		}
	}
}
