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

func TestLogicalRequest_AcceptsDataHalfCloseResult(t *testing.T) {
	t.Parallel()

	request := testLogicalRequest(t)
	request.receiveCredit = len("response")
	frames := []*contractv1.ConnectRequest{
		requestInboundFrame(
			request,
			1,
			&contractv1.ConnectRequest_Data{
				Data: &contractv1.Data{Payload: []byte("response")},
			},
		),
		requestInboundFrame(
			request,
			2,
			&contractv1.ConnectRequest_HalfClose{HalfClose: &contractv1.HalfClose{}},
		),
		requestInboundFrame(
			request,
			3,
			&contractv1.ConnectRequest_Result{
				Result: &contractv1.Result{Code: contractv1.ResultCode_RESULT_CODE_OK},
			},
		),
	}
	for _, frame := range frames {
		if err := request.handleInbound(frame); err != nil {
			t.Fatalf("handleInbound() error = %v", err)
		}
	}
	response, err := request.outcome()
	if err != nil {
		t.Fatalf("outcome() error = %v", err)
	}
	if string(response.Payload) != "response" {
		t.Fatalf("outcome() payload = %q, want %q", response.Payload, "response")
	}
}

func TestLogicalRequest_DataAndCancelSerializeResponseCredit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		dataFirst bool
		wantTypes []string
	}{
		{
			name:      "Data critical section wins",
			dataFirst: true,
			wantTypes: []string{"credit", "cancel"},
		},
		{
			name:      "Cancel critical section wins",
			dataFirst: false,
			wantTypes: []string{"cancel"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			request := testLogicalRequest(t)
			request.receiveCredit = len("data")
			data := requestInboundFrame(
				request,
				1,
				&contractv1.ConnectRequest_Data{
					Data: &contractv1.Data{Payload: []byte("data")},
				},
			)
			if test.dataFirst {
				if err := request.handleInbound(data); err != nil {
					t.Fatalf("handleInbound(Data) error = %v", err)
				}
				request.cancel(context.Canceled)
			} else {
				request.cancel(context.Canceled)
				if err := request.handleInbound(data); err != nil {
					t.Fatalf("handleInbound(Data tail) error = %v", err)
				}
			}

			frames := queuedRequestFrames(request)
			if len(frames) != len(test.wantTypes) {
				t.Fatalf("queued frames = %d, want %d (%v)", len(frames), len(test.wantTypes), test.wantTypes)
			}
			for index, frame := range frames {
				var frameType string
				switch frame.GetPayload().(type) {
				case *contractv1.ConnectResponse_Credit:
					frameType = "credit"
				case *contractv1.ConnectResponse_Cancel:
					frameType = "cancel"
				default:
					frameType = "unexpected"
				}
				if frameType != test.wantTypes[index] {
					t.Fatalf("queued frame %d = %s, want %s", index, frameType, test.wantTypes[index])
				}
				if frame.GetHeader().GetSequence() != uint64(index+1) {
					t.Fatalf("queued frame %d sequence = %d, want %d", index, frame.GetHeader().GetSequence(), index+1)
				}
			}
		})
	}
}

func TestLogicalRequest_QueuedCancelBlocksConcurrentDataCredit(t *testing.T) {
	t.Parallel()

	request := testLogicalRequest(t)
	request.receiveCredit = len("data")
	cancelEnqueued := make(chan struct{})
	continueCancel := make(chan struct{})
	cancelDone := make(chan struct{})
	go func() {
		request.cancelWithBarrier(context.Canceled, func() {
			close(cancelEnqueued)
			<-continueCancel
		})
		close(cancelDone)
	}()
	select {
	case <-cancelEnqueued:
	case <-time.After(time.Second):
		t.Fatal("Cancel did not reach enqueue barrier")
	}

	dataResult := make(chan error, 1)
	go func() {
		dataResult <- request.handleInbound(requestInboundFrame(
			request,
			1,
			&contractv1.ConnectRequest_Data{
				Data: &contractv1.Data{Payload: []byte("data")},
			},
		))
	}()
	select {
	case err := <-dataResult:
		close(continueCancel)
		<-cancelDone
		t.Fatalf("handleInbound(Data) crossed Cancel terminalization: %v", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(continueCancel)
	select {
	case <-cancelDone:
	case <-time.After(time.Second):
		t.Fatal("Cancel did not complete after barrier release")
	}
	select {
	case err := <-dataResult:
		if err != nil {
			t.Fatalf("handleInbound(Data tail) error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Data tail remained blocked after Cancel terminalization")
	}

	frames := queuedRequestFrames(request)
	if len(frames) != 1 || frames[0].GetCancel() == nil {
		t.Fatalf("queued frames after Cancel/Data race = %d, want only Cancel", len(frames))
	}
}

func TestLogicalRequest_InitialCreditRollbackOnQueueFull(t *testing.T) {
	t.Parallel()

	request := testLogicalRequest(t)
	if err := request.enqueueOpen(t.Context(), Call{Route: request.route}); err != nil {
		t.Fatalf("enqueueOpen() error = %v", err)
	}
	lane, _ := queueLane(request.policy.TrafficClass)
	request.session.outbound.lanes[lane] <- &contractv1.ConnectResponse{}
	if err := request.sendInitialResponseCredit(t.Context()); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("sendInitialResponseCredit() error = %v, want ErrQueueFull", err)
	}
	request.mu.Lock()
	receiveCredit := request.receiveCredit
	request.mu.Unlock()
	if receiveCredit != 0 {
		t.Fatalf("receiveCredit after failed enqueue = %d, want 0", receiveCredit)
	}
	request.completeLocalAbort(ErrQueueFull)
	if err := request.handleInbound(requestInboundFrame(
		request,
		1,
		&contractv1.ConnectRequest_Data{Data: &contractv1.Data{Payload: []byte("unsent")}},
	)); !errors.Is(err, ErrProtocolViolation) {
		t.Fatalf("handleInbound(Data without sent Credit) error = %v, want ErrProtocolViolation", err)
	}
}

func TestLogicalRequest_RejectsInvalidResponseFraming(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		prepare []any
		invalid any
	}{
		{
			name: "premature Result",
			invalid: &contractv1.ConnectRequest_Result{
				Result: &contractv1.Result{Code: contractv1.ResultCode_RESULT_CODE_OK},
			},
		},
		{
			name: "Data after HalfClose",
			prepare: []any{
				&contractv1.ConnectRequest_HalfClose{HalfClose: &contractv1.HalfClose{}},
			},
			invalid: &contractv1.ConnectRequest_Data{
				Data: &contractv1.Data{Payload: []byte("late")},
			},
		},
		{
			name: "duplicate HalfClose",
			prepare: []any{
				&contractv1.ConnectRequest_HalfClose{HalfClose: &contractv1.HalfClose{}},
			},
			invalid: &contractv1.ConnectRequest_HalfClose{HalfClose: &contractv1.HalfClose{}},
		},
		{
			name: "Data after Result",
			prepare: []any{
				&contractv1.ConnectRequest_HalfClose{HalfClose: &contractv1.HalfClose{}},
				&contractv1.ConnectRequest_Result{
					Result: &contractv1.Result{Code: contractv1.ResultCode_RESULT_CODE_OK},
				},
			},
			invalid: &contractv1.ConnectRequest_Data{
				Data: &contractv1.Data{Payload: []byte("late")},
			},
		},
		{
			name: "duplicate Result",
			prepare: []any{
				&contractv1.ConnectRequest_HalfClose{HalfClose: &contractv1.HalfClose{}},
				&contractv1.ConnectRequest_Result{
					Result: &contractv1.Result{Code: contractv1.ResultCode_RESULT_CODE_OK},
				},
			},
			invalid: &contractv1.ConnectRequest_Result{
				Result: &contractv1.Result{Code: contractv1.ResultCode_RESULT_CODE_OK},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			request := testLogicalRequest(t)
			request.receiveCredit = len("late")
			sequence := uint64(1)
			for _, payload := range test.prepare {
				if err := request.handleInbound(requestInboundFrame(request, sequence, payload)); err != nil {
					t.Fatalf("prepare handleInbound() error = %v", err)
				}
				sequence++
			}
			if err := request.handleInbound(
				requestInboundFrame(request, sequence, test.invalid),
			); !errors.Is(err, ErrProtocolViolation) {
				t.Fatalf("handleInbound(invalid framing) error = %v, want ErrProtocolViolation", err)
			}
		})
	}
}

func TestSession_TombstoneAcceptsCanceledResponseTail(t *testing.T) {
	t.Parallel()

	activeSession, request := testSessionRequest(t)
	request.mu.Lock()
	request.receiveCredit = len("late")
	request.mu.Unlock()
	request.cancel(context.Canceled)
	activeSession.finishRequest(request)

	frames := []*contractv1.ConnectRequest{
		requestInboundFrame(
			request,
			1,
			&contractv1.ConnectRequest_Data{
				Data: &contractv1.Data{Payload: []byte("late")},
			},
		),
		requestInboundFrame(
			request,
			2,
			&contractv1.ConnectRequest_HalfClose{HalfClose: &contractv1.HalfClose{}},
		),
		requestInboundFrame(
			request,
			3,
			&contractv1.ConnectRequest_Result{
				Result: &contractv1.Result{Code: contractv1.ResultCode_RESULT_CODE_CANCELED},
			},
		),
	}
	for _, frame := range frames {
		if err := activeSession.handleInbound(frame); err != nil {
			t.Fatalf("handleInbound(canceled tail) error = %v", err)
		}
	}
	if err := activeSession.handleInbound(requestInboundFrame(
		request,
		4,
		&contractv1.ConnectRequest_Result{
			Result: &contractv1.Result{Code: contractv1.ResultCode_RESULT_CODE_CANCELED},
		},
	)); !errors.Is(err, ErrProtocolViolation) {
		t.Fatalf("handleInbound(duplicate tail Result) error = %v, want ErrProtocolViolation", err)
	}
}

func TestSession_QueueFullAfterOpenAcceptsBoundedPeerTail(t *testing.T) {
	t.Parallel()

	activeSession, request := testSessionRequest(t)
	request.mu.Lock()
	request.receiveCredit = len("data")
	request.mu.Unlock()
	lane, _ := queueLane(request.policy.TrafficClass)
	queue := activeSession.outbound.lanes[lane]
	for len(queue) < cap(queue) {
		queue <- &contractv1.ConnectResponse{}
	}
	if err := activeSession.handleInbound(requestInboundFrame(
		request,
		1,
		&contractv1.ConnectRequest_Credit{Credit: &contractv1.Credit{Bytes: 1}},
	)); err != nil {
		t.Fatalf("handleInbound(peer Credit) error = %v", err)
	}
	if err := activeSession.handleInbound(requestInboundFrame(
		request,
		2,
		&contractv1.ConnectRequest_Data{
			Data: &contractv1.Data{Payload: []byte("data")},
		},
	)); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("handleInbound(Data with saturated lane) error = %v, want ErrQueueFull", err)
	}
	request.mu.Lock()
	receiveCredit := request.receiveCredit
	request.mu.Unlock()
	if receiveCredit != 0 {
		t.Fatalf("receiveCredit after replenishment QueueFull = %d, want 0", receiveCredit)
	}
	activeSession.finishRequest(request)

	tail := []*contractv1.ConnectRequest{
		requestInboundFrame(
			request,
			3,
			&contractv1.ConnectRequest_HalfClose{HalfClose: &contractv1.HalfClose{}},
		),
		requestInboundFrame(
			request,
			4,
			&contractv1.ConnectRequest_Result{
				Result: &contractv1.Result{Code: contractv1.ResultCode_RESULT_CODE_RESOURCE_EXHAUSTED},
			},
		),
	}
	for _, frame := range tail {
		if err := activeSession.handleInbound(frame); err != nil {
			t.Fatalf("handleInbound(local-abort tail %T) error = %v", frame.GetPayload(), err)
		}
	}
}

func TestSession_TombstoneRejectsFramesAfterPeerResult(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		payload any
	}{
		{
			name: "Data",
			payload: &contractv1.ConnectRequest_Data{
				Data: &contractv1.Data{Payload: []byte("late")},
			},
		},
		{
			name:    "HalfClose",
			payload: &contractv1.ConnectRequest_HalfClose{HalfClose: &contractv1.HalfClose{}},
		},
		{
			name: "Result",
			payload: &contractv1.ConnectRequest_Result{
				Result: &contractv1.Result{Code: contractv1.ResultCode_RESULT_CODE_OK},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			activeSession, request := testSessionRequest(t)
			if err := request.handleInbound(requestInboundFrame(
				request,
				1,
				&contractv1.ConnectRequest_HalfClose{HalfClose: &contractv1.HalfClose{}},
			)); err != nil {
				t.Fatalf("handleInbound(HalfClose) error = %v", err)
			}
			if err := request.handleInbound(requestInboundFrame(
				request,
				2,
				&contractv1.ConnectRequest_Result{
					Result: &contractv1.Result{Code: contractv1.ResultCode_RESULT_CODE_OK},
				},
			)); err != nil {
				t.Fatalf("handleInbound(Result) error = %v", err)
			}
			activeSession.finishRequest(request)

			if err := activeSession.handleInbound(
				requestInboundFrame(request, 3, test.payload),
			); !errors.Is(err, ErrProtocolViolation) {
				t.Fatalf("handleInbound(%s after Result) error = %v, want ErrProtocolViolation", test.name, err)
			}
		})
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

func testSessionRequest(t *testing.T) (*session, *logicalRequest) {
	t.Helper()
	settings, err := newSettings(testConfig())
	if err != nil {
		t.Fatalf("newSettings() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	registry := newRegistry(settings)
	activeSession := &session{
		settings:    settings,
		registry:    registry,
		limits:      cloneLimits(settings.limits),
		id:          [16]byte{1},
		instanceID:  [16]byte{3},
		dataCenter:  "dc-test",
		ctx:         ctx,
		outbound:    newOutboundQueue(settings.queues, settings.instrumentation),
		requests:    map[[16]byte]*logicalRequest{},
		routeActive: map[contractv1.RouteId]int{},
		tombstones:  map[[16]byte]*logicalRequest{},
		tombstoneOrder: make(
			[][16]byte,
			0,
			settings.limits.GetMaxInFlightRequests(),
		),
	}
	policy := settings.routes[contractv1.RouteId_ROUTE_ID_AUTH_LOGIN]
	request := newLogicalRequest(
		activeSession,
		[16]byte{2},
		contractv1.RouteId_ROUTE_ID_AUTH_LOGIN,
		policy,
		time.Now().Add(time.Second),
	)
	activeSession.requests[request.id] = request
	activeSession.routeActive[request.route] = 1
	registry.instanceActive[activeSession.instanceID] = 1

	return activeSession, request
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
	case *contractv1.ConnectRequest_HalfClose:
		frame.Payload = typedPayload
	case *contractv1.ConnectRequest_Cancel:
		frame.Payload = typedPayload
	case *contractv1.ConnectRequest_Result:
		frame.Payload = typedPayload
	}

	return frame
}

func queuedRequestFrames(request *logicalRequest) []*contractv1.ConnectResponse {
	lane, _ := queueLane(request.policy.TrafficClass)
	queue := request.session.outbound.lanes[lane]
	frames := make([]*contractv1.ConnectResponse, 0, len(queue))
	for len(queue) > 0 {
		frames = append(frames, <-queue)
	}

	return frames
}
