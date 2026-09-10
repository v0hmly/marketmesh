//go:build integration && registrationintegration

package app

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	authv1 "github.com/v0hmly/marketmesh/api/gen/go/auth/v1"
	"github.com/v0hmly/marketmesh/api/gen/go/auth/v1/authv1connect"
	userv1 "github.com/v0hmly/marketmesh/api/gen/go/user/v1"
	serviceruntime "github.com/v0hmly/marketmesh/platform/runtime"
	"github.com/v0hmly/marketmesh/platform/sessionassert"
	"github.com/v0hmly/marketmesh/platform/workloadid"
	"github.com/v0hmly/marketmesh/services/user/internal/adapter/in/internalgrpc"
	"github.com/v0hmly/marketmesh/services/user/internal/adapter/out/registrationwire"
	"github.com/v0hmly/marketmesh/services/user/migrations"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// Real Auth registration/capture/publisher binaries and real User runtime. Only
// the unchanged Auth signing-key/session-assertion boundary uses an explicit stub.
func TestIntegrationRegistrationDelivery(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	required := func(key string) string {
		t.Helper()
		value := os.Getenv(key)
		if value == "" {
			t.Fatalf("%s required; use disposable registration fixture", key)
		}
		return value
	}
	open := func(key string) *pgxpool.Pool {
		t.Helper()
		db, err := pgxpool.New(ctx, required(key))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(db.Close)
		return db
	}
	authDB, userDB := open("MARKETMESH_AUTH_POSTGRES_DSN"), open("MARKETMESH_USER_POSTGRES_DSN")
	sql := func(db *pgxpool.Pool, q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	migration := func(name string) string {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(required("AUTH_MIGRATIONS_DIR"), name))
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	authDown := migration("000003_registration_outbox.down.sql") + migration("000001_credentials.down.sql")
	sql(authDB, migration("000001_credentials.up.sql")+migration("000003_registration_outbox.up.sql"))
	sql(authDB, `CREATE ROLE delivery_auth_rw LOGIN PASSWORD 'fixture-auth-rw'; CREATE ROLE delivery_auth_ro LOGIN PASSWORD 'fixture-auth-ro'; GRANT USAGE ON SCHEMA auth TO delivery_auth_rw,delivery_auth_ro; GRANT SELECT,INSERT,UPDATE ON auth.credentials,auth.registration_outbox TO delivery_auth_rw; GRANT SELECT ON auth.credentials TO delivery_auth_ro; CREATE ROLE delivery_backfill LOGIN PASSWORD 'fixture-backfill'; GRANT USAGE ON SCHEMA auth TO delivery_backfill; GRANT SELECT(subject_id) ON auth.credentials TO delivery_backfill; GRANT SELECT,INSERT ON auth.registration_outbox TO delivery_backfill; GRANT UPDATE(published_at,next_attempt_at) ON auth.registration_outbox TO delivery_backfill`)
	sql(userDB, migrations.ProfilesUp+migrations.RegistrationInboxUp)
	sql(userDB, `CREATE ROLE registration_user_rw LOGIN PASSWORD 'fixture-rw'; CREATE ROLE registration_user_ro LOGIN PASSWORD 'fixture-ro'; GRANT USAGE ON SCHEMA users TO registration_user_rw,registration_user_ro; GRANT SELECT,INSERT,UPDATE ON users.profiles,users.registration_inbox TO registration_user_rw; GRANT SELECT ON users.profiles TO registration_user_ro`)
	t.Cleanup(func() {
		if _, err := authDB.Exec(context.Background(), `DROP OWNED BY delivery_auth_rw,delivery_auth_ro,delivery_backfill;DROP ROLE delivery_auth_rw,delivery_auth_ro,delivery_backfill;`+authDown); err != nil {
			t.Error(err)
		}
		if _, err := userDB.Exec(context.Background(), `DROP OWNED BY registration_user_rw,registration_user_ro;DROP ROLE registration_user_rw,registration_user_ro;`+migrations.RegistrationInboxDown+migrations.ProfilesDown); err != nil {
			t.Error(err)
		}
	})
	replica := open("MARKETMESH_USER_POSTGRES_RO_DSN")
	var lsn string
	if err := userDB.QueryRow(ctx, `SELECT pg_current_wal_lsn()::text`).Scan(&lsn); err != nil {
		t.Fatal(err)
	}
	awaitDelivery(t, ctx, func() bool {
		var yes bool
		return replica.QueryRow(ctx, `SELECT pg_last_wal_replay_lsn()>=$1::pg_lsn`, lsn).Scan(&yes) == nil && yes
	}, "User replica roles")
	tlsFor := func(cert, key string) *tls.Config {
		t.Helper()
		roots := x509.NewCertPool()
		raw, err := os.ReadFile(required("NATS_TEST_CA_FILE"))
		if err != nil || !roots.AppendCertsFromPEM(raw) {
			t.Fatal("NATS CA")
		}
		pair, err := tls.LoadX509KeyPair(required(cert), required(key))
		if err != nil {
			t.Fatal(err)
		}
		return &tls.Config{MinVersion: tls.VersionTLS13, ServerName: required("NATS_TEST_SERVER_NAME"), RootCAs: roots, Certificates: []tls.Certificate{pair}}
	}
	var admin *nats.Conn
	awaitDelivery(t, ctx, func() bool {
		var err error
		admin, err = nats.Connect(required("NATS_TEST_URL"), nats.Secure(tlsFor("NATS_TEST_ADMIN_CERT_FILE", "NATS_TEST_ADMIN_KEY_FILE")), nats.Timeout(time.Second))
		return err == nil
	}, "NATS ready")
	t.Cleanup(admin.Close)
	js, err := admin.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	_, err = js.AddStream(&nats.StreamConfig{Name: "AUTH_REGISTRATION", Subjects: []string{"auth.account.registered.v1"}, Storage: nats.FileStorage, Duplicates: time.Second}, nats.Context(ctx))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := js.DeleteStream("AUTH_REGISTRATION", nats.MaxWait(time.Second)); err != nil {
			t.Error(err)
		}
	})
	_, err = js.AddConsumer("AUTH_REGISTRATION", &nats.ConsumerConfig{Durable: "USER_PROFILES_V1", FilterSubject: "auth.account.registered.v1", AckPolicy: nats.AckExplicitPolicy, DeliverPolicy: nats.DeliverAllPolicy, MaxDeliver: -1, AckWait: 10 * time.Second, MaxAckPending: 16}, nats.Context(ctx))
	if err != nil {
		t.Fatal(err)
	}
	backend, err := url.Parse(required("NATS_TEST_URL"))
	if err != nil {
		t.Fatal(err)
	}
	proxy := newDeliveryProxy(t, backend.Host)
	httpClient := &http.Client{Timeout: time.Second}
	t.Cleanup(httpClient.CloseIdleConnections)
	authRoleDSN := func(raw, role, password string) string {
		u, e := url.Parse(raw)
		if e != nil {
			t.Fatal(e)
		}
		u.User = url.UserPassword(role, password)
		return u.String()
	}
	backfillDSN := authRoleDSN(required("MARKETMESH_AUTH_POSTGRES_DSN"), "delivery_backfill", "fixture-backfill")
	backfillDB, err := pgxpool.New(ctx, backfillDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(backfillDB.Close)
	for _, query := range []string{`SELECT identifier FROM auth.credentials LIMIT 0`, `SELECT password_digest FROM auth.credentials LIMIT 0`} {
		_, err := backfillDB.Exec(ctx, query)
		var denied *pgconn.PgError
		if !errors.As(err, &denied) || denied.Code != "42501" {
			t.Fatal("backfill can read credentials", err)
		}
	}
	authReplica := open("MARKETMESH_AUTH_POSTGRES_RO_DSN")
	if err := authDB.QueryRow(ctx, `SELECT pg_current_wal_lsn()::text`).Scan(&lsn); err != nil {
		t.Fatal(err)
	}
	awaitDelivery(t, ctx, func() bool {
		var yes bool
		return authReplica.QueryRow(ctx, `SELECT pg_last_wal_replay_lsn()>=$1::pg_lsn`, lsn).Scan(&yes) == nil && yes
	}, "Auth replica roles")
	var authLogs deliveryLog
	startAuth := func(publish bool) (string, func()) {
		t.Helper()
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		address := listener.Addr().String()
		_ = listener.Close()
		cmd := exec.Command("/usr/local/bin/auth")
		cmd.Env = []string{"PATH=" + os.Getenv("PATH")}
		env := map[string]string{
			"SERVICE_VERSION":                   "test",
			"ENVIRONMENT":                       "test",
			"SERVICE_INSTANCE_ID":               "registration-auth",
			"POSTGRES_RW_DSN":                   authRoleDSN(required("MARKETMESH_AUTH_POSTGRES_DSN"), "delivery_auth_rw", "fixture-auth-rw"),
			"POSTGRES_RO_DSN":                   authRoleDSN(required("MARKETMESH_AUTH_POSTGRES_RO_DSN"), "delivery_auth_ro", "fixture-auth-ro"),
			"HTTP_ADDRESS":                      address,
			"SHUTDOWN_TIMEOUT":                  "3s",
			"AUTH_REGISTRATION_EVENTS_ENABLED":  "true",
			"AUTH_REGISTRATION_PUBLISH_ENABLED": "false",
			"AUTH_NATS_URL":                     required("NATS_TEST_URL"),
			"AUTH_NATS_SERVER_NAME":             required("NATS_TEST_SERVER_NAME"),
			"AUTH_NATS_TLS_CA_FILE":             required("NATS_TEST_CA_FILE"),
			"AUTH_NATS_TLS_CERT_FILE":           required("NATS_TEST_CERT_FILE"),
			"AUTH_NATS_TLS_KEY_FILE":            required("NATS_TEST_KEY_FILE"),
			"AUTH_EVENTS_TRUST_DOMAIN":          "marketmesh.test",
			"AUTH_EVENTS_CONNECT_TIMEOUT":       "200ms",
			"POSTGRES_QUERY_TIMEOUT":            "500ms",
			"AUTH_EVENTS_POLL_INTERVAL":         "25ms",
			"AUTH_EVENTS_LEASE_DURATION":        "5s",
			"AUTH_EVENTS_PUBLISH_TIMEOUT":       "500ms",
			"AUTH_EVENTS_RETRY_INITIAL":         "50ms",
			"AUTH_EVENTS_RETRY_MAX":             "200ms",
			"ARGON2_MEMORY_KIB":                 "8192",
			"ARGON2_TIME":                       "1",
			"ARGON2_PARALLELISM":                "1",
		}
		if publish {
			env["AUTH_REGISTRATION_PUBLISH_ENABLED"] = "true"
		}
		for k, v := range env {
			cmd.Env = append(cmd.Env, k+"="+v)
		}
		cmd.Stdout = &authLogs
		cmd.Stderr = &authLogs
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		var once sync.Once
		stop := func() {
			once.Do(func() {
				_ = cmd.Process.Signal(syscall.SIGTERM)
				select {
				case err := <-done:
					if err != nil {
						t.Error("Auth shutdown", err, authLogs.String())
					}
				case <-time.After(5 * time.Second):
					_ = cmd.Process.Kill()
					<-done
					t.Error("Auth shutdown timeout")
				}
			})
		}
		t.Cleanup(stop)
		base := "http://" + address
		awaitDelivery(t, ctx, func() bool {
			response, err := httpClient.Get(base + "/readyz")
			if err != nil {
				return false
			}
			defer response.Body.Close()
			return response.StatusCode == http.StatusNoContent
		}, "Auth binary ready")
		return base, stop
	}
	captureBase, stopCapture := startAuth(false)
	register := func(base, identifier string) []byte {
		t.Helper()
		client := authv1connect.NewAuthServiceClient(httpClient, base)
		_, err := client.RegisterCredentials(ctx, connect.NewRequest(&authv1.RegisterCredentialsRequest{Identifier: identifier, Password: []byte("fixture-private-password")}))
		if err != nil {
			t.Fatal(err)
		}
		var subject []byte
		if err := authDB.QueryRow(ctx, `SELECT subject_id FROM auth.credentials WHERE identifier=$1`, identifier).Scan(&subject); err != nil {
			t.Fatal(err)
		}
		return subject
	}
	subject := register(captureBase, "runtime-registration@example.invalid")
	var payload, eventID []byte
	if err := authDB.QueryRow(ctx, `SELECT event_id,payload FROM auth.registration_outbox WHERE subject_id=$1`, subject).Scan(&eventID, &payload); err != nil {
		t.Fatal(err)
	}
	stopCapture()
	// Explicit fixture for the existing session assertion RPC; registration above is real.
	pki := newProfilePKI(t)
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(pki.caPEM)
	authCert, _, _ := pki.leaf(t, "spiffe://marketmesh.test/test/auth", []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth})
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := sessionassert.NewIssuer(key, "registration-key", "marketmesh-auth", sessionassert.WithMaxTTL(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	keys := sessionassert.NewStaticKeySet()
	if err := keys.Add("registration-key", pub); err != nil {
		t.Fatal(err)
	}
	verifier, err := sessionassert.NewVerifier("marketmesh-auth", "user", keys, sessionassert.WithLeeway(0))
	if err != nil {
		t.Fatal(err)
	}
	jwks, _ := json.Marshal(map[string]any{"keys": []any{map[string]any{"kty": "OKP", "crv": "Ed25519", "alg": "EdDSA", "use": "sig", "kid": "registration-key", "x": base64.RawURLEncoding.EncodeToString(pub), "verify_until": time.Now().Add(time.Hour)}}})
	sid := "00000000000000000000000000000001"
	authTime := time.Now().UTC().Truncate(time.Second).Add(-time.Minute)
	stub := &runtimeAuthFixture{jwks: string(jwks), verifier: verifier, sessions: map[string]runtimeAuthSession{sid: {subject: subject, authTime: authTime}}}
	policy, err := workloadid.NewPolicy(map[workloadid.Identity][]string{{TrustDomain: "marketmesh.test", Environment: "test", Role: "user"}: {authv1.AuthInternalService_GetSigningKeys_FullMethodName, authv1.AuthInternalService_VerifyAssertion_FullMethodName}})
	if err != nil {
		t.Fatal(err)
	}
	authServer := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{authCert}, ClientCAs: roots, ClientAuth: tls.RequireAndVerifyClientCert})), grpc.UnaryInterceptor(workloadid.UnaryServerInterceptor(policy)))
	authv1.RegisterAuthInternalServiceServer(authServer, stub)
	authListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = authServer.Serve(authListener) }()
	t.Cleanup(authServer.Stop)
	certConfig := pki.config(t, "spiffe://marketmesh.test/test/user", []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth})
	roleDSN := func(raw, role, password string) string {
		u, err := url.Parse(raw)
		if err != nil {
			t.Fatal(err)
		}
		u.User = url.UserPassword(role, password)
		return u.String()
	}
	env := profileEnvironment()
	for k, v := range map[string]string{
		"USER_TLS_CERT_FILE":                certConfig.certificateFile,
		"USER_TLS_KEY_FILE":                 certConfig.keyFile,
		"USER_TLS_CLIENT_CA_FILE":           certConfig.clientCAFile,
		"USER_AUTH_CA_FILE":                 certConfig.authCAFile,
		"USER_AUTH_SERVER_NAME":             "localhost",
		"USER_AUTH_TARGET":                  authListener.Addr().String(),
		"POSTGRES_RW_DSN":                   roleDSN(required("MARKETMESH_USER_POSTGRES_DSN"), "registration_user_rw", "fixture-rw"),
		"POSTGRES_RO_DSN":                   roleDSN(required("MARKETMESH_USER_POSTGRES_RO_DSN"), "registration_user_ro", "fixture-ro"),
		"USER_GRPC_ADDRESS":                 "127.0.0.1:0",
		"HTTP_ADDRESS":                      "127.0.0.1:0",
		"POSTGRES_QUERY_TIMEOUT":            "500ms",
		"USER_AUTH_TIMEOUT":                 "300ms",
		"SHUTDOWN_TIMEOUT":                  "3s",
		"USER_REGISTRATION_CONSUME_ENABLED": "true",
		"USER_NATS_URL":                     "tls://" + proxy.listener.Addr().String(),
		"USER_NATS_SERVER_NAME":             required("NATS_TEST_SERVER_NAME"),
		"USER_NATS_TLS_CA_FILE":             required("NATS_TEST_CA_FILE"),
		"USER_NATS_TLS_CERT_FILE":           required("NATS_TEST_USER_CERT_FILE"),
		"USER_NATS_TLS_KEY_FILE":            required("NATS_TEST_USER_KEY_FILE"),
		"USER_EVENTS_CONNECT_TIMEOUT":       "200ms",
		"USER_EVENTS_OPERATION_TIMEOUT":     "1s",
		"USER_EVENTS_FETCH_TIMEOUT":         "100ms",
		"USER_EVENTS_RETRY_DELAY":           "1s",
	} {
		env[k] = v
	}
	var userLogs deliveryLog
	var userHTTP string
	startUser := func() (userv1.UserServiceClient, func()) {
		runCtx, stop := context.WithCancel(ctx)
		done := make(chan error, 1)
		listeners := make(chan net.Listener, 2)
		go func() {
			done <- run(runCtx, systemDependencies{env: serviceruntime.MapEnv(env), stdout: &userLogs, stderr: &userLogs, listen: func(network, address string) (net.Listener, error) {
				l, e := net.Listen(network, address)
				if e == nil {
					listeners <- l
				}
				return l, e
			}})
		}()
		var addresses []string
		var once sync.Once
		shutdown := func() {
			once.Do(func() {
				stop()
				select {
				case err := <-done:
					if err != nil {
						t.Error("User shutdown", err, userLogs.String())
					}
				case <-time.After(5 * time.Second):
					t.Error("User shutdown timeout")
				}
				for _, address := range addresses {
					if conn, e := net.DialTimeout("tcp", address, 100*time.Millisecond); e == nil {
						_ = conn.Close()
						t.Error("User listener leaked")
					}
				}
			})
		}
		t.Cleanup(shutdown)
		take := func() net.Listener {
			select {
			case l := <-listeners:
				return l
			case err := <-done:
				t.Fatalf("User startup: %v %s", err, userLogs.String())
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			return nil
		}
		userListener, httpListener := take(), take()
		addresses = []string{userListener.Addr().String(), httpListener.Addr().String()}
		userHTTP = "http://" + httpListener.Addr().String()
		awaitDelivery(t, ctx, func() bool {
			resp, err := httpClient.Get("http://" + httpListener.Addr().String() + "/readyz")
			if err != nil {
				return false
			}
			defer resp.Body.Close()
			return resp.StatusCode == http.StatusNoContent
		}, "User ready")
		cert, _, _ := pki.leaf(t, "spiffe://marketmesh.test/test/gateway-out", []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth})
		conn, err := grpc.NewClient(userListener.Addr().String(), grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, ServerName: "localhost", RootCAs: roots, Certificates: []tls.Certificate{cert}})))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		return userv1.NewUserServiceClient(conn), shutdown
	}
	callContext := func() context.Context {
		token, err := issuer.Issue(sessionassert.IssueParams{Audience: "user", Subject: base64.RawURLEncoding.EncodeToString(subject), SessionID: sid, TTL: 30 * time.Second, AuthTime: authTime, ACR: "password", AMR: []string{"pwd"}, Scopes: []string{"user:profile:read", "user:profile:write"}})
		if err != nil {
			t.Fatal(err)
		}
		return metadata.NewOutgoingContext(ctx, metadata.Pairs(internalgrpc.AssertionMetadata, token))
	}
	client, stopUser := startUser()
	_, err = client.GetMe(callContext(), &userv1.GetMeRequest{})
	if status.Code(err) != codes.NotFound {
		t.Fatal("profile readiness", err)
	}
	reason := ""
	for _, detail := range status.Convert(err).Details() {
		if d, ok := detail.(*errdetails.ErrorInfo); ok {
			reason = d.Reason
			if d.Domain != "marketmesh.user" || len(d.Metadata) != 0 {
				t.Error("unsafe profile detail")
			}
		}
	}
	if reason != "PROFILE_NOT_READY" {
		t.Error("missing profile reason", reason)
	}
	stub.revoked.Store(true)
	if _, err := client.GetMe(callContext(), &userv1.GetMeRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatal("invalid session", err)
	}
	stub.revoked.Store(false)
	// Consumer's broker link is unavailable; Auth publisher must still retain delivery.
	publisherBase, stopPublisher := startAuth(true)
	awaitDelivery(t, ctx, func() bool {
		info, e := js.ConsumerInfo("AUTH_REGISTRATION", "USER_PROFILES_V1")
		return e == nil && info.NumPending == 1 && info.AckFloor.Stream == 0
	}, "broker-outage backlog")
	// Database failure must leave the pending message unacknowledged.
	sql(userDB, `REVOKE INSERT ON users.registration_inbox FROM registration_user_rw`)
	awaitDelivery(t, ctx, func() bool {
		resp, e := httpClient.Get(userHTTP + "/readyz")
		if e != nil {
			return false
		}
		defer resp.Body.Close()
		return resp.StatusCode == http.StatusServiceUnavailable
	}, "missing inbox INSERT readiness")
	proxy.available.Store(true)
	awaitDelivery(t, ctx, func() bool {
		info, e := js.ConsumerInfo("AUTH_REGISTRATION", "USER_PROFILES_V1")
		return e == nil && info.Delivered.Consumer > 0 && info.NumRedelivered > 0 && info.AckFloor.Stream == 0
	}, "unacked database failure")
	var profileCount int
	if err := userDB.QueryRow(ctx, `SELECT count(*) FROM users.profiles`).Scan(&profileCount); err != nil || profileCount != 0 {
		t.Fatal("partial profile", err)
	}
	sql(userDB, `GRANT INSERT ON users.registration_inbox TO registration_user_rw`)
	awaitDelivery(t, ctx, func() bool {
		resp, e := httpClient.Get(userHTTP + "/readyz")
		if e != nil {
			return false
		}
		defer resp.Body.Close()
		return resp.StatusCode == http.StatusNoContent
	}, "restored inbox INSERT readiness")
	awaitDelivery(t, ctx, func() bool {
		res, e := client.GetMe(callContext(), &userv1.GetMeRequest{})
		return e == nil && res.GetProfile().GetVersion() == 1 && bytes.Equal(res.GetProfile().GetSubjectId(), subject)
	}, "real registration profile")
	updated, err := client.UpdateMe(callContext(), &userv1.UpdateMeRequest{DisplayName: "preserve-name", Bio: "preserve-bio", ExpectedVersion: 1})
	if err != nil || updated.GetProfile().GetVersion() != 2 {
		t.Fatal("update", err)
	}
	publish := func(data []byte) {
		t.Helper()
		if _, err := js.Publish("auth.account.registered.v1", data, nats.Context(ctx)); err != nil {
			t.Fatal(err)
		}
	}
	publish(payload)
	event, err := registrationwire.Unmarshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	event.SubjectID[0] ^= 0x7f
	changed, err := registrationwire.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	awaitDelivery(t, ctx, func() bool {
		info, e := js.ConsumerInfo("AUTH_REGISTRATION", "USER_PROFILES_V1")
		return e == nil && info.NumPending == 0 && info.NumAckPending == 0
	}, "duplicate/conflict settled")
	current, err := client.GetMe(callContext(), &userv1.GetMeRequest{})
	if err != nil || current.GetProfile().GetVersion() != 2 || current.GetProfile().GetDisplayName() != "preserve-name" || current.GetProfile().GetBio() != "preserve-bio" {
		t.Fatal("replay overwrote profile", err)
	}
	if err := userDB.QueryRow(ctx, `SELECT count(*) FROM users.profiles`).Scan(&profileCount); err != nil || profileCount != 1 {
		t.Fatal("conflicting event applied", err)
	}
	stopUser()
	client, stopUser = startUser()
	if res, err := client.GetMe(callContext(), &userv1.GetMeRequest{}); err != nil || res.GetProfile().GetVersion() != 2 {
		t.Fatal("restart", err)
	}
	// Backfill operates on fixture-only Auth records, never accesses User's database.
	historical := make([]byte, 16)
	historical[0] = 0xee
	sql(authDB, `INSERT INTO auth.credentials(subject_id,identifier,password_digest)VALUES($1,'historical@example.invalid','fixture')`, historical)
	backfill := func(args ...string) map[string]any {
		t.Helper()
		cmd := exec.CommandContext(ctx, "/usr/local/bin/auth-registration-backfill", args...)
		cmd.Env = []string{"MARKETMESH_AUTH_POSTGRES_DSN=" + backfillDSN}
		out, err := cmd.Output()
		if err != nil {
			t.Fatal("backfill CLI", err)
		}
		var result map[string]any
		if json.Unmarshal(out, &result) != nil {
			t.Fatal("backfill JSON")
		}
		return result
	}
	dry := backfill("--limit=100")
	if dry["Missing"] != float64(1) || dry["Inserted"] != float64(0) {
		t.Fatal("dry run", dry)
	}
	var missing int
	if err := authDB.QueryRow(ctx, `SELECT count(*) FROM auth.registration_outbox WHERE subject_id=$1`, historical).Scan(&missing); err != nil || missing != 0 {
		t.Fatal("dry run wrote", err)
	}
	applied := backfill("--limit=100", "--apply")
	if applied["Inserted"] != float64(1) {
		t.Fatal("apply", applied)
	}
	awaitDelivery(t, ctx, func() bool {
		var count int
		return userDB.QueryRow(ctx, `SELECT count(*) FROM users.profiles WHERE subject_id=$1`, historical).Scan(&count) == nil && count == 1
	}, "backfill profile")
	awaitDelivery(t, ctx, func() bool {
		var count int
		return authDB.QueryRow(ctx, `SELECT count(*) FROM auth.registration_outbox WHERE published_at IS NULL`).Scan(&count) == nil && count == 0
	}, "publisher drained")
	stopPublisher()
	replay := backfill("--limit=100", "--apply", "--replay-published")
	if replay["Requeued"] != float64(2) {
		t.Fatal("replay", replay)
	}
	var sameID, samePayload []byte
	if err := authDB.QueryRow(ctx, `SELECT event_id,payload FROM auth.registration_outbox WHERE subject_id=$1`, subject).Scan(&sameID, &samePayload); err != nil || !bytes.Equal(sameID, eventID) || !bytes.Equal(samePayload, payload) {
		t.Fatal("replay identity changed", err)
	}
	// Real publisher uses the same message ID. Wait out the deliberately short fixture dedup window.
	time.Sleep(1100 * time.Millisecond)
	_, stopPublisher = startAuth(true)
	awaitDelivery(t, ctx, func() bool {
		var count int
		return authDB.QueryRow(ctx, `SELECT count(*) FROM auth.registration_outbox WHERE published_at IS NULL`).Scan(&count) == nil && count == 0
	}, "replay publication")
	awaitDelivery(t, ctx, func() bool {
		info, e := js.ConsumerInfo("AUTH_REGISTRATION", "USER_PROFILES_V1")
		return e == nil && info.NumPending == 0 && info.NumAckPending == 0
	}, "replay drained")
	res, err := client.GetMe(callContext(), &userv1.GetMeRequest{})
	if err != nil || res.GetProfile().GetVersion() != 2 || res.GetProfile().GetBio() != "preserve-bio" {
		t.Fatal("backfill replay changed profile", err)
	}
	_ = publisherBase
	// Conflicting reuse stays unacknowledged for operator investigation.
	publish(changed)
	awaitDelivery(t, ctx, func() bool {
		info, e := js.ConsumerInfo("AUTH_REGISTRATION", "USER_PROFILES_V1")
		return e == nil && info.NumPending == 0 && info.NumAckPending == 1 && info.NumRedelivered > 0
	}, "conflict not acknowledged")
	if err := userDB.QueryRow(ctx, `SELECT count(*) FROM users.profiles`).Scan(&profileCount); err != nil || profileCount != 2 {
		t.Fatal("conflicting event created profile", err)
	}

	// Explicitly prove the consumer certificate cannot manage resources or publish events.
	var denied atomic.Int64
	restricted, err := nats.Connect(required("NATS_TEST_URL"), nats.Secure(tlsFor("NATS_TEST_USER_CERT_FILE", "NATS_TEST_USER_KEY_FILE")), nats.Timeout(time.Second), nats.ErrorHandler(func(*nats.Conn, *nats.Subscription, error) { denied.Add(1) }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restricted.Close)
	for _, subject := range []string{"auth.account.registered.v1", "$JS.API.CONSUMER.CREATE.AUTH_REGISTRATION.BAD", "$JS.API.CONSUMER.DELETE.AUTH_REGISTRATION.USER_PROFILES_V1", "$JS.ACK.OTHER.USER_PROFILES_V1.1.1.1.1.0"} {
		if err := restricted.Publish(subject, []byte("{}")); err != nil {
			t.Fatal(err)
		}
	}
	_ = restricted.FlushTimeout(time.Second)
	awaitDelivery(t, ctx, func() bool { return denied.Load() >= 4 }, "scoped consumer ACL")
	stopPublisher()
	stopUser()
	for _, secret := range []string{"fixture-private-password", "runtime-registration@example.invalid", "historical@example.invalid", "preserve-name", "preserve-bio", hex.EncodeToString(subject), required("MARKETMESH_AUTH_POSTGRES_DSN"), required("MARKETMESH_USER_POSTGRES_DSN")} {
		if strings.Contains(authLogs.String()+userLogs.String(), secret) {
			t.Fatal("sensitive data in logs")
		}
	}
}

func awaitDelivery(t *testing.T, ctx context.Context, predicate func() bool, name string) {
	t.Helper()
	deadline := time.NewTimer(12 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if predicate() {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-deadline.C:
			t.Fatal("timed out:", name)
		case <-ticker.C:
		}
	}
}

type deliveryLog struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *deliveryLog) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}
func (b *deliveryLog) String() string { b.mu.Lock(); defer b.mu.Unlock(); return b.b.String() }

type deliveryProxy struct {
	listener  net.Listener
	available atomic.Bool
}

func newDeliveryProxy(t *testing.T, backend string) *deliveryProxy {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &deliveryProxy{listener: l}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			client, err := l.Accept()
			if err != nil {
				return
			}
			if !p.available.Load() {
				_ = client.Close()
				continue
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer client.Close()
				server, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, "tcp", backend)
				if err != nil {
					return
				}
				defer server.Close()
				done := make(chan struct{}, 2)
				go func() { _, _ = io.Copy(server, client); done <- struct{}{} }()
				go func() { _, _ = io.Copy(client, server); done <- struct{}{} }()
				select {
				case <-done:
				case <-ctx.Done():
				}
				_ = client.Close()
				_ = server.Close()
				select {
				case <-done:
				case <-time.After(time.Second):
				}
			}()
		}
	}()
	t.Cleanup(func() { cancel(); _ = l.Close(); wg.Wait() })
	return p
}
