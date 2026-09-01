package redis

import (
	"errors"
	"net"
	"strings"
	"testing"
)

func TestSafeConnectionRedactsAddresses(t *testing.T) {
	t.Parallel()

	clientConnection, serverConnection := net.Pipe()
	t.Cleanup(func() {
		_ = clientConnection.Close()
		_ = serverConnection.Close()
	})
	connection := &safeConn{Conn: clientConnection}
	if connection.LocalAddr().String() != "[REDACTED]" ||
		connection.RemoteAddr().String() != "[REDACTED]" {
		t.Fatalf(
			"safe addresses = %q/%q",
			connection.LocalAddr(),
			connection.RemoteAddr(),
		)
	}
}

func TestNetworkErrorHidesDetailsAndPreservesNetworkBehavior(t *testing.T) {
	t.Parallel()

	cause := &net.DNSError{
		Err:         "lookup private.redis.internal failed",
		Name:        "private.redis.internal",
		IsTimeout:   true,
		IsTemporary: true,
	}
	err := wrapNetworkError("connect", cause)
	if !errors.Is(err, cause) {
		t.Fatalf("wrapped error = %v, want preserved cause", err)
	}
	var networkErr net.Error
	if !errors.As(err, &networkErr) || !networkErr.Timeout() {
		t.Fatalf("wrapped error does not preserve timeout behavior: %v", err)
	}
	if strings.Contains(err.Error(), "private.redis.internal") {
		t.Fatalf("wrapped error exposed address: %v", err)
	}
}
