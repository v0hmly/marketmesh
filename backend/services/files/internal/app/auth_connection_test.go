package app

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/test/bufconn"
)

func TestAuthConnectionAllowsColdStartBeyondRPCDeadline(t *testing.T) {
	t.Parallel()
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	t.Cleanup(server.Stop)
	t.Cleanup(func() { _ = listener.Close() })
	go func() { _ = server.Serve(listener) }()
	connection, err := grpc.NewClient("passthrough:///auth", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			timer := time.NewTimer(2100 * time.Millisecond)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-timer.C:
				return listener.DialContext(ctx)
			}
		}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	if err := connectAuth(t.Context(), connection); err != nil {
		t.Fatalf("healthy cold connection rejected: %v", err)
	}
}

func TestAuthConnectionRespectsParentCancellation(t *testing.T) {
	t.Parallel()
	connection, err := grpc.NewClient("passthrough:///auth", grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	if err := connectAuth(ctx, connection); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("connection ignored parent deadline: %v", err)
	}
}

func TestAuthConnectionRejectsClosedClient(t *testing.T) {
	t.Parallel()
	connection, err := grpc.NewClient("passthrough:///auth", grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	_ = connection.Close()
	if err := connectAuth(t.Context(), connection); err == nil {
		t.Fatal("closed connection accepted")
	}
}
