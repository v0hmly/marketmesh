package internalgrpc

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"github.com/v0hmly/marketmesh/platform/logger"
	public "github.com/v0hmly/marketmesh/services/auth/internal/adapter/in/connectrpc"
	"io"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	authv1 "github.com/v0hmly/marketmesh/api/gen/go/auth/v1"
	"github.com/v0hmly/marketmesh/platform/sessionassert"
	"github.com/v0hmly/marketmesh/platform/workloadid"
	"github.com/v0hmly/marketmesh/services/auth/internal/adapter/out/sessionkeys"
	"github.com/v0hmly/marketmesh/services/auth/internal/domain/credential"
	domain "github.com/v0hmly/marketmesh/services/auth/internal/domain/session"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
)

type operations struct {
	keys    *sessionkeys.Manager
	record  domain.Record
	calls   atomic.Int32
	checks  atomic.Int32
	revoked atomic.Bool
}

func (o *operations) Exchange(ctx context.Context, token, audience string) (string, time.Time, error) {
	o.calls.Add(1)
	if token != "opaque-access" {
		return "", time.Time{}, domain.ErrInvalidSession
	}
	assertion, err := o.keys.Issue(ctx, o.record, audience, o.record.CreatedAt.Add(time.Minute))
	return assertion, o.record.CreatedAt.Add(time.Minute), err
}
func (o *operations) Check(_ context.Context, id domain.ID, subject credential.SubjectID, authTime time.Time) error {
	o.checks.Add(1)
	if o.revoked.Load() || id != o.record.ID || subject != o.record.SubjectID || !authTime.Equal(o.record.CreatedAt) {
		return domain.ErrInvalidSession
	}
	return nil
}

func TestPrivateRPCMTLSBoundary(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "keys.json")
	data, err := json.Marshal(map[string]any{"keys": []any{map[string]any{"kid": "test", "private_key": base64.RawURLEncoding.EncodeToString(priv), "public_key": base64.RawURLEncoding.EncodeToString(pub), "sign_from": now.Add(-time.Hour), "sign_until": now.Add(time.Hour), "verify_until": now.Add(2 * time.Hour)}}})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	audiences := map[string][]string{"user-service": {"profile:read"}, "other-service": {"read"}, "files": {"files:read", "files:write"}}
	keys, err := sessionkeys.New(sessionkeys.Config{Path: path, Issuer: "auth.marketmesh", MaxTTL: time.Minute, Clock: func() time.Time { return now }, Audiences: audiences})
	if err != nil {
		t.Fatal(err)
	}
	ops := &operations{keys: keys, record: domain.Record{ID: domain.ID{1}, SubjectID: credential.SubjectID{2}, Version: 1, CreatedAt: now, AccessExpiresAt: now.Add(time.Hour), RefreshExpiresAt: now.Add(time.Hour), ExpiresAt: now.Add(time.Hour)}}
	handler, err := New(ops, keys, Config{TrustDomain: "marketmesh.test", Environment: "dev", Issuer: "auth.marketmesh", AssertionTTL: time.Minute, Clock: func() time.Time { return now }, Audiences: audiences, AllowedOrigins: []string{"https://app.example"}})
	if err != nil {
		t.Fatal(err)
	}
	ca, caKey := certificateAuthority(t)
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	serverCert := issueCertificate(t, ca, caKey, "", true)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{serverCert}, ClientCAs: roots, ClientAuth: tls.RequireAndVerifyClientCert})), grpc.UnaryInterceptor(workloadid.UnaryServerInterceptor(handler.Policy())))
	authv1.RegisterAuthInternalServiceServer(server, handler)
	log, err := logger.New(logger.Config{Service: "auth", Version: "test", Environment: "test", Output: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	credentialsOps := &browserCredentials{}
	publicHandler, err := public.New(credentialsOps, browserVerifier{}, log)
	if err != nil {
		t.Fatal(err)
	}
	bridge, err := NewBrowser(publicHandler, handler.Policy())
	if err != nil {
		t.Fatal(err)
	}
	authv1.RegisterAuthBrowserServiceServer(server, bridge)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	client := func(t *testing.T, identity string) authv1.AuthInternalServiceClient {
		t.Helper()
		cfg := &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, ServerName: "localhost"}
		if identity != "" {
			cfg.Certificates = []tls.Certificate{issueCertificate(t, ca, caKey, identity, false)}
		}
		conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(credentials.NewTLS(cfg)))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		return authv1.NewAuthInternalServiceClient(conn)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	t.Run("browser workload contract", func(t *testing.T) {
		for _, identity := range []string{"", "spiffe://marketmesh.test/dev/gateway-in", "spiffe://marketmesh.test/prod/gateway-out", "spiffe://other.test/dev/gateway-out", "spiffe://marketmesh.test/dev/user-service", "spiffe://marketmesh.test/dev/gateway-out"} {
			cfg := &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, ServerName: "localhost"}
			if identity != "" {
				cfg.Certificates = []tls.Certificate{issueCertificate(t, ca, caKey, identity, false)}
			}
			conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(credentials.NewTLS(cfg)))
			if err != nil {
				t.Fatal(err)
			}
			c := authv1.NewAuthBrowserServiceClient(conn)
			_, err = c.BrowserRegisterCredentials(ctx, &authv1.BrowserRegisterCredentialsRequest{Request: &authv1.RegisterCredentialsRequest{Identifier: "name", Password: []byte("secret")}, Context: &authv1.BrowserContext{Origin: []string{"https://app.example"}}})
			_ = conn.Close()
			if identity == "spiffe://marketmesh.test/dev/gateway-out" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("unauthorized browser call accepted")
			}
		}
		if credentialsOps.calls.Load() != 1 {
			t.Fatal("unauthorized caller reached registration")
		}
	})
	t.Run("files require full scope and current session", func(t *testing.T) {
		own := workloadid.Scope{TrustDomain: "marketmesh.test", Environment: "dev", Cluster: "dc-a", Namespace: "marketmesh", ServiceAccount: "auth"}
		expected := own
		expected.ServiceAccount = "files"
		secured, err := workloadid.ScopedTLS(issueCertificate(t, ca, caKey, own.String(), true), roots, own, expected, "", true)
		if err != nil {
			t.Fatal(err)
		}
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		server := grpc.NewServer(grpc.Creds(credentials.NewTLS(secured)))
		scoped, err := NewScopedFiles(handler, expected)
		if err != nil {
			t.Fatal(err)
		}
		authv1.RegisterAuthInternalServiceServer(server, scoped)
		go func() { _ = server.Serve(listener) }()
		defer server.Stop()
		token, err := keys.Issue(ctx, ops.record, "files", now.Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		identities := []string{"", "spiffe://marketmesh.test/dev/files", strings.Replace(expected.String(), "/env/dev/", "/env/prod/", 1), strings.Replace(expected.String(), "/cluster/dc-a/", "/cluster/dc-b/", 1), strings.Replace(expected.String(), "/ns/marketmesh/", "/ns/foreign/", 1), strings.Replace(expected.String(), "/sa/files", "/sa/worker", 1), expected.String(), expected.String() + "/pod/01234567-89ab-cdef-0123-456789abcdef"}
		for _, identity := range identities {
			cfg := &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, ServerName: "localhost"}
			if identity != "" {
				cfg.Certificates = []tls.Certificate{issueCertificate(t, ca, caKey, identity, false)}
			}
			conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(credentials.NewTLS(cfg)))
			if err != nil {
				t.Fatal(err)
			}
			consumer := authv1.NewAuthInternalServiceClient(conn)
			result, err := consumer.VerifyAssertion(ctx, &authv1.VerifyAssertionRequest{Assertion: token})
			allowed := identity == expected.String() || strings.HasPrefix(identity, expected.String()+"/pod/")
			if allowed {
				if err != nil || len(result.GetSubjectId()) != 16 {
					t.Fatal("valid scoped assertion rejected", err)
				}
				if _, err := consumer.ExchangeBrowserSession(ctx, &authv1.ExchangeBrowserSessionRequest{}); status.Code(err) != codes.Unimplemented {
					t.Fatal("scoped listener exposes browser sessions")
				}
				ops.revoked.Store(true)
				if _, err := consumer.VerifyAssertion(ctx, &authv1.VerifyAssertionRequest{Assertion: token}); status.Code(err) != codes.Unauthenticated {
					t.Fatal("revoked session accepted")
				}
				ops.revoked.Store(false)
			} else if err == nil {
				t.Fatal("foreign scope accepted")
			}
			conn.Close()
		}
		if _, err := client(t, "spiffe://marketmesh.test/dev/files").VerifyAssertion(ctx, &authv1.VerifyAssertionRequest{Assertion: token}); status.Code(err) != codes.PermissionDenied {
			t.Fatal("legacy identity bypassed scoped listener", err)
		}
		if _, err := scoped.GetSigningKeys(context.Background(), &authv1.GetSigningKeysRequest{}); status.Code(err) != codes.Unauthenticated {
			t.Fatal("direct call lacks TLS authorization")
		}
	})
	gateway := client(t, "spiffe://marketmesh.test/dev/gateway-out")
	consumer := client(t, "spiffe://marketmesh.test/dev/user-service")
	other := client(t, "spiffe://marketmesh.test/dev/other-service")
	t.Run("caller boundary", func(t *testing.T) {
		for _, identity := range []string{"", "spiffe://marketmesh.test/dev/gateway-in", "spiffe://marketmesh.test/prod/gateway-out", "spiffe://other.test/dev/gateway-out", "spiffe://marketmesh.test/dev/user-service"} {
			t.Run(identity, func(t *testing.T) {
				_, err := client(t, identity).ExchangeSession(ctx, &authv1.ExchangeSessionRequest{Cookie: "__Host-mm-access=opaque-access", Audience: "user-service"})
				if err == nil {
					t.Fatal("unauthorized caller accepted")
				}
			})
		}
		if ops.calls.Load() != 0 {
			t.Fatal("unauthorized call reached session service")
		}
		if _, err := handler.GetSigningKeys(context.Background(), &authv1.GetSigningKeysRequest{}); status.Code(err) != codes.Unauthenticated {
			t.Fatalf("direct call: %v", err)
		}
	})
	t.Run("cookie parser", func(t *testing.T) {
		for _, cookie := range []string{"opaque-access", "__Host-mm-refresh=opaque-access", "__Host-mm-access=", "__Host-mm-access=opaque-access; __Host-mm-access=opaque-access", "__Host-mm-access=opaque-access\r\nInjected: yes", strings.Repeat("a", 8193)} {
			_, err := gateway.ExchangeSession(ctx, &authv1.ExchangeSessionRequest{Cookie: cookie, Audience: "user-service"})
			if status.Code(err) != codes.Unauthenticated {
				t.Fatalf("invalid cookie accepted: %v", err)
			}
		}
		if ops.calls.Load() != 0 {
			t.Fatal("invalid cookies reached application")
		}
		_, err := gateway.ExchangeSession(ctx, &authv1.ExchangeSessionRequest{Cookie: "__Host-mm-access=opaque-access", Audience: "unknown"})
		if status.Code(err) != codes.Unauthenticated {
			t.Fatal(err)
		}
	})
	t.Run("browser exchange enforces origin and workload before session access", func(t *testing.T) {
		valid := func() *authv1.ExchangeBrowserSessionRequest {
			return &authv1.ExchangeBrowserSessionRequest{Audience: "user-service", Context: &authv1.BrowserContext{Cookie: []string{"unrelated=value", "__Host-mm-access=opaque-access"}, Origin: []string{"https://app.example"}, SecFetchSite: []string{"same-origin"}}}
		}
		before := ops.calls.Load()
		for _, identity := range []string{"", "spiffe://marketmesh.test/dev/gateway-in", "spiffe://marketmesh.test/prod/gateway-out", "spiffe://other.test/dev/gateway-out", "spiffe://marketmesh.test/dev/user-service"} {
			if _, err := client(t, identity).ExchangeBrowserSession(ctx, valid()); err == nil {
				t.Fatal("unauthorized browser exchange accepted")
			}
		}
		for _, mutate := range []func(*authv1.ExchangeBrowserSessionRequest){
			func(r *authv1.ExchangeBrowserSessionRequest) { r.Context = nil },
			func(r *authv1.ExchangeBrowserSessionRequest) { r.Audience = "unknown" },
			func(r *authv1.ExchangeBrowserSessionRequest) { r.Context.Origin = nil },
			func(r *authv1.ExchangeBrowserSessionRequest) { r.Context.Origin = []string{"https://evil.example"} },
			func(r *authv1.ExchangeBrowserSessionRequest) { r.Context.Origin = []string{"null"} },
			func(r *authv1.ExchangeBrowserSessionRequest) {
				r.Context.Origin = []string{"https://app.example", "https://app.example"}
			},
			func(r *authv1.ExchangeBrowserSessionRequest) { r.Context.SecFetchSite = []string{"cross-site"} },
			func(r *authv1.ExchangeBrowserSessionRequest) {
				r.Context.SecFetchSite = []string{"same-origin", "same-origin"}
			},
			func(r *authv1.ExchangeBrowserSessionRequest) {
				r.Context.Cookie = []string{"__Host-mm-refresh=opaque-access"}
			},
			func(r *authv1.ExchangeBrowserSessionRequest) {
				r.Context.Cookie = []string{"__Host-mm-access=opaque-access", "__Host-mm-access=opaque-access"}
			},
			func(r *authv1.ExchangeBrowserSessionRequest) { r.Context.Cookie = []string{strings.Repeat("a", 8193)} },
			func(r *authv1.ExchangeBrowserSessionRequest) {
				r.Context.Cookie = []string{"__Host-mm-access=opaque-access\r\nInjected: yes"}
			},
		} {
			r := valid()
			mutate(r)
			if _, err := gateway.ExchangeBrowserSession(ctx, r); status.Code(err) != codes.Unauthenticated {
				t.Fatalf("invalid browser context: %v", err)
			}
		}
		if ops.calls.Load() != before {
			t.Fatal("untrusted browser input reached session service")
		}
		response, err := gateway.ExchangeBrowserSession(ctx, valid())
		if err != nil || response.GetAssertion() == "" || response.GetExpiresAtUnix() <= now.Unix() {
			t.Fatalf("valid browser exchange failed: %v", err)
		}
		if _, err := consumer.VerifyAssertion(ctx, &authv1.VerifyAssertionRequest{Assertion: response.GetAssertion()}); err != nil {
			t.Fatal(err)
		}
		ops.checks.Store(0)
	})
	exchanged, err := gateway.ExchangeSession(ctx, &authv1.ExchangeSessionRequest{Cookie: "unrelated=value; __Host-mm-access=opaque-access; __Host-mm-refresh=refresh", Audience: "user-service"})
	if err != nil {
		t.Fatal(err)
	}
	t.Run("audience and online revocation", func(t *testing.T) {
		req := &authv1.VerifyAssertionRequest{Assertion: exchanged.GetAssertion()}
		response, err := consumer.VerifyAssertion(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		if response.GetSessionId() != ops.record.ID.String() || string(response.GetSubjectId()) != string(ops.record.SubjectID[:]) || ops.checks.Load() != 1 {
			t.Fatal("wrong verified session")
		}
		if _, err := other.VerifyAssertion(ctx, req); status.Code(err) != codes.Unauthenticated {
			t.Fatalf("audience confusion: %v", err)
		}
		if ops.checks.Load() != 1 {
			t.Fatal("wrong audience reached online checker")
		}
		ops.revoked.Store(true)
		if _, err := consumer.VerifyAssertion(ctx, req); status.Code(err) != codes.Unauthenticated {
			t.Fatalf("revoked assertion accepted: %v", err)
		}
		if ops.checks.Load() != 2 {
			t.Fatal("revocation check skipped")
		}
		ops.revoked.Store(false)
	})
	t.Run("invalid issuer and expiry", func(t *testing.T) {
		for _, tc := range []struct {
			issuer string
			issued time.Time
		}{{"foreign", now}, {"auth.marketmesh", now.Add(-2 * time.Minute)}} {
			issuer, err := sessionassert.NewIssuer(priv, "test", tc.issuer, sessionassert.WithIssuerClock(func() time.Time { return tc.issued }))
			if err != nil {
				t.Fatal(err)
			}
			token, err := issuer.Issue(sessionassert.IssueParams{Audience: "user-service", Subject: base64.RawURLEncoding.EncodeToString(ops.record.SubjectID[:]), SessionID: ops.record.ID.String(), TTL: time.Minute, AuthTime: tc.issued, ACR: "password", AMR: []string{"pwd"}})
			if err != nil {
				t.Fatal(err)
			}
			before := ops.checks.Load()
			if _, err := consumer.VerifyAssertion(ctx, &authv1.VerifyAssertionRequest{Assertion: token}); status.Code(err) != codes.Unauthenticated {
				t.Fatalf("invalid assertion accepted: %v", err)
			}
			if ops.checks.Load() != before {
				t.Fatal("invalid assertion reached online checker")
			}
		}
	})
	t.Run("public JWKS only", func(t *testing.T) {
		response, err := gateway.GetSigningKeys(ctx, &authv1.GetSigningKeysRequest{})
		if err != nil {
			t.Fatal(err)
		}
		var jwks struct {
			Keys []map[string]any `json:"keys"`
		}
		if err = json.Unmarshal([]byte(response.GetJwksJson()), &jwks); err != nil {
			t.Fatal(err)
		}
		if len(jwks.Keys) != 1 || jwks.Keys[0]["x"] != base64.RawURLEncoding.EncodeToString(pub) || jwks.Keys[0]["kty"] != "OKP" {
			t.Fatal("invalid public JWKS")
		}
		for _, key := range jwks.Keys {
			for field := range key {
				switch field {
				case "kid", "kty", "crv", "alg", "use", "x", "verify_until":
				default:
					t.Fatalf("unexpected JWK field %q", field)
				}
			}
		}
		if strings.Contains(response.GetJwksJson(), base64.RawURLEncoding.EncodeToString(priv)) {
			t.Fatal("private material disclosed")
		}
	})
}

func certificateAuthority(t *testing.T) (*x509.Certificate, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, template, template, pub, priv)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, priv
}
func issueCertificate(t *testing.T, ca *x509.Certificate, caKey ed25519.PrivateKey, identity string, server bool) tls.Certificate {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: serial, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	if server {
		template.DNSNames = []string{"localhost"}
		template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	}
	if identity != "" {
		u, err := url.Parse(identity)
		if err != nil {
			t.Fatal(err)
		}
		template.URIs = []*url.URL{u}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, pub, caKey)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv}
}

type browserCredentials struct{ calls atomic.Int32 }

func (b *browserCredentials) Execute(context.Context, string, []byte) error {
	b.calls.Add(1)
	return nil
}

type browserVerifier struct{}

func (browserVerifier) Execute(context.Context, string, []byte) (credential.SubjectID, error) {
	return credential.SubjectID{1}, nil
}
