//go:build integration

package jetstream_test

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"net"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats.go"
	natsjs "github.com/nats-io/nats.go/jetstream"
	adapter "github.com/v0hmly/marketmesh/services/auth/internal/adapter/out/jetstream"
	"github.com/v0hmly/marketmesh/services/auth/internal/adapter/out/postgresoutbox"
	"github.com/v0hmly/marketmesh/services/auth/internal/adapter/out/registrationwire"
	"github.com/v0hmly/marketmesh/services/auth/internal/application/publishregistration"
	"github.com/v0hmly/marketmesh/services/auth/internal/domain/credential"
	"github.com/v0hmly/marketmesh/services/auth/internal/domain/registrationevent"
	"github.com/v0hmly/marketmesh/services/auth/migrations"
)

func integrationEnv(t *testing.T, key string) string {
	t.Helper()
	v := os.Getenv(key)
	if v == "" {
		t.Fatalf("%s required", key)
	}
	return v
}
func integrationTLS(t *testing.T, admin bool) *tls.Config {
	t.Helper()
	ca, err := os.ReadFile(integrationEnv(t, "NATS_TEST_CA_FILE"))
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		t.Fatal("invalid fixture CA")
	}
	certName, keyName := "NATS_TEST_CERT_FILE", "NATS_TEST_KEY_FILE"
	if admin {
		certName, keyName = "NATS_TEST_ADMIN_CERT_FILE", "NATS_TEST_ADMIN_KEY_FILE"
	}
	cert, err := tls.LoadX509KeyPair(integrationEnv(t, certName), integrationEnv(t, keyName))
	if err != nil {
		t.Fatal(err)
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, Certificates: []tls.Certificate{cert}, ServerName: integrationEnv(t, "NATS_TEST_SERVER_NAME")}
}
func integrationRecord(t *testing.T, marker byte) publishregistration.Record {
	t.Helper()
	id := [16]byte{marker}
	subject, err := credential.NewSubjectID(id[:])
	if err != nil {
		t.Fatal(err)
	}
	event := registrationevent.Event{ID: id, SubjectID: subject, OccurredAt: time.Now().UTC().Truncate(time.Microsecond)}
	raw, err := registrationwire.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	return publishregistration.Record{ID: id, Payload: raw, OccurredAt: event.OccurredAt}
}
func TestIntegrationJetStreamDeliveryLeaseRecoveryAndIsolation(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	broker := integrationEnv(t, "NATS_TEST_URL")
	adminTLS := integrationTLS(t, true)
	publisherTLS := integrationTLS(t, false)
	admin, err := nats.Connect(broker, nats.Secure(adminTLS), nats.NoReconnect(), nats.Timeout(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	js, err := natsjs.New(admin)
	if err != nil {
		t.Fatal(err)
	}
	stream, err := js.CreateStream(ctx, natsjs.StreamConfig{Name: adapter.Stream, Subjects: []string{adapter.Subject}, Storage: natsjs.FileStorage, Duplicates: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := js.DeleteStream(context.Background(), adapter.Stream); err != nil {
			t.Error(err)
		}
	}()
	cfg := adapter.Config{URL: broker, TLS: publisherTLS, ConnectTimeout: time.Second, PublishTimeout: 2 * time.Second, ValidatePayload: registrationwire.ValidatePayload}
	publisher, err := adapter.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = publisher.Close() }()
	db, err := pgxpool.New(ctx, integrationEnv(t, "MARKETMESH_AUTH_POSTGRES_DSN"))
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
	exec(migrations.CredentialsUp)
	exec(migrations.RegistrationOutboxUp)
	defer func() {
		if _, err := db.Exec(context.Background(), migrations.RegistrationOutboxDown+migrations.CredentialsDown); err != nil {
			t.Error(err)
		}
	}()
	insert := func(r publishregistration.Record) {
		t.Helper()
		exec(`INSERT INTO auth.registration_outbox(event_id,subject_id,occurred_at,payload)VALUES($1,$1,$2,$3)`, r.ID[:], r.OccurredAt, r.Payload)
	}
	store, _ := postgresoutbox.New(db)
	record := integrationRecord(t, 1)
	insert(record)
	claimed, found, err := store.Claim(ctx, 5*time.Second)
	if err != nil || !found {
		t.Fatal("claim", err)
	}
	if err := publisher.Publish(ctx, claimed); err != nil {
		t.Fatal("valid PubAck", err)
	}
	// The process dies after ACK but before SQL completion. Its lease must recover,
	// and the stable event ID deduplicates the second publish at the broker.
	exec(`UPDATE auth.registration_outbox SET lease_until=clock_timestamp()-interval '1 second' WHERE event_id=$1`, record.ID[:])
	taken, found, err := store.Claim(ctx, 5*time.Second)
	if err != nil || !found || taken.LeaseToken == claimed.LeaseToken {
		t.Fatal("lease takeover", err)
	}
	if ok, err := store.MarkPublished(ctx, claimed); err != nil || ok {
		t.Fatal("old owner completed", err)
	}
	if err := publisher.Publish(ctx, taken); err != nil {
		t.Fatal("duplicate PubAck", err)
	}
	if ok, err := store.MarkPublished(ctx, taken); err != nil || !ok {
		t.Fatal("completion after duplicate ACK", err)
	}
	info, err := stream.Info(ctx)
	if err != nil || info.State.Msgs != 1 {
		t.Fatal("stable ID did not deduplicate", err)
	}
	saved, err := stream.GetMsg(ctx, 1)
	if err != nil || !bytes.Equal(saved.Data, record.Payload) || saved.Header.Get(natsjs.MsgIDHeader) != hex.EncodeToString(record.ID[:]) {
		t.Fatal("wire payload/ID changed", err)
	}
	// Broker unavailable: the background worker retries durable state without a mark.
	second := integrationRecord(t, 2)
	insert(second)
	unavailable := cfg
	u, err := url.Parse(broker)
	if err != nil {
		t.Fatal(err)
	}
	u.Host = net.JoinHostPort(u.Hostname(), "1")
	unavailable.URL = u.String()
	unavailable.ConnectTimeout = 100 * time.Millisecond
	unavailable.PublishTimeout = 200 * time.Millisecond
	offline, err := adapter.New(unavailable)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = offline.Close() }()
	observed := make(chan publishregistration.Outcome, 16)
	worker, err := publishregistration.New(store, offline, integrationObserver{outcomes: observed}, publishregistration.Config{BatchSize: 1, PollInterval: 20 * time.Millisecond, PublishTimeout: 300 * time.Millisecond, LeaseDuration: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- worker.Run(runCtx) }()
	joined := false
	defer func() {
		stop()
		if !joined {
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Error("worker cleanup timeout")
			}
		}
	}()
	select {
	case outcome := <-observed:
		if outcome != publishregistration.OutcomePublishError {
			t.Fatal("unexpected outcome", outcome)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	for {
		var pending, rescheduled bool
		if err := db.QueryRow(ctx, `SELECT published_at IS NULL, lease_token IS NULL AND next_attempt_at>clock_timestamp() FROM auth.registration_outbox WHERE event_id=$1`, second.ID[:]).Scan(&pending, &rescheduled); err != nil {
			t.Fatal(err)
		}
		if !pending {
			t.Fatal("outage marked published")
		}
		if rescheduled {
			break
		}
		select {
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		case <-time.After(time.Millisecond):
		}
	}
	stop()
	runErr := <-done
	joined = true
	if !errors.Is(runErr, context.Canceled) {
		t.Fatal("worker stop", runErr)
	}
	exec(`UPDATE auth.registration_outbox SET next_attempt_at=clock_timestamp()-interval '1 second' WHERE event_id=$1`, second.ID[:])
	retry, found, err := store.Claim(ctx, 5*time.Second)
	if err != nil || !found {
		t.Fatal("recovery claim", err)
	}
	if err := publisher.Publish(ctx, retry); err != nil {
		t.Fatal("recovery publish", err)
	}
	if ok, err := store.MarkPublished(ctx, retry); err != nil || !ok {
		t.Fatal("recovery completion", err)
	}
	// mTLS is required even when the caller trusts the server certificate.
	anonymous := publisherTLS.Clone()
	anonymous.Certificates = nil
	if nc, err := nats.Connect(broker, nats.Secure(anonymous), nats.Timeout(time.Second), nats.NoReconnect()); err == nil {
		nc.Close()
		t.Fatal("anonymous broker connection accepted")
	}
	// Auth can publish only its event subject, and cannot provision streams.
	permissionErrors := make(chan error, 4)
	scoped, err := nats.Connect(broker, nats.Secure(publisherTLS), nats.Timeout(time.Second), nats.NoReconnect(), nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) { permissionErrors <- err }))
	if err != nil {
		t.Fatal(err)
	}
	defer scoped.Close()
	for _, subject := range []string{"auth.unrelated.v1", "$JS.API.STREAM.CREATE.UNAUTHORIZED"} {
		if err := scoped.Publish(subject, []byte("{}")); err != nil {
			t.Fatal(err)
		}
		if err := scoped.FlushTimeout(time.Second); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-permissionErrors:
			if !errors.Is(err, nats.ErrPermissionViolation) {
				t.Fatal("unexpected ACL result", err)
			}
		case <-time.After(time.Second):
			t.Fatal("forbidden subject accepted")
		}
	}
	info, err = stream.Info(ctx)
	if err != nil || info.State.Msgs != 2 {
		t.Fatal("unexpected broker message count", err)
	}
}

type integrationObserver struct {
	outcomes chan publishregistration.Outcome
}

func (integrationObserver) ObserveBacklog(context.Context, int64, time.Duration) {}
func (o integrationObserver) Attempt(_ context.Context, outcome publishregistration.Outcome) {
	select {
	case o.outcomes <- outcome:
	default:
	}
}
