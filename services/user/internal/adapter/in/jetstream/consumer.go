// Package jetstream consumes an operator-provisioned registration durable.
package jetstream

import (
	"context"
	"crypto/tls"
	"errors"
	"github.com/nats-io/nats.go"
	js "github.com/nats-io/nats.go/jetstream"
	"github.com/v0hmly/marketmesh/services/user/internal/adapter/out/registrationwire"
	"github.com/v0hmly/marketmesh/services/user/internal/application/consumeregistration"
	"github.com/v0hmly/marketmesh/services/user/internal/domain/registrationevent"
	"net"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"time"
)

const (
	Stream  = "AUTH_REGISTRATION"
	Durable = "USER_PROFILES_V1"
	Subject = "auth.account.registered.v1"
)

type Outcome string

const (
	Applied     Outcome = "applied"
	Duplicate   Outcome = "duplicate"
	Invalid     Outcome = "invalid"
	Conflict    Outcome = "conflict"
	StoreError  Outcome = "store_error"
	BrokerError Outcome = "broker_error"
	AckError    Outcome = "ack_error"
)

type Config struct {
	URL                                                        string
	TLSConfig                                                  *tls.Config
	ConnectTimeout, OperationTimeout, FetchTimeout, RetryDelay time.Duration
	Observe                                                    func(Outcome)
	ObserveBacklog                                             func(uint64, int)
	ObserveAge                                                 func(time.Duration)
}
type Applier interface {
	Apply(context.Context, registrationevent.Event) (consumeregistration.Outcome, error)
}
type Consumer struct {
	cfg     Config
	apply   Applier
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.Mutex
	nc      *nats.Conn
	started atomic.Bool
}

func New(c Config, a Applier) (*Consumer, error) {
	u, e := url.Parse(c.URL)
	if e != nil || u.Scheme != "tls" || u.Hostname() == "" || u.Port() == "" || u.User != nil || u.ForceQuery || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || a == nil || c.TLSConfig == nil {
		return nil, errors.New("registration consumer: invalid configuration")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return nil, errors.New("registration consumer: invalid port")
	}
	t := c.TLSConfig
	if t.ServerName == "" || (t.MaxVersion != 0 && t.MaxVersion < t.MinVersion) || t.MinVersion < tls.VersionTLS13 || t.InsecureSkipVerify || t.RootCAs == nil || len(t.Certificates) == 0 || t.Certificates[0].PrivateKey == nil || len(t.Certificates[0].Certificate) == 0 {
		return nil, errors.New("registration consumer: invalid TLS configuration")
	}
	if c.ConnectTimeout == 0 {
		c.ConnectTimeout = 2 * time.Second
	}
	if c.OperationTimeout == 0 {
		c.OperationTimeout = 5 * time.Second
	}
	if c.FetchTimeout == 0 {
		c.FetchTimeout = time.Second
	}
	if c.RetryDelay == 0 {
		c.RetryDelay = time.Second
	}
	if c.ConnectTimeout <= 0 || c.ConnectTimeout > c.OperationTimeout || c.OperationTimeout <= 0 || c.OperationTimeout > 30*time.Second || c.FetchTimeout < 10*time.Millisecond || c.FetchTimeout > 5*time.Second || c.RetryDelay < time.Second || c.RetryDelay > time.Minute {
		return nil, errors.New("registration consumer: invalid timeouts")
	}
	c.TLSConfig = t.Clone()
	ctx, cancel := context.WithCancel(context.Background())
	return &Consumer{cfg: c, apply: a, ctx: ctx, cancel: cancel}, nil
}
func (c *Consumer) observe(o Outcome) {
	if c.cfg.Observe != nil {
		c.cfg.Observe(o)
	}
}
func (c *Consumer) Close() error {
	c.cancel()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.nc != nil {
		c.nc.Close()
		c.nc = nil
	}
	return nil
}
func (c *Consumer) connect(ctx context.Context) (js.Consumer, error) {
	ctx, cancel := context.WithTimeout(ctx, c.cfg.ConnectTimeout)
	defer cancel()
	d := &handshakeDialer{ctx: ctx, dial: (&net.Dialer{}).DialContext}
	defer d.abort()
	nc, err := nats.Connect(c.cfg.URL, nats.Secure(c.cfg.TLSConfig), nats.Timeout(c.cfg.ConnectTimeout), nats.NoReconnect(), nats.ReconnectBufSize(-1), nats.NoCallbacksAfterClientClose(), nats.SkipHostLookup(), nats.SetCustomDialer(d), nats.ErrorHandler(func(*nats.Conn, *nats.Subscription, error) {}))
	if err != nil {
		return nil, err
	}
	if err = d.release(); err != nil {
		nc.Close()
		return nil, err
	}
	c.mu.Lock()
	if c.ctx.Err() != nil || ctx.Err() != nil {
		c.mu.Unlock()
		nc.Close()
		return nil, context.Canceled
	}
	if c.nc != nil {
		c.nc.Close()
	}
	c.nc = nc
	c.mu.Unlock()
	j, err := js.New(nc)
	if err != nil {
		return nil, err
	}
	consumer, err := j.Consumer(ctx, Stream, Durable)
	if err != nil {
		return nil, err
	}
	cfg := consumer.CachedInfo().Config
	if cfg.DeliverSubject != "" || cfg.Durable != Durable || cfg.AckPolicy != js.AckExplicitPolicy || cfg.DeliverPolicy != js.DeliverAllPolicy || cfg.FilterSubject != Subject || len(cfg.FilterSubjects) != 0 || cfg.MaxDeliver != -1 || cfg.AckWait <= 2*c.cfg.OperationTimeout+time.Second || cfg.AckWait > 5*time.Minute || cfg.MaxAckPending < 1 || cfg.MaxAckPending > 1024 || len(cfg.BackOff) != 0 {
		return nil, errors.New("registration consumer: incompatible durable")
	}
	return consumer, nil
}
func (c *Consumer) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("registration consumer: context required")
	}
	if c.ctx.Err() != nil {
		return context.Canceled
	}
	if !c.started.CompareAndSwap(false, true) {
		return errors.New("registration consumer: already started")
	}
	defer c.Close()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(c.ctx, cancel)
	defer stop()
	// Closing the socket interrupts Fetch and DoubleAck during cancellation.
	stopSocket := context.AfterFunc(ctx, func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.nc != nil {
			c.nc.Close()
		}
	})
	defer stopSocket()
	var consumer js.Consumer
	var lastInfo time.Time
	for ctx.Err() == nil {
		if consumer == nil {
			var err error
			consumer, err = c.connect(ctx)
			if err != nil {
				c.observe(BrokerError)
				if !c.pause(ctx) {
					break
				}
				continue
			}
		}
		if c.cfg.ObserveBacklog != nil && time.Since(lastInfo) >= 5*time.Second {
			infoCtx, stopInfo := context.WithTimeout(ctx, c.cfg.OperationTimeout)
			info, err := consumer.Info(infoCtx)
			stopInfo()
			lastInfo = time.Now()
			if err == nil {
				c.cfg.ObserveBacklog(info.NumPending, info.NumAckPending)
			} else {
				c.observe(BrokerError)
			}
		}
		msg, err := consumer.Next(js.FetchMaxWait(c.cfg.FetchTimeout))
		if err != nil {
			if errors.Is(err, nats.ErrTimeout) || errors.Is(err, js.ErrNoMessages) {
				continue
			}
			consumer = nil
			c.observe(BrokerError)
			if !c.pause(ctx) {
				break
			}
			continue
		}
		c.handle(ctx, msg)
	}
	return ctx.Err()
}
func (c *Consumer) pause(ctx context.Context) bool {
	timer := time.NewTimer(c.cfg.RetryDelay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

type message interface {
	Data() []byte
	Subject() string
	DoubleAck(context.Context) error
	NakWithDelay(time.Duration) error
}

func (c *Consumer) handle(ctx context.Context, m message) {
	if ctx.Err() != nil {
		return
	}
	event, err := registrationwire.Unmarshal(m.Data())
	if m.Subject() != Subject {
		err = registrationevent.ErrInvalidEvent
	}
	if err != nil {
		c.observe(Invalid)
		if m.NakWithDelay(c.cfg.RetryDelay) != nil {
			c.observe(BrokerError)
		}
		return
	}
	applyCtx, cancel := context.WithTimeout(ctx, c.cfg.OperationTimeout)
	out, err := c.apply.Apply(applyCtx, event)
	cancel()
	if ctx.Err() != nil {
		return
	}
	if err == nil && out != consumeregistration.Applied && out != consumeregistration.Duplicate {
		err = errors.New("registration consumer: unknown apply outcome")
	}
	if err != nil {
		if errors.Is(err, registrationevent.ErrInvalidEvent) {
			c.observe(Invalid)
		} else if errors.Is(err, consumeregistration.ErrConflict) {
			c.observe(Conflict)
		} else {
			c.observe(StoreError)
		}
		if m.NakWithDelay(c.cfg.RetryDelay) != nil {
			c.observe(BrokerError)
		}
		return
	}
	if c.cfg.ObserveAge != nil {
		age := time.Since(event.OccurredAt)
		if age < 0 {
			age = 0
		}
		c.cfg.ObserveAge(age)
	}
	ackCtx, cancel := context.WithTimeout(ctx, c.cfg.OperationTimeout)
	defer cancel()
	if m.DoubleAck(ackCtx) != nil {
		c.observe(AckError)
		return
	}
	if out == consumeregistration.Duplicate {
		c.observe(Duplicate)
	} else {
		c.observe(Applied)
	}
}
