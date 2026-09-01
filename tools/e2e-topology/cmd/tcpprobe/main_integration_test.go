//go:build integration

package main

import (
	"net"
	"strconv"
	"testing"
	"time"
)

func TestRunConnectIntegration(t *testing.T) {
	listener := listenOrSkip(t)
	defer listener.Close()

	accepted := make(chan error, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr == nil {
			acceptErr = connection.Close()
		}
		accepted <- acceptErr
	}()

	err := run([]string{
		"connect",
		"--address",
		listener.Addr().String(),
		"--timeout",
		"1s",
	})
	if err != nil {
		t.Fatalf("run(connect) error = %v", err)
	}
	select {
	case acceptErr := <-accepted:
		if acceptErr != nil {
			t.Fatalf("Accept() error = %v", acceptErr)
		}
	case <-time.After(time.Second):
		t.Fatal("Accept() did not complete")
	}
}

func TestRunServeIntegrationIsBounded(t *testing.T) {
	previousRuntimeDir := probeRuntimeDir
	probeRuntimeDir = t.TempDir()
	t.Cleanup(func() { probeRuntimeDir = previousRuntimeDir })

	listener := listenOrSkip(t)
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	started := time.Now()
	err := run([]string{
		"serve",
		"--port",
		strconv.Itoa(port),
		"--lifetime",
		"100ms",
	})
	if err != nil {
		t.Fatalf("run(serve) error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Errorf("run(serve) elapsed = %s, want <= 1s", elapsed)
	}
}

func listenOrSkip(t *testing.T) net.Listener {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Skipf("loopback sockets are unavailable: %v", err)
	}
	return listener
}
