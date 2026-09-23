//go:build ignore

// A bounded TCP round-robin fixture: TLS is passed unchanged to gateway-in.
package main

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"os/signal"
	"sync"
	"syscall"
	"time"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()
	listener, err := net.Listen("tcp", ":30443")
	if err != nil {
		log.Fatal(err)
	}
	if err := serve(ctx, listener, []string{"127.0.0.1:30444", "127.0.0.1:30445"}); err != nil {
		log.Fatal(err)
	}
}

func serve(ctx context.Context, listener net.Listener, backends []string) error {
	ctx, cancel := context.WithCancel(ctx)
	var workers sync.WaitGroup
	workers.Go(func() {
		<-ctx.Done()
		_ = listener.Close()
	})
	defer func() { cancel(); workers.Wait() }()
	if len(backends) == 0 {
		return errors.New("no tunnel backends")
	}
	slots := make(chan struct{}, 16)
	next := 0
	for {
		client, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		select {
		case slots <- struct{}{}:
			backend := backends[next%len(backends)]
			next++
			workers.Go(func() {
				defer func() { <-slots }()
				forward(ctx, client, backend)
			})
		default:
			_ = client.Close()
		}
	}
}

func forward(ctx context.Context, client net.Conn, address string) {
	defer client.Close()
	upstream, err := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, "tcp", address)
	if err != nil {
		return
	}
	defer upstream.Close()
	done := make(chan struct{}, 2)
	var copies sync.WaitGroup
	copies.Go(func() { _, _ = io.Copy(upstream, client); done <- struct{}{} })
	copies.Go(func() { _, _ = io.Copy(client, upstream); done <- struct{}{} })
	select {
	case <-ctx.Done():
	case <-done:
	}
	_ = client.Close()
	_ = upstream.Close()
	copies.Wait()
}
