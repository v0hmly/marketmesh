package jetstream

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	natsjs "github.com/nats-io/nats.go/jetstream"
	"github.com/v0hmly/marketmesh/services/auth/internal/application/publishregistration"
)

type fakeConnection struct {
	closed  atomic.Bool
	calls   int
	ack     *natsjs.PubAck
	err     error
	message *nats.Msg
}

func (c *fakeConnection) Connected() bool { return !c.closed.Load() }
func (c *fakeConnection) Close()          { c.closed.Store(true) }
func (c *fakeConnection) PublishMsg(_ context.Context, m *nats.Msg, _ ...natsjs.PublishOpt) (*natsjs.PubAck, error) {
	c.calls++
	c.message = m
	return c.ack, c.err
}
func testConfig(t *testing.T) Config {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	return Config{URL: "tls://localhost:4222", TLS: &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: x509.NewCertPool(), Certificates: []tls.Certificate{{Certificate: [][]byte{{1}}, PrivateKey: key}}}, ValidatePayload: func([]byte, [16]byte) error { return nil }}
}
func TestAckIdentityAndPayloadValidation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ack   *natsjs.PubAck
		err   error
		valid bool
	}{{name: "ack", ack: &natsjs.PubAck{Stream: Stream, Sequence: 1}, valid: true}, {name: "duplicate ack", ack: &natsjs.PubAck{Stream: Stream, Sequence: 1, Duplicate: true}, valid: true}, {name: "wrong stream", ack: &natsjs.PubAck{Stream: "OTHER", Sequence: 1}}, {name: "zero sequence", ack: &natsjs.PubAck{Stream: Stream}}, {name: "nil ack"}, {name: "broker failure", err: errors.New("private URL")}} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := New(testConfig(t))
			if err != nil {
				t.Fatal(err)
			}
			c := &fakeConnection{ack: tc.ack, err: tc.err}
			p.dial = func(context.Context) (connection, error) { return c, nil }
			r := publishregistration.Record{ID: [16]byte{1}, Payload: []byte("payload")}
			err = p.Publish(context.Background(), r)
			if (err == nil) != tc.valid {
				t.Fatal("ack classification", err)
			}
			if err != nil && strings.Contains(err.Error(), "private") {
				t.Fatal("sensitive error")
			}
			if c.message.Subject != Subject || c.message.Header.Get(natsjs.MsgIDHeader) != "01000000000000000000000000000000" || c.message.Header.Get(natsjs.ExpectedStreamHeader) != Stream {
				t.Fatal("incorrect routing or dedup identity")
			}
			r.Payload[0] = 'X'
			if string(c.message.Data) != "payload" {
				t.Fatal("payload aliases caller")
			}
			_ = p.Close()
		})
	}
	c := testConfig(t)
	c.ValidatePayload = func([]byte, [16]byte) error { return errors.New("private body") }
	p, _ := New(c)
	p.dial = func(context.Context) (connection, error) { t.Fatal("invalid payload connected"); return nil, nil }
	if err := p.Publish(context.Background(), publishregistration.Record{ID: [16]byte{1}, Payload: []byte{1}}); err == nil || strings.Contains(err.Error(), "private") {
		t.Fatal("payload rejection", err)
	}
}
func TestLazyReconnectAndPermanentClose(t *testing.T) {
	p, _ := New(testConfig(t))
	calls := 0
	var conn *fakeConnection
	p.dial = func(context.Context) (connection, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("offline")
		}
		conn = &fakeConnection{ack: &natsjs.PubAck{Stream: Stream, Sequence: 1}}
		return conn, nil
	}
	r := publishregistration.Record{ID: [16]byte{1}, Payload: []byte{1}}
	if err := p.Publish(context.Background(), r); err == nil {
		t.Fatal("offline publish succeeded")
	}
	if err := p.Publish(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if err := p.Publish(context.Background(), r); err != nil {
		t.Fatal(err)
	}
	if calls != 3 {
		t.Fatal("did not reconnect")
	}
	_ = p.Close()
	_ = p.Close()
	if err := p.Publish(context.Background(), r); err == nil || calls != 3 {
		t.Fatal("closed publisher resurrected")
	}
}
func TestCloseDuringConnectionPreventsResurrection(t *testing.T) {
	p, _ := New(testConfig(t))
	entered, release := make(chan struct{}), make(chan struct{})
	c := &fakeConnection{}
	p.dial = func(context.Context) (connection, error) { close(entered); <-release; return c, nil }
	done := make(chan error, 1)
	go func() {
		done <- p.Publish(context.Background(), publishregistration.Record{ID: [16]byte{1}, Payload: []byte{1}})
	}()
	<-entered
	closed := make(chan struct{})
	go func() { _ = p.Close(); close(closed) }()
	deadline := time.Now().Add(time.Second)
	for !p.closed.Load() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	close(release)
	if err := <-done; err == nil {
		t.Fatal("publish succeeded during close")
	}
	<-closed
	if !c.closed.Load() || c.calls != 0 {
		t.Fatal("connection survived close")
	}
}
func TestValidationAndCancelledGate(t *testing.T) {
	for _, change := range []func(*Config){func(c *Config) { c.URL = "nats://localhost:4222" }, func(c *Config) { c.TLS.InsecureSkipVerify = true }, func(c *Config) { c.TLS.MinVersion = tls.VersionTLS12 }, func(c *Config) { c.ValidatePayload = nil }, func(c *Config) { c.ConnectTimeout = time.Minute; c.PublishTimeout = time.Second }} {
		c := testConfig(t)
		change(&c)
		if _, err := New(c); err == nil {
			t.Fatal("unsafe config accepted")
		}
	}
	p, _ := New(testConfig(t))
	if err := p.Publish(nil, publishregistration.Record{}); err == nil {
		t.Fatal("nil context accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := p.Publish(ctx, publishregistration.Record{}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
