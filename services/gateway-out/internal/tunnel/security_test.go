package tunnel

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	contractv1 "github.com/v0hmly/marketmesh/api/gen/go/tunnel/v1"
	protocolv1 "github.com/v0hmly/marketmesh/api/tunnel/v1"
	grpcgo "google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestUnconfiguredRouteIsRejectedWithoutInternalCall(t *testing.T) {
	t.Parallel()

	pki := newTestPKI(t)
	internal := newFakeHealth()
	clients, _ := startInternalHealth(t, internal)
	registry := newTestRegistry(t, clients, contractv1.RouteId_ROUTE_ID_USER_GET_ME)
	result := make(chan contractv1.ResultCode, 1)
	requestID := bytes.Repeat([]byte{0x61}, protocolv1.RequestIDBytes)
	gateway := &fakeGatewayIn{connect: func(
		stream grpcgo.BidiStreamingServer[contractv1.ConnectRequest, contractv1.ConnectResponse],
	) error {
		if _, err := acceptTestHello(stream); err != nil {
			return err
		}
		if err := stream.Send(testGatewayInFrame(
			requestID,
			contractv1.TrafficClass_TRAFFIC_CLASS_CONTROL_AUTH,
			1,
			&contractv1.ConnectResponse_Open{Open: &contractv1.Open{
				RouteId:        contractv1.RouteId_ROUTE_ID_AUTH_LOGIN,
				Deadline:       timestamppb.New(time.Now().Add(500 * time.Millisecond)),
				IdempotencyKey: []byte("unconfigured"),
			}},
		)); err != nil {
			return err
		}
		frame, err := recvApplicationFrame(stream)
		if err != nil {
			return err
		}
		if frame.GetResult() == nil {
			return errors.New("gateway-out did not return a safe result")
		}
		result <- frame.GetResult().GetCode()
		<-stream.Context().Done()
		return nil
	}}
	dialer := startGatewayIn(t, pki, gateway)
	config := newTestConfig(t, pki, dialer, &bytes.Buffer{})
	client, err := NewClient(config, registry)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	runCtx, cancelRun := context.WithCancel(t.Context())
	runResult := make(chan error, 1)
	go func() { runResult <- client.Run(runCtx) }()

	select {
	case code := <-result:
		if code != contractv1.ResultCode_RESULT_CODE_PERMISSION_DENIED {
			t.Fatalf("unconfigured route result = %s, want PERMISSION_DENIED", code)
		}
	case <-time.After(time.Second):
		t.Fatal("unconfigured route was not rejected")
	}
	if calls := internal.callCount.Load(); calls != 0 {
		t.Fatalf("internal calls = %d, want 0 for unconfigured route", calls)
	}
	cancelRun()
	if err := <-runResult; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}

func TestReceiveCreditViolationClosesTunnelWithoutInternalCall(t *testing.T) {
	t.Parallel()

	pki := newTestPKI(t)
	internal := newFakeHealth()
	clients, _ := startInternalHealth(t, internal)
	registry := newTestRegistry(t, clients, contractv1.RouteId_ROUTE_ID_USER_GET_ME)
	requestID := bytes.Repeat([]byte{0x62}, protocolv1.RequestIDBytes)
	gateway := &fakeGatewayIn{connect: func(
		stream grpcgo.BidiStreamingServer[contractv1.ConnectRequest, contractv1.ConnectResponse],
	) error {
		if _, err := acceptTestHello(stream); err != nil {
			return err
		}
		if err := stream.Send(testGatewayInFrame(
			requestID,
			contractv1.TrafficClass_TRAFFIC_CLASS_REGULAR,
			1,
			&contractv1.ConnectResponse_Open{Open: &contractv1.Open{
				RouteId:  contractv1.RouteId_ROUTE_ID_USER_GET_ME,
				Deadline: timestamppb.New(time.Now().Add(500 * time.Millisecond)),
			}},
		)); err != nil {
			return err
		}
		credit, err := recvApplicationFrame(stream)
		if err != nil {
			return err
		}
		violation := make([]byte, int(credit.GetCredit().GetBytes())+1)
		if err := stream.Send(testGatewayInFrame(
			requestID,
			contractv1.TrafficClass_TRAFFIC_CLASS_REGULAR,
			2,
			&contractv1.ConnectResponse_Data{Data: &contractv1.Data{Payload: violation}},
		)); err != nil {
			return err
		}
		_, err = stream.Recv()
		return err
	}}
	dialer := startGatewayIn(t, pki, gateway)
	config := newTestConfig(t, pki, dialer, &bytes.Buffer{})
	config.ClassLimits[contractv1.TrafficClass_TRAFFIC_CLASS_REGULAR] = ClassLimits{
		MaxInFlight:        1,
		SendQueueDepth:     2,
		ReceiveWindowBytes: 16,
	}
	config.Reconnect.MaxAttempts = 1
	client, err := NewClient(config, registry)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	err = client.Run(t.Context())
	if !errors.Is(err, ErrReconnectExhausted) {
		t.Fatalf("Run() error = %v, want exhausted after protocol violation", err)
	}
	if calls := internal.callCount.Load(); calls != 0 {
		t.Fatalf("internal calls = %d, want 0 after credit violation", calls)
	}
}
