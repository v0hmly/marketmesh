//go:build integration

package app

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	authv1 "github.com/v0hmly/marketmesh/api/gen/go/auth/v1"
	"github.com/v0hmly/marketmesh/api/gen/go/auth/v1/authv1connect"
	serviceruntime "github.com/v0hmly/marketmesh/platform/runtime"
	"github.com/v0hmly/marketmesh/services/auth/internal/adapter/out/registrationwire"
	"github.com/v0hmly/marketmesh/services/auth/migrations"
)

func TestIntegrationRegistrationRuntimeCaptureRecoveryAndPrivacy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	envRequired := func(name string) string {
		t.Helper()
		v := os.Getenv(name)
		if v == "" {
			t.Fatalf("%s required; use auth:registration:integration", name)
		}
		return v
	}
	dsn, roDSN := envRequired("MARKETMESH_AUTH_POSTGRES_DSN"), envRequired("MARKETMESH_AUTH_POSTGRES_RO_DSN")
	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := db.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(migrations.CredentialsUp)
	exec(migrations.RegistrationOutboxUp)
	exec(`CREATE ROLE registration_runtime_rw LOGIN PASSWORD 'fixture-registration-rw'; CREATE ROLE registration_runtime_ro LOGIN PASSWORD 'fixture-registration-ro'; GRANT USAGE ON SCHEMA auth TO registration_runtime_rw,registration_runtime_ro; GRANT SELECT,INSERT,UPDATE ON auth.credentials,auth.registration_outbox TO registration_runtime_rw; GRANT SELECT ON auth.credentials TO registration_runtime_ro`)
	t.Cleanup(func() {
		if _, err := db.Exec(context.Background(), `DROP OWNED BY registration_runtime_rw,registration_runtime_ro; DROP ROLE registration_runtime_rw,registration_runtime_ro;`+migrations.RegistrationOutboxDown+migrations.CredentialsDown); err != nil {
			t.Error(err)
		}
	})
	replica, err := pgxpool.New(ctx, roDSN)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(replica.Close)
	var lsn string
	if err := db.QueryRow(ctx, "SELECT pg_current_wal_lsn()::text").Scan(&lsn); err != nil {
		t.Fatal(err)
	}
	awaitRegistration(t, ctx, 10*time.Second, func() bool {
		var replayed bool
		return replica.QueryRow(ctx, "SELECT pg_last_wal_replay_lsn()>=$1::pg_lsn", lsn).Scan(&replayed) == nil && replayed
	}, "replica schema and roles")
	natsURL := envRequired("NATS_TEST_URL")
	adminTLS := registrationFixtureTLS(t, envRequired("NATS_TEST_CA_FILE"), envRequired("NATS_TEST_ADMIN_CERT_FILE"), envRequired("NATS_TEST_ADMIN_KEY_FILE"), envRequired("NATS_TEST_SERVER_NAME"))
	var admin *nats.Conn
	awaitRegistration(t, ctx, 10*time.Second, func() bool {
		var err error
		admin, err = nats.Connect(natsURL, nats.Secure(adminTLS), nats.Timeout(300*time.Millisecond), nats.NoReconnect())
		return err == nil
	}, "NATS fixture")
	t.Cleanup(admin.Close)
	js, err := admin.JetStream()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := js.AddStream(&nats.StreamConfig{Name: "AUTH_REGISTRATION", Subjects: []string{"auth.account.registered.v1"}, Storage: nats.FileStorage, Duplicates: time.Minute}, nats.Context(ctx)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := js.DeleteStream("AUTH_REGISTRATION", nats.MaxWait(time.Second)); err != nil {
			t.Error(err)
		}
	})
	backend, err := url.Parse(natsURL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := newRegistrationBrokerProxy(t, backend.Host)
	env := requiredEnvironment()
	roleDSN := func(endpoint, name, password string) string {
		u, err := url.Parse(endpoint)
		if err != nil {
			t.Fatal(err)
		}
		u.User = url.UserPassword(name, password)
		return u.String()
	}
	for key, value := range map[string]string{
		"AUTH_REGISTRATION_EVENTS_ENABLED": "true", "AUTH_REGISTRATION_PUBLISH_ENABLED": "false",
		"AUTH_NATS_URL": "tls://" + proxy.address(), "AUTH_NATS_SERVER_NAME": envRequired("NATS_TEST_SERVER_NAME"), "AUTH_EVENTS_TRUST_DOMAIN": "marketmesh.test",
		"AUTH_NATS_TLS_CA_FILE": envRequired("NATS_TEST_CA_FILE"), "AUTH_NATS_TLS_CERT_FILE": envRequired("NATS_TEST_CERT_FILE"), "AUTH_NATS_TLS_KEY_FILE": envRequired("NATS_TEST_KEY_FILE"),
		"POSTGRES_RW_DSN": roleDSN(dsn, "registration_runtime_rw", "fixture-registration-rw"), "POSTGRES_RO_DSN": roleDSN(roDSN, "registration_runtime_ro", "fixture-registration-ro"),
		"HTTP_ADDRESS": "127.0.0.1:0", "POSTGRES_QUERY_TIMEOUT": "500ms", "HEALTH_CHECK_TIMEOUT": "500ms", "SHUTDOWN_TIMEOUT": "3s",
		"AUTH_EVENTS_CONNECT_TIMEOUT": "200ms", "AUTH_EVENTS_PUBLISH_TIMEOUT": "500ms", "AUTH_EVENTS_POLL_INTERVAL": "25ms", "AUTH_EVENTS_LEASE_DURATION": "5s", "AUTH_EVENTS_RETRY_INITIAL": "50ms", "AUTH_EVENTS_RETRY_MAX": "200ms",
		// Exercise the real password hasher with inexpensive test-only parameters.
		"ARGON2_MEMORY_KIB": "8192", "ARGON2_TIME": "1", "ARGON2_PARALLELISM": "1",
	} {
		env[key] = value
	}
	httpClient := &http.Client{Timeout: 3 * time.Second}
	t.Cleanup(httpClient.CloseIdleConnections)
	var logs registrationLogBuffer
	start := func() (string, func()) {
		runCtx, stop := context.WithCancel(ctx)
		listeners := make(chan net.Listener, 1)
		done := make(chan error, 1)
		snapshot := make(map[string]string, len(env))
		for k, v := range env {
			snapshot[k] = v
		}
		go func() {
			done <- run(runCtx, systemDependencies{env: serviceruntime.MapEnv(snapshot), stdout: &logs, stderr: &logs, listen: func(network, address string) (net.Listener, error) {
				l, err := net.Listen(network, address)
				if err == nil {
					listeners <- l
				}
				return l, err
			}})
		}()
		var listener net.Listener
		select {
		case listener = <-listeners:
		case err := <-done:
			stop()
			t.Fatalf("runtime startup failed: %v", err)
		case <-ctx.Done():
			stop()
			t.Fatal(ctx.Err())
		}
		var once sync.Once
		stopAndWait := func() {
			once.Do(func() {
				stop()
				select {
				case err := <-done:
					if err != nil {
						t.Error("runtime shutdown failed", err)
					}
				case <-time.After(5 * time.Second):
					t.Error("runtime shutdown timed out")
				}
				if c, err := net.DialTimeout("tcp", listener.Addr().String(), 100*time.Millisecond); err == nil {
					_ = c.Close()
					t.Error("runtime listener leaked")
				}
			})
		}
		t.Cleanup(stopAndWait)
		return "http://" + listener.Addr().String(), stopAndWait
	}
	ready := func(base string, want int) {
		t.Helper()
		awaitRegistration(t, ctx, 5*time.Second, func() bool {
			resp, err := httpClient.Get(base + "/readyz")
			if err != nil {
				return false
			}
			_ = resp.Body.Close()
			return resp.StatusCode == want
		}, "runtime readiness")
	}
	register := func(base, identifier string) {
		t.Helper()
		client := authv1connect.NewAuthServiceClient(httpClient, base)
		request := connect.NewRequest(&authv1.RegisterCredentialsRequest{Identifier: identifier, Password: []byte("fixture-private-password")})
		request.Header().Set("traceparent", "00-0123456789abcdef0123456789abcdef-0123456789abcdef-01")
		if _, err := client.RegisterCredentials(ctx, request); err != nil {
			t.Fatal("registration RPC failed", err)
		}
	}
	count := func(sql string) int {
		t.Helper()
		var n int
		if err := db.QueryRow(ctx, sql).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	// Capture-only mode does not lose facts while publication is administratively paused.
	base, stopCapture := start()
	ready(base, http.StatusNoContent)
	register(base, "private-capture@example.com")
	register(base, "private-capture@example.com")
	if count(`SELECT count(*) FROM auth.credentials`) != 1 || count(`SELECT count(*) FROM auth.registration_outbox WHERE published_at IS NULL AND attempts=0`) != 1 {
		t.Fatal("capture/duplicate invariant failed")
	}
	info, err := js.StreamInfo("AUTH_REGISTRATION", nats.Context(ctx))
	if err != nil || info.State.Msgs != 0 {
		t.Fatal("capture-only mode published", err)
	}
	stopCapture()
	// Publication starts while the network path is unavailable. Registration and
	// readiness must remain usable; only durable outbox work accumulates.
	env["AUTH_REGISTRATION_PUBLISH_ENABLED"] = "true"
	base, stopPublishing := start()
	ready(base, http.StatusNoContent)
	register(base, "private-outage@example.com")
	awaitRegistration(t, ctx, 5*time.Second, func() bool {
		return count(`SELECT count(*) FROM auth.registration_outbox WHERE published_at IS NULL AND attempts>0`) == 2
	}, "durable retry after broker outage")
	ready(base, http.StatusNoContent)
	if count(`SELECT count(*) FROM auth.credentials`) != 2 {
		t.Fatal("broker outage lost registration")
	}
	exec(`ALTER TABLE auth.registration_outbox RENAME TO registration_outbox_unavailable`)
	ready(base, http.StatusServiceUnavailable)
	exec(`ALTER TABLE auth.registration_outbox_unavailable RENAME TO registration_outbox`)
	ready(base, http.StatusNoContent)
	proxy.available.Store(true)
	awaitRegistration(t, ctx, 10*time.Second, func() bool {
		return count(`SELECT count(*) FROM auth.registration_outbox WHERE published_at IS NOT NULL`) == 2
	}, "broker recovery and acknowledged publication")
	info, err = js.StreamInfo("AUTH_REGISTRATION", nats.Context(ctx))
	if err != nil || info.State.Msgs != 2 {
		t.Fatal("wrong broker message count", err)
	}
	for sequence := uint64(1); sequence <= 2; sequence++ {
		message, err := js.GetMsg("AUTH_REGISTRATION", sequence, nats.Context(ctx))
		if err != nil {
			t.Fatal(err)
		}
		event, err := registrationwire.Unmarshal(message.Data)
		if err != nil {
			t.Fatal(err)
		}
		if message.Header.Get(nats.MsgIdHdr) != hex.EncodeToString(event.ID[:]) {
			t.Fatal("unstable broker deduplication ID")
		}
		var stored []byte
		if err := db.QueryRow(ctx, `SELECT payload FROM auth.registration_outbox WHERE event_id=$1`, event.ID[:]).Scan(&stored); err != nil || !bytes.Equal(stored, message.Data) {
			t.Fatal("published bytes differ from durable fact", err)
		}
		for _, secret := range []string{"private-capture@example.com", "private-outage@example.com", "fixture-private-password", "argon2id"} {
			if bytes.Contains(message.Data, []byte(secret)) {
				t.Fatal("private credential material in event")
			}
		}
	}
	register(base, "private-capture@example.com")
	if count(`SELECT count(*) FROM auth.registration_outbox`) != 2 {
		t.Fatal("duplicate registered a second fact")
	}
	stopPublishing()
	for _, secret := range []string{"private-capture@example.com", "private-outage@example.com", "fixture-private-password", "fixture-registration-rw", "fixture-registration-ro", "BEGIN PRIVATE KEY"} {
		if strings.Contains(logs.String(), secret) {
			t.Fatal("private runtime log")
		}
	}
}

func registrationFixtureTLS(t *testing.T, caFile, certFile, keyFile, serverName string) *tls.Config {
	t.Helper()
	cert, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(caFile)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(raw) {
		t.Fatal("invalid fixture CA")
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, Certificates: []tls.Certificate{cert}, ServerName: serverName}
}
func awaitRegistration(t *testing.T, ctx context.Context, timeout time.Duration, predicate func() bool, operation string) {
	t.Helper()
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	for {
		if predicate() {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-deadline.C:
			t.Fatal("timed out waiting for", operation)
		case <-ticker.C:
		}
	}
}

type registrationLogBuffer struct {
	mu     sync.Mutex
	buffer bytes.Buffer
}

func (b *registrationLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.Write(p)
}
func (b *registrationLogBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String()
}

type registrationBrokerProxy struct {
	listener  net.Listener
	available atomic.Bool
}

func newRegistrationBrokerProxy(t *testing.T, backend string) *registrationBrokerProxy {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	proxy := &registrationBrokerProxy{listener: listener}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			client, err := listener.Accept()
			if err != nil {
				return
			}
			if !proxy.available.Load() {
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
				copied := make(chan struct{}, 2)
				go func() { _, _ = io.Copy(server, client); copied <- struct{}{} }()
				go func() { _, _ = io.Copy(client, server); copied <- struct{}{} }()
				select {
				case <-copied:
				case <-ctx.Done():
				}
				_ = client.Close()
				_ = server.Close()
				select {
				case <-copied:
				case <-time.After(time.Second):
				}
			}()
		}
	}()
	t.Cleanup(func() { cancel(); _ = listener.Close(); wg.Wait() })
	return proxy
}
func (p *registrationBrokerProxy) address() string { return p.listener.Addr().String() }
