package tunnel

import (
	"context"
	"errors"
	"testing"
	"time"

	contractv1 "github.com/v0hmly/marketmesh/api/gen/go/tunnel/v1"
)

func TestLogicalRequest_CreditWakesBoundedSender(t *testing.T) {
	t.Parallel()

	request := testLogicalRequest(t)
	result := make(chan int, 1)
	errResult := make(chan error, 1)
	go func() {
		bytes, err := request.takeSendCredit(context.Background(), 10)
		if err != nil {
			errResult <- err
			return
		}
		result <- bytes
	}()

	frame := requestInboundFrame(
		request,
		1,
		&contractv1.ConnectRequest_Credit{Credit: &contractv1.Credit{Bytes: 3}},
	)
	if err := request.handleInbound(frame); err != nil {
		t.Fatalf("handleInbound(Credit) error = %v", err)
	}
	select {
	case err := <-errResult:
		t.Fatalf("takeSendCredit() error = %v", err)
	case bytes := <-result:
		if bytes != 3 {
			t.Fatalf("takeSendCredit() bytes = %d, want 3", bytes)
		}
	case <-time.After(time.Second):
		t.Fatal("takeSendCredit() did not wake after credit")
	}
}

func TestLogicalRequest_RejectsDataWithoutCredit(t *testing.T) {
	t.Parallel()

	request := testLogicalRequest(t)
	frame := requestInboundFrame(
		request,
		1,
		&contractv1.ConnectRequest_Data{Data: &contractv1.Data{Payload: []byte("x")}},
	)
	if err := request.handleInbound(frame); !errors.Is(err, ErrProtocolViolation) {
		t.Fatalf("handleInbound(Data) error = %v, want ErrProtocolViolation", err)
	}
}

func TestLogicalRequest_RejectsSequenceGap(t *testing.T) {
	t.Parallel()

	request := testLogicalRequest(t)
	frame := requestInboundFrame(
		request,
		2,
		&contractv1.ConnectRequest_Credit{Credit: &contractv1.Credit{Bytes: 1}},
	)
	if err := request.handleInbound(frame); !errors.Is(err, ErrProtocolViolation) {
		t.Fatalf("handleInbound(sequence gap) error = %v, want ErrProtocolViolation", err)
	}
}

func testLogicalRequest(t *testing.T) *logicalRequest {
	t.Helper()
	settings, err := newSettings(testConfig())
	if err != nil {
		t.Fatalf("newSettings() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	activeSession := &session{
		settings: settings,
		limits:   cloneLimits(settings.limits),
		id:       [16]byte{1},
		ctx:      ctx,
		outbound: newOutboundQueue(settings.queues, settings.instrumentation),
	}
	policy := settings.routes[contractv1.RouteId_ROUTE_ID_AUTH_LOGIN]

	return newLogicalRequest(
		activeSession,
		[16]byte{2},
		contractv1.RouteId_ROUTE_ID_AUTH_LOGIN,
		policy,
		time.Now().Add(time.Second),
	)
}

func requestInboundFrame(
	request *logicalRequest,
	sequence uint64,
	payload any,
) *contractv1.ConnectRequest {
	frame := &contractv1.ConnectRequest{
		Header: &contractv1.FrameHeader{
			ProtocolVersion: protocolVersion,
			TunnelId:        request.session.id[:],
			RequestId:       request.id[:],
			Sequence:        sequence,
			TrafficClass:    request.policy.TrafficClass,
		},
	}
	switch typedPayload := payload.(type) {
	case *contractv1.ConnectRequest_Credit:
		frame.Payload = typedPayload
	case *contractv1.ConnectRequest_Data:
		frame.Payload = typedPayload
	}

	return frame
}
