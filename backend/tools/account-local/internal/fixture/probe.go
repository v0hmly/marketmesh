package fixture

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nats-io/nats.go"
)

func Probe(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	counts := map[string]uint64{}
	for _, item := range []struct{ service, key, sql string }{
		{"auth", "registration_outbox_pending", "SELECT count(*) FROM auth.registration_outbox WHERE published_at IS NULL"},
		{"user", "registration_inbox_applied", "SELECT count(*) FROM users.registration_inbox"},
		{"user", "profiles", "SELECT count(*) FROM users.profiles"},
	} {
		conn, err := pgx.Connect(ctx, os.Getenv(strings.ToUpper(item.service)+"_ADMIN_DSN"))
		if err != nil {
			return errors.New("probe database unavailable")
		}
		var count uint64
		err = conn.QueryRow(ctx, item.sql).Scan(&count)
		_ = conn.Close(ctx)
		if err != nil {
			return errors.New("probe count unavailable")
		}
		counts[item.key] = count
	}
	cfg, err := clientTLS("/secrets", "nats")
	if err != nil {
		return err
	}
	pair, err := tls.LoadX509KeyPair("/secrets/cert.pem", "/secrets/key.pem")
	if err != nil {
		return errors.New("probe identity unavailable")
	}
	cfg.Certificates = []tls.Certificate{pair}
	nc, err := nats.Connect("tls://nats:4222", nats.Secure(cfg), nats.Timeout(2*time.Second), nats.NoReconnect())
	if err != nil {
		return errors.New("probe NATS unavailable")
	}
	defer nc.Close()
	js, err := nc.JetStream()
	if err != nil {
		return errors.New("probe JetStream unavailable")
	}
	info, err := js.ConsumerInfo("AUTH_REGISTRATION", "USER_PROFILES_V1", nats.Context(ctx))
	if err != nil {
		return errors.New("probe consumer unavailable")
	}
	counts["events_pending"] = info.NumPending
	counts["events_ack_pending"] = uint64(info.NumAckPending)
	return json.NewEncoder(os.Stdout).Encode(counts)
}
