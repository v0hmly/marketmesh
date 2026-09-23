package redis

import (
	"context"
	"crypto/tls"
	"net"
	"time"

	serviceruntime "github.com/v0hmly/marketmesh/platform/runtime"
)

const redactedRedisAddress = "redacted.invalid:6379"

type safeDialer struct {
	address serviceruntime.Secret
	timeout time.Duration
	tls     *tls.Config
}

func (dialer safeDialer) dial(ctx context.Context, _, _ string) (net.Conn, error) {
	connection, err := (&net.Dialer{Timeout: dialer.timeout}).DialContext(
		ctx,
		"tcp",
		dialer.address.Reveal(),
	)
	if err != nil {
		return nil, wrapNetworkError("connect", err)
	}

	if dialer.tls == nil {
		return &safeConn{Conn: connection}, nil
	}

	tlsConnection := tls.Client(connection, dialer.tls.Clone())
	if err := tlsConnection.HandshakeContext(ctx); err != nil {
		_ = connection.Close()
		return nil, wrapNetworkError("TLS handshake", err)
	}

	return &safeConn{Conn: tlsConnection}, nil
}

type safeConn struct {
	net.Conn
}

func (connection *safeConn) Read(buffer []byte) (int, error) {
	read, err := connection.Conn.Read(buffer)

	return read, wrapNetworkError("read", err)
}

func (connection *safeConn) Write(buffer []byte) (int, error) {
	written, err := connection.Conn.Write(buffer)

	return written, wrapNetworkError("write", err)
}

func (connection *safeConn) Close() error {
	return wrapNetworkError("close", connection.Conn.Close())
}

func (connection *safeConn) LocalAddr() net.Addr {
	return redactedAddr{}
}

func (connection *safeConn) RemoteAddr() net.Addr {
	return redactedAddr{}
}

func (connection *safeConn) SetDeadline(deadline time.Time) error {
	return wrapNetworkError("set deadline", connection.Conn.SetDeadline(deadline))
}

func (connection *safeConn) SetReadDeadline(deadline time.Time) error {
	return wrapNetworkError("set read deadline", connection.Conn.SetReadDeadline(deadline))
}

func (connection *safeConn) SetWriteDeadline(deadline time.Time) error {
	return wrapNetworkError("set write deadline", connection.Conn.SetWriteDeadline(deadline))
}

type redactedAddr struct{}

func (redactedAddr) Network() string {
	return "tcp"
}

func (redactedAddr) String() string {
	return "[REDACTED]"
}

type networkError struct {
	operation string
	err       error
}

func (err *networkError) Error() string {
	return "redis: network " + err.operation + " failed"
}

func (err *networkError) Unwrap() error {
	return err.err
}

func wrapNetworkError(operation string, err error) error {
	if err == nil {
		return nil
	}

	return &networkError{operation: operation, err: err}
}
