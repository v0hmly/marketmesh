// Package jetstream publishes registration events to an existing stream with synchronous acknowledgements.
package jetstream

import (
	"context"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"net"
	"net/url"
	"sync/atomic"
	"time"

	"github.com/nats-io/nats.go"
	natsjs "github.com/nats-io/nats.go/jetstream"
	"github.com/v0hmly/marketmesh/services/auth/internal/application/publishregistration"
)

const Subject = "auth.account.registered.v1"
const Stream = "AUTH_REGISTRATION"

type Config struct {
	URL                            string
	TLS                            *tls.Config
	ConnectTimeout, PublishTimeout time.Duration
	ValidatePayload                func([]byte, [16]byte) error
}
type connection interface {
	PublishMsg(context.Context, *nats.Msg, ...natsjs.PublishOpt) (*natsjs.PubAck, error)
	Connected() bool
	Close()
}
type Publisher struct {
	config       Config
	gate         chan struct{}
	closed       atomic.Bool
	conn         connection
	dial         func(context.Context) (connection, error)
	dialContext  func(context.Context, string, string) (net.Conn, error)
	closeContext context.Context
	stop         context.CancelFunc
}

func New(c Config) (*Publisher, error) {
	u, err := url.Parse(c.URL)
	if err != nil || u.Scheme != "tls" || u.Hostname() == "" || u.Port() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || c.TLS == nil || c.TLS.MinVersion < tls.VersionTLS13 || c.TLS.InsecureSkipVerify || c.TLS.RootCAs == nil || len(c.TLS.Certificates) == 0 || c.TLS.Certificates[0].PrivateKey == nil || len(c.TLS.Certificates[0].Certificate) == 0 || c.ValidatePayload == nil {
		return nil, errors.New("registration jetstream: invalid configuration")
	}
	if c.ConnectTimeout == 0 {
		c.ConnectTimeout = 2 * time.Second
	}
	if c.PublishTimeout == 0 {
		c.PublishTimeout = 5 * time.Second
	}
	if c.ConnectTimeout <= 0 || c.PublishTimeout <= 0 || c.ConnectTimeout > c.PublishTimeout || c.PublishTimeout > time.Minute {
		return nil, errors.New("registration jetstream: invalid timeouts")
	}
	c.TLS = c.TLS.Clone()
	closeCtx, stop := context.WithCancel(context.Background())
	p := &Publisher{config: c, gate: make(chan struct{}, 1), closeContext: closeCtx, stop: stop, dialContext: (&net.Dialer{}).DialContext}
	p.gate <- struct{}{}
	p.dial = p.connect
	return p, nil
}
func (p *Publisher) Publish(ctx context.Context, r publishregistration.Record) error {
	if ctx == nil {
		return errors.New("registration jetstream: context required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.closed.Load() {
		return errors.New("registration jetstream: publisher closed")
	}
	if r.ID == ([16]byte{}) || len(r.Payload) < 1 || len(r.Payload) > 8192 {
		return errors.New("registration jetstream: invalid event")
	}
	// Keep validated bytes private: caller mutation cannot alter the published message.
	payload := append([]byte(nil), r.Payload...)
	if err := p.config.ValidatePayload(payload, r.ID); err != nil {
		return errors.New("registration jetstream: invalid event payload")
	}
	ctx, cancel := context.WithTimeout(ctx, p.config.PublishTimeout)
	defer cancel()
	conn, err := p.connection(ctx)
	if err != nil {
		return safeError{err}
	}
	if p.closed.Load() {
		return errors.New("registration jetstream: publisher closed")
	}
	message := &nats.Msg{Subject: Subject, Data: payload, Header: nats.Header{}}
	message.Header.Set(natsjs.MsgIDHeader, hex.EncodeToString(r.ID[:]))
	message.Header.Set(natsjs.ExpectedStreamHeader, Stream)
	ack, err := conn.PublishMsg(ctx, message, natsjs.WithRetryAttempts(0))
	if err != nil {
		return safeError{err}
	}
	if err := ctx.Err(); err != nil {
		return safeError{err}
	}
	if ack == nil || ack.Stream != Stream || ack.Sequence == 0 {
		return errors.New("registration jetstream: invalid publish acknowledgement")
	}
	return nil
}
func (p *Publisher) connection(ctx context.Context) (connection, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.closeContext.Done():
		return nil, errors.New("publisher closed")
	case <-p.gate:
	}
	defer func() { p.gate <- struct{}{} }()
	if p.closed.Load() {
		return nil, errors.New("publisher closed")
	}
	if p.conn != nil && p.conn.Connected() {
		return p.conn, nil
	}
	if p.conn != nil {
		p.conn.Close()
		p.conn = nil
	}
	conn, err := p.dial(ctx)
	if err != nil {
		return nil, err
	}
	if p.closed.Load() || ctx.Err() != nil {
		conn.Close()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("publisher closed")
	}
	p.conn = conn
	return conn, nil
}
func (p *Publisher) connect(ctx context.Context) (connection, error) {
	connectCtx, cancel := context.WithTimeout(ctx, p.config.ConnectTimeout)
	defer cancel()
	stopClose := context.AfterFunc(p.closeContext, cancel)
	defer stopClose()
	if err := connectCtx.Err(); err != nil {
		return nil, err
	}
	dialer := &handshakeDialer{ctx: connectCtx, dial: p.dialContext}
	defer dialer.abort()
	nc, err := nats.Connect(p.config.URL, p.connectionOptions(dialer)...)
	if err != nil {
		if connectCtx.Err() != nil {
			return nil, connectCtx.Err()
		}
		return nil, err
	}
	if err := dialer.release(); err != nil {
		nc.Close()
		return nil, err
	}
	js, err := natsjs.New(nc)
	if err != nil {
		nc.Close()
		return nil, err
	}
	return &natsConnection{Conn: nc, JetStream: js}, nil
}
func (p *Publisher) connectionOptions(dialer nats.CustomDialer) []nats.Option {
	return []nats.Option{nats.Secure(p.config.TLS), nats.Timeout(p.config.ConnectTimeout), nats.NoReconnect(), nats.ReconnectBufSize(-1), nats.NoCallbacksAfterClientClose(), nats.SkipHostLookup(), nats.SetCustomDialer(dialer),
		// NATS' default callback writes raw errors and subjects to process stderr.
		// Publish results already feed the bounded worker outcome metrics.
		nats.ErrorHandler(func(*nats.Conn, *nats.Subscription, error) {})}
}

// Close permanently prevents future connections and closes in-flight requests.
func (p *Publisher) Close() error {
	if p.closed.Swap(true) {
		return nil
	}
	p.stop()
	<-p.gate
	defer func() { p.gate <- struct{}{} }()
	if p.conn != nil {
		p.conn.Close()
		p.conn = nil
	}
	return nil
}

type natsConnection struct {
	*nats.Conn
	natsjs.JetStream
}

func (c *natsConnection) Connected() bool { return c.Conn.IsConnected() }
func (c *natsConnection) Close()          { c.Conn.Close() }

// Explicit method avoids embedding ambiguity with NATS Core's PublishMsg.
func (c *natsConnection) PublishMsg(ctx context.Context, msg *nats.Msg, opts ...natsjs.PublishOpt) (*natsjs.PubAck, error) {
	return c.JetStream.PublishMsg(ctx, msg, opts...)
}

type safeError struct{ cause error }

func (safeError) Error() string   { return "registration jetstream: publish failed" }
func (e safeError) Unwrap() error { return e.cause }

var _ publishregistration.Publisher = (*Publisher)(nil)
