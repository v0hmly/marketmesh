package tunnel

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	contractv1 "github.com/v0hmly/marketmesh/api/gen/go/tunnel/v1"
	grpcgo "google.golang.org/grpc"
)

func TestApplicationPingPongKeepsTunnelAlive(t *testing.T) {
	t.Parallel()

	pki := newTestPKI(t)
	internal := newFakeHealth()
	clients, _ := startInternalHealth(t, internal)
	registry := newTestRegistry(t, clients, contractv1.RouteId_ROUTE_ID_USER_GET_ME)
	pongSent := make(chan struct{}, 1)
	gateway := &fakeGatewayIn{connect: func(
		stream grpcgo.BidiStreamingServer[contractv1.ConnectRequest, contractv1.ConnectResponse],
	) error {
		if _, err := acceptTestHello(stream); err != nil {
			return err
		}
		for {
			frame, err := stream.Recv()
			if err != nil {
				return err
			}
			ping := frame.GetPing()
			if ping == nil {
				continue
			}
			if err := stream.Send(testGatewayInFrame(
				nil,
				contractv1.TrafficClass_TRAFFIC_CLASS_UNSPECIFIED,
				0,
				&contractv1.ConnectResponse_Pong{Pong: &contractv1.Pong{Nonce: ping.GetNonce()}},
			)); err != nil {
				return err
			}
			notify(pongSent)
			<-stream.Context().Done()
			return nil
		}
	}}
	dialer := startGatewayIn(t, pki, gateway)
	config := newTestConfig(t, pki, dialer, &bytes.Buffer{})
	config.PingInterval = 30 * time.Millisecond
	config.PingTimeout = 20 * time.Millisecond
	client, err := NewClient(config, registry)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	runCtx, cancelRun := context.WithCancel(t.Context())
	runResult := make(chan error, 1)
	go func() { runResult <- client.Run(runCtx) }()

	select {
	case <-pongSent:
	case <-time.After(time.Second):
		t.Fatal("application Ping/Pong was not exchanged")
	}
	cancelRun()
	select {
	case err := <-runResult:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run() did not stop")
	}
}

func TestMissingApplicationPongEndsBoundedAttempt(t *testing.T) {
	t.Parallel()

	pki := newTestPKI(t)
	internal := newFakeHealth()
	clients, _ := startInternalHealth(t, internal)
	registry := newTestRegistry(t, clients, contractv1.RouteId_ROUTE_ID_USER_GET_ME)
	gateway := &fakeGatewayIn{connect: func(
		stream grpcgo.BidiStreamingServer[contractv1.ConnectRequest, contractv1.ConnectResponse],
	) error {
		if _, err := acceptTestHello(stream); err != nil {
			return err
		}
		for {
			if _, err := stream.Recv(); err != nil {
				return err
			}
		}
	}}
	dialer := startGatewayIn(t, pki, gateway)
	config := newTestConfig(t, pki, dialer, &bytes.Buffer{})
	config.PingInterval = 20 * time.Millisecond
	config.PingTimeout = 10 * time.Millisecond
	config.Reconnect.MaxAttempts = 1
	client, err := NewClient(config, registry)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	err = client.Run(t.Context())
	if !errors.Is(err, ErrReconnectExhausted) {
		t.Fatalf("Run() error = %v, want bounded reconnect exhaustion", err)
	}
}
