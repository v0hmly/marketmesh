//go:build integration

package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	authv1 "github.com/v0hmly/marketmesh/api/gen/go/auth/v1"
	userv1 "github.com/v0hmly/marketmesh/api/gen/go/user/v1"
	serviceruntime "github.com/v0hmly/marketmesh/platform/runtime"
	"github.com/v0hmly/marketmesh/platform/sessionassert"
	"github.com/v0hmly/marketmesh/platform/workloadid"
	"github.com/v0hmly/marketmesh/services/user/internal/adapter/in/internalgrpc"
	"github.com/v0hmly/marketmesh/services/user/migrations"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type runtimeAuthSession struct {
	subject  []byte
	authTime time.Time
}

type runtimeAuthFixture struct {
	authv1.UnimplementedAuthInternalServiceServer
	sessions map[string]runtimeAuthSession
	jwks     string
	verifier *sessionassert.Verifier
	checks   atomic.Int64
	revoked  atomic.Bool
}

func (a *runtimeAuthFixture) GetSigningKeys(context.Context, *authv1.GetSigningKeysRequest) (*authv1.GetSigningKeysResponse, error) {
	return &authv1.GetSigningKeysResponse{JwksJson: a.jwks}, nil
}
func (a *runtimeAuthFixture) VerifyAssertion(ctx context.Context, req *authv1.VerifyAssertionRequest) (*authv1.VerifyAssertionResponse, error) {
	a.checks.Add(1)
	claims, err := a.verifier.VerifyContext(ctx, req.GetAssertion())
	if err != nil || a.revoked.Load() {
		return nil, status.Error(codes.Unauthenticated, "session rejected")
	}
	raw, err := base64.RawURLEncoding.DecodeString(claims.Subject)
	if err != nil {
		return nil, status.Error(codes.Unauthenticated, "subject rejected")
	}
	session, found := a.sessions[claims.SessionID]
	if !found || !bytes.Equal(raw, session.subject) || !claims.AuthTime.Equal(session.authTime) {
		return nil, status.Error(codes.Unauthenticated, "session binding rejected")
	}
	return &authv1.VerifyAssertionResponse{SubjectId: raw, SessionId: claims.SessionID, ExpiresAtUnix: claims.ExpiresAt.Unix()}, nil
}

func TestIntegrationProfileRuntime(t *testing.T) {
	dsn := os.Getenv("MARKETMESH_USER_POSTGRES_DSN")
	if dsn == "" {
		t.Fatal("MARKETMESH_USER_POSTGRES_DSN required; use disposable integration fixture")
	}
	roDSN := os.Getenv("MARKETMESH_USER_POSTGRES_RO_DSN")
	if roDSN == "" {
		t.Fatal("MARKETMESH_USER_POSTGRES_RO_DSN required for recovery replica")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(migrations.ProfilesUp)
	exec(`CREATE ROLE runtime_user_rw LOGIN PASSWORD 'fixture-rw'; CREATE ROLE runtime_user_ro LOGIN PASSWORD 'fixture-ro'; GRANT USAGE ON SCHEMA users TO runtime_user_rw,runtime_user_ro; GRANT SELECT,UPDATE ON users.profiles TO runtime_user_rw; GRANT SELECT ON users.profiles TO runtime_user_ro`)
	defer func() {
		_, err := db.Exec(context.Background(), `DROP OWNED BY runtime_user_rw,runtime_user_ro; DROP ROLE runtime_user_rw,runtime_user_ro;`+migrations.ProfilesDown)
		if err != nil {
			t.Error(err)
		}
	}()
	a, b := make([]byte, 16), make([]byte, 16)
	a[0] = 1
	b[0] = 2
	exec(`INSERT INTO users.profiles(subject_id,display_name) VALUES($1,'private-alice'),($2,'private-bob')`, a, b)
	// Wait until schema, fixture roles and rows reach the real recovery replica.
	var lsn string
	if err := db.QueryRow(ctx, "SELECT pg_current_wal_lsn()::text").Scan(&lsn); err != nil {
		t.Fatal(err)
	}
	replica, err := pgxpool.New(ctx, roDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer replica.Close()
	for {
		var replayed bool
		if err := replica.QueryRow(ctx, "SELECT pg_last_wal_replay_lsn() >= $1::pg_lsn", lsn).Scan(&replayed); err != nil {
			t.Fatal(err)
		}
		if replayed {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
	pki := newProfilePKI(t)
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(pki.caPEM)
	authCert, _, _ := pki.leaf(t, "spiffe://marketmesh.test/test/auth", []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth})
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := sessionassert.NewIssuer(key, "runtime-key", "marketmesh-auth", sessionassert.WithMaxTTL(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	keys := sessionassert.NewStaticKeySet()
	if err := keys.Add("runtime-key", pub); err != nil {
		t.Fatal(err)
	}
	verifier, err := sessionassert.NewVerifier("marketmesh-auth", "user", keys, sessionassert.WithLeeway(0))
	if err != nil {
		t.Fatal(err)
	}
	jwks, err := json.Marshal(map[string]any{"keys": []any{map[string]any{"kty": "OKP", "crv": "Ed25519", "alg": "EdDSA", "use": "sig", "kid": "runtime-key", "x": base64.RawURLEncoding.EncodeToString(pub), "verify_until": time.Now().Add(time.Hour)}}})
	if err != nil {
		t.Fatal(err)
	}
	authTime := time.Now().UTC().Truncate(time.Second).Add(-time.Minute)
	sessions := map[string]runtimeAuthSession{
		"00000000000000000000000000000001": {subject: a, authTime: authTime},
		"00000000000000000000000000000002": {subject: b, authTime: authTime.Add(-time.Minute)},
	}
	auth := &runtimeAuthFixture{jwks: string(jwks), verifier: verifier, sessions: sessions}
	policy, err := workloadid.NewPolicy(map[workloadid.Identity][]string{{TrustDomain: "marketmesh.test", Environment: "test", Role: "user"}: {authv1.AuthInternalService_GetSigningKeys_FullMethodName, authv1.AuthInternalService_VerifyAssertion_FullMethodName}})
	if err != nil {
		t.Fatal(err)
	}
	authServer := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{authCert}, ClientCAs: roots, ClientAuth: tls.RequireAndVerifyClientCert})), grpc.UnaryInterceptor(workloadid.UnaryServerInterceptor(policy)))
	authv1.RegisterAuthInternalServiceServer(authServer, auth)
	authListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	authDone := make(chan error, 1)
	go func() { authDone <- authServer.Serve(authListener) }()
	defer func() { authServer.Stop(); _ = authListener.Close(); <-authDone }()
	certConfig := pki.config(t, "spiffe://marketmesh.test/test/user", []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth})
	env := profileEnvironment()
	roleDSN := func(endpoint, role, password string) string {
		u, err := url.Parse(endpoint)
		if err != nil {
			t.Fatal(err)
		}
		u.User = url.UserPassword(role, password)
		return u.String()
	}
	// Auth is a separate fixture database: User LOGIN roles have no CONNECT privilege.
	exec(`CREATE DATABASE auth`)
	defer func() {
		if _, err := db.Exec(context.Background(), `DROP DATABASE auth`); err != nil {
			t.Error(err)
		}
	}()
	exec(`REVOKE CONNECT ON DATABASE auth FROM PUBLIC`)
	for _, role := range []struct{ name, password string }{{"runtime_user_rw", "fixture-rw"}, {"runtime_user_ro", "fixture-ro"}} {
		u, err := url.Parse(roleDSN(dsn, role.name, role.password))
		if err != nil {
			t.Fatal(err)
		}
		u.Path = "/auth"
		forbidden, err := pgxpool.New(ctx, u.String())
		if err != nil {
			t.Fatal(err)
		}
		err = forbidden.Ping(ctx)
		forbidden.Close()
		var denied *pgconn.PgError
		if !errors.As(err, &denied) || denied.Code != "42501" {
			t.Fatal("User Auth CONNECT did not fail with insufficient privilege", err)
		}
	}
	for k, v := range map[string]string{"USER_TLS_CERT_FILE": certConfig.certificateFile, "USER_TLS_KEY_FILE": certConfig.keyFile, "USER_TLS_CLIENT_CA_FILE": certConfig.clientCAFile, "USER_AUTH_CA_FILE": certConfig.authCAFile, "USER_AUTH_SERVER_NAME": "localhost", "USER_AUTH_TARGET": authListener.Addr().String(), "POSTGRES_RW_DSN": roleDSN(dsn, "runtime_user_rw", "fixture-rw"), "POSTGRES_RO_DSN": roleDSN(roDSN, "runtime_user_ro", "fixture-ro"), "USER_GRPC_ADDRESS": "127.0.0.1:0", "HTTP_ADDRESS": "127.0.0.1:0", "USER_AUTH_TIMEOUT": "300ms", "SHUTDOWN_TIMEOUT": "3s"} {
		env[k] = v
	}
	// Listeners are transferred through a channel; no shared address variables race.
	listeners := make(chan net.Listener, 2)
	var logs bytes.Buffer
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() {
		done <- run(runCtx, systemDependencies{env: serviceruntime.MapEnv(env), stdout: &logs, stderr: &logs, listen: func(network, address string) (net.Listener, error) {
			l, err := net.Listen(network, address)
			if err == nil {
				listeners <- l
			}
			return l, err
		}})
	}()
	stopped := false
	defer func() {
		stop()
		if !stopped {
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Error("runtime cleanup timed out")
			}
		}
	}()
	take := func() net.Listener {
		t.Helper()
		select {
		case l := <-listeners:
			return l
		case err := <-done:
			stopped = true
			t.Fatalf("runtime startup failed: %v", err)
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
		return nil
	}
	userListener, httpListener := take(), take()
	clientFor := func(role string) *grpc.ClientConn {
		t.Helper()
		cert, _, _ := pki.leaf(t, "spiffe://marketmesh.test/test/"+role, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth})
		conn, err := grpc.NewClient(userListener.Addr().String(), grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, ServerName: "localhost", RootCAs: roots, Certificates: []tls.Certificate{cert}})))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		return conn
	}
	client := userv1.NewUserServiceClient(clientFor("gateway-out"))
	tokenFor := func(subject []byte, audience string) string {
		t.Helper()
		var sid string
		var session runtimeAuthSession
		for id, value := range sessions {
			if bytes.Equal(value.subject, subject) {
				sid = id
				session = value
				break
			}
		}
		if sid == "" {
			t.Fatal("missing fixture session")
		}
		token, err := issuer.Issue(sessionassert.IssueParams{Audience: audience, Subject: base64.RawURLEncoding.EncodeToString(subject), SessionID: sid, TTL: 30 * time.Second, AuthTime: session.authTime, ACR: "password", AMR: []string{"pwd"}, Scopes: []string{"user:profile:read", "user:profile:write"}})
		if err != nil {
			t.Fatal(err)
		}
		return token
	}
	token := tokenFor(a, "user")
	callCtx := metadata.NewOutgoingContext(ctx, metadata.Pairs(internalgrpc.AssertionMetadata, token))
	httpClient := &http.Client{Timeout: time.Second}
	defer httpClient.CloseIdleConnections()
	ready := func(want int) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			resp, err := httpClient.Get("http://" + httpListener.Addr().String() + "/readyz")
			if err == nil {
				_ = resp.Body.Close()
				if resp.StatusCode == want {
					return
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("readiness did not reach %d", want)
	}
	ready(http.StatusNoContent)
	var headers metadata.MD
	first, err := client.GetMe(callCtx, &userv1.GetMeRequest{}, grpc.Header(&headers))
	if err != nil || first.GetProfile().GetVersion() != 1 || !bytes.Equal(first.GetProfile().GetSubjectId(), a) {
		t.Fatal("initial profile failed", err)
	}
	if values := headers.Get("cache-control"); len(values) == 0 || values[0] != "no-store" {
		t.Fatal("cache policy missing")
	}
	if _, err := replica.Exec(ctx, `SELECT pg_wal_replay_pause()`); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = replica.Exec(context.Background(), `SELECT pg_wal_replay_resume()`) }()
	updated, err := client.UpdateMe(callCtx, &userv1.UpdateMeRequest{DisplayName: "private-updated", Bio: "private-bio", ExpectedVersion: 1})
	if err != nil || updated.GetProfile().GetVersion() != 2 {
		t.Fatal("update failed", err)
	}
	current, err := client.GetMe(callCtx, &userv1.GetMeRequest{})
	if err != nil || current.GetProfile().GetVersion() != 2 || current.GetProfile().GetBio() != "private-bio" {
		t.Fatal("read-after-write failed", err)
	}
	var replicaVersion int64
	if err := replica.QueryRow(ctx, `SELECT version FROM users.profiles WHERE subject_id=$1`, a).Scan(&replicaVersion); err != nil || replicaVersion != 1 {
		t.Fatal("replica did not stay stale", err)
	}
	if _, err := replica.Exec(ctx, `SELECT pg_wal_replay_resume()`); err != nil {
		t.Fatal(err)
	}
	if _, err := client.UpdateMe(callCtx, &userv1.UpdateMeRequest{ExpectedVersion: 1}); status.Code(err) != codes.Aborted {
		t.Fatal("stale CAS accepted", err)
	}
	otherCtx := metadata.NewOutgoingContext(ctx, metadata.Pairs(internalgrpc.AssertionMetadata, tokenFor(b, "user")))
	other, err := client.GetMe(otherCtx, &userv1.GetMeRequest{})
	if err != nil || other.GetProfile().GetDisplayName() != "private-bob" || other.GetProfile().GetVersion() != 1 {
		t.Fatal("subject isolation failed", err)
	}
	before := auth.checks.Load()
	invalidCtx := metadata.NewOutgoingContext(ctx, metadata.Pairs(internalgrpc.AssertionMetadata, tokenFor(a, "another-service")))
	if _, err := client.GetMe(invalidCtx, &userv1.GetMeRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatal("wrong audience accepted", err)
	}
	if auth.checks.Load() != before {
		t.Fatal("invalid local assertion reached session RPC")
	}
	unauthorized := userv1.NewUserServiceClient(clientFor("gateway-in"))
	if _, err := unauthorized.GetMe(callCtx, &userv1.GetMeRequest{}); status.Code(err) != codes.PermissionDenied {
		t.Fatal("invalid workload accepted", err)
	}
	if auth.checks.Load() != before {
		t.Fatal("invalid workload reached Auth")
	}
	boundElsewhere, err := issuer.Issue(sessionassert.IssueParams{Audience: "user", Subject: base64.RawURLEncoding.EncodeToString(b), SessionID: "00000000000000000000000000000001", TTL: 30 * time.Second, AuthTime: authTime, ACR: "password", AMR: []string{"pwd"}, Scopes: []string{"user:profile:read"}})
	if err != nil {
		t.Fatal(err)
	}
	boundCtx := metadata.NewOutgoingContext(ctx, metadata.Pairs(internalgrpc.AssertionMetadata, boundElsewhere))
	if _, err := client.GetMe(boundCtx, &userv1.GetMeRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatal("session subject binding ignored", err)
	}
	if auth.checks.Load() != before+1 {
		t.Fatal("binding check did not execute fresh RPC")
	}
	before = auth.checks.Load()
	auth.revoked.Store(true)
	if _, err := client.GetMe(callCtx, &userv1.GetMeRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatal("fresh Auth rejection ignored", err)
	}
	if auth.checks.Load() != before+1 {
		t.Fatal("fresh RPC did not execute")
	}
	auth.revoked.Store(false)
	exec(`ALTER TABLE users.profiles RENAME TO profiles_unavailable`)
	ready(http.StatusServiceUnavailable)
	exec(`ALTER TABLE users.profiles_unavailable RENAME TO profiles`)
	ready(http.StatusNoContent)
	authServer.Stop()
	ready(http.StatusServiceUnavailable)
	stop()
	select {
	case err := <-done:
		stopped = true
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Fatal("graceful shutdown", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown timed out")
	}
	for _, s := range []string{"private-alice", "private-bob", "private-updated", "private-bio", token, "fixture-rw", "fixture-ro"} {
		if strings.Contains(logs.String(), s) {
			t.Fatal("sensitive runtime log")
		}
	}
	for _, l := range []net.Listener{userListener, httpListener} {
		conn, err := net.DialTimeout("tcp", l.Addr().String(), 100*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			t.Fatal("listener leaked after cancellation")
		}
	}
}
