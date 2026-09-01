package tunnel

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	contractv1 "github.com/v0hmly/marketmesh/api/gen/go/tunnel/v1"
	protocolv1 "github.com/v0hmly/marketmesh/api/tunnel/v1"
	grpcgo "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestClientRelaysAllowlistedUnaryRPC(t *testing.T) {
	t.Parallel()

	pki := newTestPKI(t)
	internal := newFakeHealth()
	clients, _ := startInternalHealth(t, internal)
	registry := newTestRegistry(t, clients, contractv1.RouteId_ROUTE_ID_AUTH_LOGIN)
	result := make(chan relayResult, 1)
	requestID := bytes.Repeat([]byte{0x11}, protocolv1.RequestIDBytes)
	requestBytes, err := proto.Marshal(&grpc_health_v1.HealthCheckRequest{Service: testSensitiveBody})
	if err != nil {
		t.Fatalf("marshal internal request: %v", err)
	}

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
				IdempotencyKey: []byte("idempotency-key"),
				Metadata: []*contractv1.Metadata{
					{
						Key:   contractv1.MetadataKey_METADATA_KEY_CONTENT_TYPE,
						Value: []byte("application/protobuf"),
					},
					{
						Key: contractv1.MetadataKey_METADATA_KEY_TRACEPARENT,
						Value: []byte(
							"00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01",
						),
					},
					{
						Key:   contractv1.MetadataKey_METADATA_KEY_SESSION_ASSERTION,
						Value: []byte(testSensitiveToken),
					},
				},
			}},
		)); err != nil {
			return err
		}
		credit, err := recvApplicationFrame(stream)
		if err != nil || credit.GetCredit() == nil {
			return errors.New("gateway-out did not grant request credit")
		}
		if err := stream.Send(testGatewayInFrame(
			requestID,
			contractv1.TrafficClass_TRAFFIC_CLASS_CONTROL_AUTH,
			2,
			&contractv1.ConnectResponse_Data{Data: &contractv1.Data{Payload: requestBytes}},
		)); err != nil {
			return err
		}
		if err := stream.Send(testGatewayInFrame(
			requestID,
			contractv1.TrafficClass_TRAFFIC_CLASS_CONTROL_AUTH,
			3,
			&contractv1.ConnectResponse_HalfClose{HalfClose: &contractv1.HalfClose{}},
		)); err != nil {
			return err
		}
		if err := stream.Send(testGatewayInFrame(
			requestID,
			contractv1.TrafficClass_TRAFFIC_CLASS_CONTROL_AUTH,
			4,
			&contractv1.ConnectResponse_Credit{Credit: &contractv1.Credit{Bytes: 4 << 10}},
		)); err != nil {
			return err
		}

		var response []byte
		for {
			frame, err := recvApplicationFrame(stream)
			if err != nil {
				return err
			}
			if data := frame.GetData(); data != nil {
				response = append(response, data.GetPayload()...)
				continue
			}
			if tunnelResult := frame.GetResult(); tunnelResult != nil {
				result <- relayResult{
					code:     tunnelResult.GetCode(),
					metadata: tunnelResult.GetMetadata(),
					payload:  response,
				}
				<-stream.Context().Done()
				return nil
			}
		}
	}}
	dialer := startGatewayIn(t, pki, gateway)
	logs := &bytes.Buffer{}
	config := newTestConfig(t, pki, dialer, logs)
	client, err := NewClient(config, registry)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	runCtx, cancelRun := context.WithCancel(t.Context())
	runResult := make(chan error, 1)
	go func() { runResult <- client.Run(runCtx) }()

	select {
	case received := <-result:
		if received.code != contractv1.ResultCode_RESULT_CODE_OK {
			t.Fatalf("Result code = %s, want OK", received.code)
		}
		if len(received.metadata) != 0 {
			t.Fatalf("Result metadata = %v, want internal metadata dropped", received.metadata)
		}
		response := &grpc_health_v1.HealthCheckResponse{}
		if err := proto.Unmarshal(received.payload, response); err != nil {
			t.Fatalf("unmarshal internal response: %v", err)
		}
		if response.GetStatus() != grpc_health_v1.HealthCheckResponse_SERVING {
			t.Fatalf("internal response status = %s", response.GetStatus())
		}
	case <-time.After(2 * time.Second):
		t.Fatal("tunnel relay did not complete")
	}

	internal.metadataMu.Lock()
	incomingMetadata := internal.metadata.Copy()
	internalDeadline := internal.deadline
	internal.metadataMu.Unlock()
	if values := incomingMetadata.Get(internalSessionAssertionMetadata); len(values) != 1 ||
		values[0] != testSensitiveToken {
		t.Fatalf("internal session assertion metadata = %v", values)
	}
	if values := incomingMetadata.Get("authorization"); len(values) != 0 {
		t.Fatalf("internal authorization metadata = %v, want dropped", values)
	}
	if values := incomingMetadata.Get("traceparent"); len(values) != 1 {
		t.Fatalf("internal traceparent metadata = %v", values)
	}
	if internalDeadline.IsZero() || time.Until(internalDeadline) > time.Second {
		t.Fatalf("internal deadline = %v, want propagated bounded deadline", internalDeadline)
	}

	cancelRun()
	select {
	case err := <-runResult:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run() did not stop after cancellation")
	}

	for _, sensitive := range []string{
		testSensitiveBody,
		testSensitiveToken,
		"private-dsn-and-stack-must-not-leak",
	} {
		if strings.Contains(logs.String(), sensitive) {
			t.Fatalf("gateway-out logs contain sensitive value %q: %s", sensitive, logs.String())
		}
	}
}

func TestCancelReleasesInternalRPC(t *testing.T) {
	t.Parallel()

	pki := newTestPKI(t)
	internal := newFakeHealth()
	internal.block.Store(true)
	clients, _ := startInternalHealth(t, internal)
	registry := newTestRegistry(t, clients, contractv1.RouteId_ROUTE_ID_USER_GET_ME)
	requestID := bytes.Repeat([]byte{0x22}, protocolv1.RequestIDBytes)
	requestBytes, err := proto.Marshal(&grpc_health_v1.HealthCheckRequest{Service: "blocked"})
	if err != nil {
		t.Fatalf("marshal internal request: %v", err)
	}
	result := make(chan contractv1.ResultCode, 1)

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
				Deadline: timestamppb.New(time.Now().Add(time.Second)),
			}},
		)); err != nil {
			return err
		}
		if _, err := recvApplicationFrame(stream); err != nil {
			return err
		}
		if err := stream.Send(testGatewayInFrame(
			requestID,
			contractv1.TrafficClass_TRAFFIC_CLASS_REGULAR,
			2,
			&contractv1.ConnectResponse_Data{Data: &contractv1.Data{Payload: requestBytes}},
		)); err != nil {
			return err
		}
		if err := stream.Send(testGatewayInFrame(
			requestID,
			contractv1.TrafficClass_TRAFFIC_CLASS_REGULAR,
			3,
			&contractv1.ConnectResponse_HalfClose{HalfClose: &contractv1.HalfClose{}},
		)); err != nil {
			return err
		}
		select {
		case <-internal.started:
		case <-time.After(time.Second):
			return errors.New("internal RPC did not start")
		}
		if err := stream.Send(testGatewayInFrame(
			requestID,
			contractv1.TrafficClass_TRAFFIC_CLASS_REGULAR,
			4,
			&contractv1.ConnectResponse_Cancel{Cancel: &contractv1.Cancel{
				Reason: contractv1.CancelReason_CANCEL_REASON_CALLER,
			}},
		)); err != nil {
			return err
		}
		for {
			frame, err := recvApplicationFrame(stream)
			if err != nil {
				return err
			}
			if frame.GetResult() != nil {
				result <- frame.GetResult().GetCode()
				<-stream.Context().Done()
				return nil
			}
		}
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
		if code != contractv1.ResultCode_RESULT_CODE_CANCELED {
			t.Fatalf("Result code = %s, want CANCELED", code)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("canceled request did not finish")
	}
	select {
	case <-internal.canceled:
	case <-time.After(time.Second):
		t.Fatal("internal RPC context was not canceled")
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

func TestDisconnectDoesNotRetryMutatingRequest(t *testing.T) {
	t.Parallel()

	pki := newTestPKI(t)
	internal := newFakeHealth()
	internal.block.Store(true)
	clients, _ := startInternalHealth(t, internal)
	registry := newTestRegistry(t, clients, contractv1.RouteId_ROUTE_ID_AUTH_LOGIN)
	requestID := bytes.Repeat([]byte{0x33}, protocolv1.RequestIDBytes)
	requestBytes, err := proto.Marshal(&grpc_health_v1.HealthCheckRequest{Service: "mutating"})
	if err != nil {
		t.Fatalf("marshal internal request: %v", err)
	}
	var connections atomic.Int64
	reconnected := make(chan struct{}, 1)

	gateway := &fakeGatewayIn{connect: func(
		stream grpcgo.BidiStreamingServer[contractv1.ConnectRequest, contractv1.ConnectResponse],
	) error {
		attempt := connections.Add(1)
		if _, err := acceptTestHello(stream); err != nil {
			return err
		}
		if attempt > 1 {
			notify(reconnected)
			<-stream.Context().Done()
			return nil
		}
		if err := stream.Send(testGatewayInFrame(
			requestID,
			contractv1.TrafficClass_TRAFFIC_CLASS_CONTROL_AUTH,
			1,
			&contractv1.ConnectResponse_Open{Open: &contractv1.Open{
				RouteId:        contractv1.RouteId_ROUTE_ID_AUTH_LOGIN,
				Deadline:       timestamppb.New(time.Now().Add(time.Second)),
				IdempotencyKey: []byte("mutating-key"),
			}},
		)); err != nil {
			return err
		}
		if _, err := recvApplicationFrame(stream); err != nil {
			return err
		}
		if err := stream.Send(testGatewayInFrame(
			requestID,
			contractv1.TrafficClass_TRAFFIC_CLASS_CONTROL_AUTH,
			2,
			&contractv1.ConnectResponse_Data{Data: &contractv1.Data{Payload: requestBytes}},
		)); err != nil {
			return err
		}
		if err := stream.Send(testGatewayInFrame(
			requestID,
			contractv1.TrafficClass_TRAFFIC_CLASS_CONTROL_AUTH,
			3,
			&contractv1.ConnectResponse_HalfClose{HalfClose: &contractv1.HalfClose{}},
		)); err != nil {
			return err
		}
		select {
		case <-internal.started:
			return statusUnavailable()
		case <-time.After(time.Second):
			return errors.New("internal mutating RPC did not start")
		}
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
	case <-reconnected:
	case <-time.After(2 * time.Second):
		t.Fatal("client did not reconnect")
	}
	if calls := internal.callCount.Load(); calls != 1 {
		t.Fatalf("mutating internal RPC calls = %d, want exactly 1", calls)
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

func TestShutdownSendsDrainAndCancelsAtBound(t *testing.T) {
	t.Parallel()

	pki := newTestPKI(t)
	internal := newFakeHealth()
	internal.block.Store(true)
	clients, _ := startInternalHealth(t, internal)
	registry := newTestRegistry(t, clients, contractv1.RouteId_ROUTE_ID_USER_GET_ME)
	requestID := bytes.Repeat([]byte{0x44}, protocolv1.RequestIDBytes)
	requestBytes, err := proto.Marshal(&grpc_health_v1.HealthCheckRequest{Service: "drain"})
	if err != nil {
		t.Fatalf("marshal internal request: %v", err)
	}
	drainReceived := make(chan time.Time, 1)

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
				Deadline: timestamppb.New(time.Now().Add(time.Second)),
			}},
		)); err != nil {
			return err
		}
		if _, err := recvApplicationFrame(stream); err != nil {
			return err
		}
		if err := stream.Send(testGatewayInFrame(
			requestID,
			contractv1.TrafficClass_TRAFFIC_CLASS_REGULAR,
			2,
			&contractv1.ConnectResponse_Data{Data: &contractv1.Data{Payload: requestBytes}},
		)); err != nil {
			return err
		}
		if err := stream.Send(testGatewayInFrame(
			requestID,
			contractv1.TrafficClass_TRAFFIC_CLASS_REGULAR,
			3,
			&contractv1.ConnectResponse_HalfClose{HalfClose: &contractv1.HalfClose{}},
		)); err != nil {
			return err
		}
		for {
			frame, err := recvApplicationFrame(stream)
			if err != nil {
				return err
			}
			if drain := frame.GetDrain(); drain != nil {
				drainReceived <- drain.GetDeadline().AsTime()
				<-stream.Context().Done()
				return nil
			}
		}
	}}
	dialer := startGatewayIn(t, pki, gateway)
	config := newTestConfig(t, pki, dialer, &bytes.Buffer{})
	config.DrainTimeout = 50 * time.Millisecond
	client, err := NewClient(config, registry)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}
	runResult := make(chan error, 1)
	go func() { runResult <- client.Run(t.Context()) }()
	select {
	case <-internal.started:
	case <-time.After(time.Second):
		t.Fatal("internal RPC did not start")
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancelShutdown()
	if err := client.Shutdown(shutdownCtx); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	select {
	case deadline := <-drainReceived:
		if time.Until(deadline) > 100*time.Millisecond {
			t.Fatalf("Drain deadline = %v, want configured bound", deadline)
		}
	case <-time.After(time.Second):
		t.Fatal("gateway-in did not receive Drain")
	}
	select {
	case <-internal.canceled:
	case <-time.After(time.Second):
		t.Fatal("Drain deadline did not cancel internal RPC")
	}
	select {
	case err := <-runResult:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Run() did not stop after Shutdown")
	}
}

func TestServerIdentityMismatchFailsBeforeConnectRPC(t *testing.T) {
	t.Parallel()

	pki := newTestPKI(t)
	var connects atomic.Int64
	gateway := &fakeGatewayIn{connect: func(
		grpcgo.BidiStreamingServer[contractv1.ConnectRequest, contractv1.ConnectResponse],
	) error {
		connects.Add(1)
		return nil
	}}
	dialer := startGatewayIn(t, pki, gateway)
	internal := newFakeHealth()
	clients, _ := startInternalHealth(t, internal)
	registry := newTestRegistry(t, clients, contractv1.RouteId_ROUTE_ID_USER_GET_ME)
	config := newTestConfig(t, pki, dialer, &bytes.Buffer{})
	config.ExpectedServerIdentity = "spiffe://marketmesh.test/dmz/not-gateway-in"
	config.Reconnect.MaxAttempts = 1
	client, err := NewClient(config, registry)
	if err != nil {
		t.Fatalf("NewClient() error = %v", err)
	}

	err = client.Run(t.Context())
	if !errors.Is(err, ErrReconnectExhausted) {
		t.Fatalf("Run() error = %v, want ErrReconnectExhausted", err)
	}
	if connects.Load() != 0 {
		t.Fatalf("Connect RPC calls = %d, want 0 before server identity verification", connects.Load())
	}
}

type relayResult struct {
	code     contractv1.ResultCode
	metadata []*contractv1.Metadata
	payload  []byte
}

func statusUnavailable() error {
	return status.Error(codes.Unavailable, "test disconnect")
}
