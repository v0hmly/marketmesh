package jetstream

import (
	"context"
	"errors"
	"net"
	"sync"
	"time"
)

// handshakeDialer applies one cancellation budget to DNS, TCP, INFO and TLS.
// SkipHostLookup makes NATS delegate resolution to this context-aware dialer.
type handshakeDialer struct {
	ctx  context.Context
	dial func(context.Context, string, string) (net.Conn, error)
	conn *handshakeConn
}

func (d *handshakeDialer) Dial(network, address string) (net.Conn, error) {
	if err := d.ctx.Err(); err != nil {
		return nil, err
	}
	if d.conn != nil {
		d.conn.abort()
		d.conn = nil
	}
	raw, err := d.dial(d.ctx, network, address)
	if err != nil {
		return nil, err
	}
	deadline, ok := d.ctx.Deadline()
	if !ok {
		_ = raw.Close()
		return nil, errors.New("connection deadline required")
	}
	guarded := &handshakeConn{Conn: raw, ctx: d.ctx, deadline: deadline, active: true}
	// Install the initial socket deadline before the library can read server INFO.
	if err := guarded.SetDeadline(deadline); err != nil {
		_ = raw.Close()
		return nil, err
	}
	guarded.stop = context.AfterFunc(d.ctx, guarded.cancel)
	d.conn = guarded
	if err := d.ctx.Err(); err != nil {
		guarded.abort()
		return nil, err
	}
	return guarded, nil
}
func (d *handshakeDialer) abort() {
	if d.conn != nil {
		d.conn.abort()
		d.conn = nil
	}
}
func (d *handshakeDialer) release() error {
	if d.conn == nil {
		return errors.New("connection missing")
	}
	if err := d.conn.release(); err != nil {
		return err
	}
	d.conn = nil
	return nil
}

// NATS resets and clears deadlines during connect; those updates must never
// extend the shared budget. The guard is removed only after successful setup.
type handshakeConn struct {
	net.Conn
	mu                sync.Mutex
	ctx               context.Context
	deadline          time.Time
	active, cancelled bool
	stop              func() bool
}

func (c *handshakeConn) clamp(deadline time.Time) time.Time {
	if c.active && (deadline.IsZero() || deadline.After(c.deadline)) {
		return c.deadline
	}
	return deadline
}
func (c *handshakeConn) SetDeadline(deadline time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Conn.SetDeadline(c.clamp(deadline))
}
func (c *handshakeConn) SetReadDeadline(deadline time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Conn.SetReadDeadline(c.clamp(deadline))
}
func (c *handshakeConn) SetWriteDeadline(deadline time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.Conn.SetWriteDeadline(c.clamp(deadline))
}
func (c *handshakeConn) cancel() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active {
		c.cancelled = true
		_ = c.Conn.Close()
	}
}
func (c *handshakeConn) abort() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.stop != nil {
		c.stop()
	}
	c.active = false
	_ = c.Conn.Close()
}
func (c *handshakeConn) release() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.ctx.Err(); err != nil {
		_ = c.Conn.Close()
		return err
	}
	if c.cancelled {
		return context.Canceled
	}
	// A callback already queued by cancellation also takes mu and sees active=false,
	// preventing it from closing the successfully transferred connection later.
	c.active = false
	if c.stop != nil {
		c.stop()
	}
	return c.Conn.SetDeadline(time.Time{})
}
