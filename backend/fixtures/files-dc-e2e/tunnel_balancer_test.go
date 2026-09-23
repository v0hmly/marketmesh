//go:build ignore

package main

import (
	"context"
	"io"
	"net"
	"testing"
	"time"
)

func TestDistributionAndShutdown(t *testing.T) {
	var addresses []string
	for _, value := range []byte{'a', 'b'} {
		backend, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = backend.Close() })
		addresses = append(addresses, backend.Addr().String())
		done := make(chan struct{})
		go func() {
			defer close(done)
			conn, err := backend.Accept()
			if err != nil {
				return
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
			var input [1]byte
			if _, err := io.ReadFull(conn, input[:]); err == nil {
				_, _ = conn.Write([]byte{input[0], value})
				_, _ = io.Copy(io.Discard, conn)
			}
		}()
		t.Cleanup(func() { _ = backend.Close(); <-done })
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- serve(ctx, listener, addresses) }()
	for _, want := range []string{"xa", "xb"} {
		conn, err := net.DialTimeout("tcp", listener.Addr().String(), time.Second)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := conn.Write([]byte("x")); err != nil {
			t.Fatal(err)
		}
		var output [2]byte
		if _, err := io.ReadFull(conn, output[:]); err != nil {
			t.Fatal(err)
		}
		if string(output[:]) != want {
			t.Fatalf("got %q, want %q", output, want)
		}
	}
	cancel() // Open streams must not keep any forwarding worker alive.
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown left active forwarding workers")
	}
}
