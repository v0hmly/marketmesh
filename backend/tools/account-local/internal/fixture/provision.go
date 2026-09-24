package fixture

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nats-io/nats.go"
)

func clientTLS(dir, server string) (*tls.Config, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "ca.pem"))
	if err != nil {
		return nil, errors.New("read fixture CA")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(raw) {
		return nil, errors.New("invalid fixture CA")
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, ServerName: server}, nil
}

// Provision applies checksum-tracked migrations transactionally and grants only
// app table privileges; it never inserts credentials, profiles or session rows.
func Provision(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	defer cancel()
	services := []string{"auth", "user"}
	if os.Getenv("STAFF_ADMIN_DSN") != "" {
		services = append(services, "staff")
	}
	for _, service := range services {
		conn, err := pgx.Connect(ctx, os.Getenv(strings.ToUpper(service)+"_ADMIN_DSN"))
		if err != nil {
			return fmt.Errorf("%s database unavailable", service)
		}
		err = migrate(ctx, conn, "/migrations/"+service, service)
		_ = conn.Close(ctx)
		if err != nil {
			return fmt.Errorf("%s migration failed: %w", service, err)
		}
	}
	cfg, err := clientTLS("/secrets", "nats")
	if err != nil {
		return err
	}
	pair, err := tls.LoadX509KeyPair("/secrets/cert.pem", "/secrets/key.pem")
	if err != nil {
		return errors.New("read provision identity")
	}
	cfg.Certificates = []tls.Certificate{pair}
	var nc *nats.Conn
	for ctx.Err() == nil {
		nc, err = nats.Connect("tls://nats:4222", nats.Secure(cfg), nats.Timeout(time.Second), nats.NoReconnect())
		if err == nil {
			break
		}
		select {
		case <-ctx.Done():
		case <-time.After(250 * time.Millisecond):
		}
	}
	if err != nil {
		return errors.New("NATS provision connection failed")
	}
	defer nc.Close()
	js, err := nc.JetStream()
	if err != nil {
		return errors.New("JetStream unavailable")
	}
	stream := &nats.StreamConfig{Name: "AUTH_REGISTRATION", Subjects: []string{"auth.account.registered.v1"}, Storage: nats.FileStorage, Retention: nats.LimitsPolicy, Duplicates: 2 * time.Minute, MaxBytes: 128 * 1024 * 1024}
	existingStream, err := js.StreamInfo(stream.Name, nats.Context(ctx))
	if errors.Is(err, nats.ErrStreamNotFound) {
		_, err = js.AddStream(stream, nats.Context(ctx))
	} else if err == nil && !compatibleStream(existingStream.Config, *stream) {
		return errors.New("existing registration stream differs from fixture policy")
	}
	if err != nil {
		return errors.New("registration stream configuration failed")
	}
	consumer := &nats.ConsumerConfig{Durable: "USER_PROFILES_V1", FilterSubject: "auth.account.registered.v1", AckPolicy: nats.AckExplicitPolicy, DeliverPolicy: nats.DeliverAllPolicy, MaxDeliver: -1, AckWait: 30 * time.Second, MaxAckPending: 16}
	existingConsumer, err := js.ConsumerInfo(stream.Name, consumer.Durable, nats.Context(ctx))
	if errors.Is(err, nats.ErrConsumerNotFound) {
		_, err = js.AddConsumer(stream.Name, consumer, nats.Context(ctx))
	} else if err == nil && !compatibleConsumer(existingConsumer.Config, *consumer) {
		return errors.New("existing registration consumer differs from fixture policy")
	}
	if err != nil {
		return errors.New("registration consumer configuration failed")
	}
	return nil
}

func migrationFiles(dir string) ([]string, error) {
	files, err := filepath.Glob(filepath.Join(dir, "*.up.sql"))
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	if len(files) == 0 {
		return nil, errors.New("migrations missing")
	}
	return files, nil
}
func migrate(ctx context.Context, conn *pgx.Conn, dir, schema string) error {
	files, err := migrationFiles(dir)
	if err != nil {
		return err
	}
	tx, err := conn.Begin(ctx)
	if err != nil {
		return errors.New("begin transaction")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(640015); CREATE TABLE IF NOT EXISTS public.local_fixture_migrations (name text PRIMARY KEY, checksum text NOT NULL)`); err != nil {
		return errors.New("migration ledger unavailable")
	}
	for _, file := range files {
		raw, e := os.ReadFile(file)
		if e != nil {
			return errors.New("migration unreadable")
		}
		sum := sha256.Sum256(raw)
		checksum := hex.EncodeToString(sum[:])
		var existing string
		e = tx.QueryRow(ctx, `SELECT checksum FROM public.local_fixture_migrations WHERE name=$1`, filepath.Base(file)).Scan(&existing)
		if e == nil {
			if checksum != existing {
				return errors.New("applied migration checksum changed")
			}
			continue
		}
		if !errors.Is(e, pgx.ErrNoRows) {
			return errors.New("migration ledger read failed")
		}
		if _, e = tx.Exec(ctx, string(raw)); e != nil {
			return fmt.Errorf("apply %s", filepath.Base(file))
		}
		if _, e = tx.Exec(ctx, `INSERT INTO public.local_fixture_migrations(name,checksum) VALUES($1,$2)`, filepath.Base(file), checksum); e != nil {
			return errors.New("migration ledger write failed")
		}
	}
	// schema is a fixed internal allowlist, never caller-controlled SQL.
	if schema != "auth" && schema != "user" && schema != "staff" {
		return errors.New("unknown schema")
	}
	rw := pgx.Identifier{schema + "_rw"}.Sanitize()
	ro := pgx.Identifier{schema + "_ro"}.Sanitize()
	if schema == "user" {
		schema = "users"
	}
	quoted := pgx.Identifier{schema}.Sanitize()
	_, err = tx.Exec(ctx, `GRANT USAGE ON SCHEMA `+quoted+` TO `+rw+`,`+ro+`; GRANT SELECT,INSERT,UPDATE,DELETE ON ALL TABLES IN SCHEMA `+quoted+` TO `+rw+`; GRANT SELECT ON ALL TABLES IN SCHEMA `+quoted+` TO `+ro+`; GRANT USAGE,SELECT ON ALL SEQUENCES IN SCHEMA `+quoted+` TO `+rw)
	if err != nil {
		return errors.New("application grants failed")
	}
	return tx.Commit(ctx)
}

func compatibleStream(actual, expected nats.StreamConfig) bool {
	return actual.Name == expected.Name && len(actual.Subjects) == 1 && actual.Subjects[0] == expected.Subjects[0] && actual.Storage == expected.Storage && actual.Retention == expected.Retention && actual.Duplicates == expected.Duplicates && actual.MaxBytes == expected.MaxBytes
}
func compatibleConsumer(actual, expected nats.ConsumerConfig) bool {
	return actual.Durable == expected.Durable && actual.FilterSubject == expected.FilterSubject && len(actual.FilterSubjects) == 0 && actual.DeliverSubject == "" && actual.AckPolicy == expected.AckPolicy && actual.DeliverPolicy == expected.DeliverPolicy && actual.MaxDeliver == expected.MaxDeliver && actual.AckWait == expected.AckWait && actual.MaxAckPending == expected.MaxAckPending && len(actual.BackOff) == 0
}
