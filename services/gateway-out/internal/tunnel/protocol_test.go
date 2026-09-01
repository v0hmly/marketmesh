package tunnel

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	contractv1 "github.com/v0hmly/marketmesh/api/gen/go/tunnel/v1"
	protocolv1 "github.com/v0hmly/marketmesh/api/tunnel/v1"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestStrictCodecRejectsMultipleWirePayloads(t *testing.T) {
	t.Parallel()

	requestID := bytes.Repeat([]byte{1}, protocolv1.RequestIDBytes)
	encoded, err := proto.Marshal(testGatewayInFrame(
		requestID,
		contractv1.TrafficClass_TRAFFIC_CLASS_REGULAR,
		1,
		&contractv1.ConnectResponse_Open{Open: &contractv1.Open{
			RouteId:  contractv1.RouteId_ROUTE_ID_USER_GET_ME,
			Deadline: validFutureTimestamp(),
		}},
	))
	if err != nil {
		t.Fatalf("marshal valid frame: %v", err)
	}
	secondPayload, err := proto.Marshal(&contractv1.Data{Payload: []byte("second")})
	if err != nil {
		t.Fatalf("marshal second payload: %v", err)
	}
	encoded = protowire.AppendTag(encoded, 12, protowire.BytesType)
	encoded = protowire.AppendBytes(encoded, secondPayload)

	target := &contractv1.ConnectResponse{}
	err = (strictCodec{}).Unmarshal(encoded, target)
	if !errors.Is(err, protocolv1.ErrUnknownFrameType) {
		t.Fatalf("strictCodec.Unmarshal() error = %v, want ErrUnknownFrameType", err)
	}
}

func TestStrictCodecRejectsUnknownMetadata(t *testing.T) {
	t.Parallel()

	frame := testGatewayInFrame(
		bytes.Repeat([]byte{2}, protocolv1.RequestIDBytes),
		contractv1.TrafficClass_TRAFFIC_CLASS_REGULAR,
		1,
		&contractv1.ConnectResponse_Open{Open: &contractv1.Open{
			RouteId:  contractv1.RouteId_ROUTE_ID_USER_GET_ME,
			Deadline: validFutureTimestamp(),
			Metadata: []*contractv1.Metadata{{
				Key:   contractv1.MetadataKey(999),
				Value: []byte("not-forwarded"),
			}},
		}},
	)
	encoded, err := proto.Marshal(frame)
	if err != nil {
		t.Fatalf("marshal frame with unknown metadata: %v", err)
	}

	err = (strictCodec{}).Unmarshal(encoded, &contractv1.ConnectResponse{})
	if !errors.Is(err, protocolv1.ErrInvalidFrame) {
		t.Fatalf("strictCodec.Unmarshal() error = %v, want ErrInvalidFrame", err)
	}
}

func TestRealtimeQueueOverflowDoesNotBlockControl(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	session := &session{
		ctx:           ctx,
		cancel:        cancel,
		tunnelID:      bytes.Repeat([]byte{0x42}, protocolv1.TunnelIDBytes),
		controlQueue:  make(chan queuedFrame, 1),
		regularQueue:  make(chan queuedFrame, 1),
		realtimeQueue: make(chan queuedFrame, 1),
	}
	realtime := validGatewayOutDataFrame(
		session.tunnelID,
		bytes.Repeat([]byte{0x51}, protocolv1.RequestIDBytes),
		contractv1.TrafficClass_TRAFFIC_CLASS_REALTIME,
	)
	if err := session.enqueue(ctx, contractv1.TrafficClass_TRAFFIC_CLASS_REALTIME, queuedFrame{
		frame: realtime,
	}); err != nil {
		t.Fatalf("first realtime enqueue error = %v", err)
	}
	if err := session.enqueue(ctx, contractv1.TrafficClass_TRAFFIC_CLASS_REALTIME, queuedFrame{
		frame: realtime,
	}); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("second realtime enqueue error = %v, want ErrQueueFull", err)
	}

	control := &contractv1.ConnectRequest{
		Header: &contractv1.FrameHeader{
			ProtocolVersion: protocolVersion,
			TunnelId:        bytes.Clone(session.tunnelID),
		},
		Payload: &contractv1.ConnectRequest_Ping{Ping: &contractv1.Ping{Nonce: 1}},
	}
	if err := session.enqueue(
		ctx,
		contractv1.TrafficClass_TRAFFIC_CLASS_CONTROL_AUTH,
		queuedFrame{frame: control},
	); err != nil {
		t.Fatalf("control enqueue after realtime saturation error = %v", err)
	}
}

func TestTerminalResponseBatchFitsSingleQueueSlot(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	tunnelID := bytes.Repeat([]byte{0x42}, protocolv1.TunnelIDBytes)
	requestID := bytes.Repeat([]byte{0x53}, protocolv1.RequestIDBytes)
	limits := testProtocolLimits()
	activeSession := &session{
		ctx:           ctx,
		tunnelID:      tunnelID,
		limits:        limits,
		controlQueue:  make(chan queuedFrame, 1),
		regularQueue:  make(chan queuedFrame, 1),
		realtimeQueue: make(chan queuedFrame, 1),
	}
	request := &requestState{
		requestID: requestID,
		route: route{RouteSpec: RouteSpec{
			TrafficClass:     contractv1.TrafficClass_TRAFFIC_CLASS_REGULAR,
			MaxResponseBytes: limits.GetMaxMessageBytes(),
		}},
	}

	err := activeSession.enqueueRequestFrames(
		request,
		&contractv1.ConnectRequest_HalfClose{HalfClose: &contractv1.HalfClose{}},
		&contractv1.ConnectRequest_Result{
			Result: &contractv1.Result{Code: contractv1.ResultCode_RESULT_CODE_OK},
		},
	)
	if err != nil {
		t.Fatalf("enqueueRequestFrames() error = %v", err)
	}
	if len(activeSession.regularQueue) != 1 {
		t.Fatalf("regular queue items = %d, want one atomic terminal batch", len(activeSession.regularQueue))
	}
	queued := <-activeSession.regularQueue
	if queued.frame.GetHalfClose() == nil || queued.frame.GetHeader().GetSequence() != 1 {
		t.Fatal("terminal batch does not start with sequence 1 HalfClose")
	}
	if len(queued.following) != 1 || queued.following[0].GetResult() == nil ||
		queued.following[0].GetHeader().GetSequence() != 2 {
		t.Fatal("terminal batch does not end with sequence 2 Result")
	}
}

func TestRejectedOpenBatchFitsSingleQueueSlot(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	requestID := bytes.Repeat([]byte{0x55}, protocolv1.RequestIDBytes)
	activeSession := &session{
		ctx:           ctx,
		tunnelID:      bytes.Repeat([]byte{0x42}, protocolv1.TunnelIDBytes),
		limits:        testProtocolLimits(),
		controlQueue:  make(chan queuedFrame, 1),
		regularQueue:  make(chan queuedFrame, 1),
		realtimeQueue: make(chan queuedFrame, 1),
		terminal:      map[requestKey]*terminalRequest{},
	}
	header := &contractv1.FrameHeader{
		ProtocolVersion: protocolVersion,
		TunnelId:        bytes.Clone(activeSession.tunnelID),
		RequestId:       requestID,
		Sequence:        1,
		TrafficClass:    contractv1.TrafficClass_TRAFFIC_CLASS_REGULAR,
	}
	if err := activeSession.rejectOpen(
		header,
		contractv1.ResultCode_RESULT_CODE_PERMISSION_DENIED,
	); err != nil {
		t.Fatalf("rejectOpen() error = %v", err)
	}
	if len(activeSession.regularQueue) != 1 {
		t.Fatalf("regular queue items = %d, want one atomic rejection batch", len(activeSession.regularQueue))
	}
	queued := <-activeSession.regularQueue
	if queued.frame.GetHalfClose() == nil || len(queued.following) != 1 ||
		queued.following[0].GetResult().GetCode() != contractv1.ResultCode_RESULT_CODE_PERMISSION_DENIED {
		t.Fatal("rejected Open batch is not HalfClose followed by safe Result")
	}
}

func TestTerminalRequestAcceptsBoundedLateCreditAndCancel(t *testing.T) {
	t.Parallel()

	requestID := bytes.Repeat([]byte{0x54}, protocolv1.RequestIDBytes)
	key := requestKey{}
	copy(key[:], requestID)
	activeSession := &session{
		tunnelID: bytes.Repeat([]byte{0x42}, protocolv1.TunnelIDBytes),
		limits:   testProtocolLimits(),
		terminal: map[requestKey]*terminalRequest{
			key: {
				class:            contractv1.TrafficClass_TRAFFIC_CLASS_REGULAR,
				incomingSequence: 3,
				maximumCredit:    64,
			},
		},
		requests: map[requestKey]*requestState{},
	}
	frames := []*contractv1.ConnectResponse{
		testGatewayInFrame(
			requestID,
			contractv1.TrafficClass_TRAFFIC_CLASS_REGULAR,
			4,
			&contractv1.ConnectResponse_Credit{Credit: &contractv1.Credit{Bytes: 32}},
		),
		testGatewayInFrame(
			requestID,
			contractv1.TrafficClass_TRAFFIC_CLASS_REGULAR,
			5,
			&contractv1.ConnectResponse_Cancel{
				Cancel: &contractv1.Cancel{Reason: contractv1.CancelReason_CANCEL_REASON_CALLER},
			},
		),
	}
	for _, frame := range frames {
		if err := activeSession.handleFrame(frame); err != nil {
			t.Fatalf("handleFrame(late %T) error = %v", frame.GetPayload(), err)
		}
	}
	duplicateCancel := testGatewayInFrame(
		requestID,
		contractv1.TrafficClass_TRAFFIC_CLASS_REGULAR,
		6,
		&contractv1.ConnectResponse_Cancel{
			Cancel: &contractv1.Cancel{Reason: contractv1.CancelReason_CANCEL_REASON_CALLER},
		},
	)
	if err := activeSession.handleFrame(duplicateCancel); !errors.Is(err, errProtocol) {
		t.Fatalf("handleFrame(duplicate late Cancel) error = %v, want errProtocol", err)
	}
}

func TestActiveTerminalTransitionKeepsFrameAccountingAtomic(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		update     func(*requestState)
		assertLate func(*testing.T, *session, []byte)
	}{
		{
			name: "Credit",
			update: func(request *requestState) {
				request.sendCredit += 32
			},
			assertLate: func(t *testing.T, activeSession *session, requestID []byte) {
				t.Helper()
				lateCredit := testGatewayInFrame(
					requestID,
					contractv1.TrafficClass_TRAFFIC_CLASS_REGULAR,
					5,
					&contractv1.ConnectResponse_Credit{
						Credit: &contractv1.Credit{Bytes: 17},
					},
				)
				if err := activeSession.handleFrame(lateCredit); !errors.Is(err, errProtocol) {
					t.Fatalf("handleFrame(over-limit late Credit) error = %v, want errProtocol", err)
				}
			},
		},
		{
			name: "Cancel",
			update: func(request *requestState) {
				request.peerCanceled = true
			},
			assertLate: func(t *testing.T, activeSession *session, requestID []byte) {
				t.Helper()
				duplicateCancel := testGatewayInFrame(
					requestID,
					contractv1.TrafficClass_TRAFFIC_CLASS_REGULAR,
					5,
					&contractv1.ConnectResponse_Cancel{
						Cancel: &contractv1.Cancel{Reason: contractv1.CancelReason_CANCEL_REASON_CALLER},
					},
				)
				if err := activeSession.handleFrame(duplicateCancel); !errors.Is(err, errProtocol) {
					t.Fatalf("handleFrame(duplicate late Cancel) error = %v, want errProtocol", err)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			activeSession, request, requestID := testTransitionSession(t)
			entered := make(chan struct{})
			continueUpdate := make(chan struct{})
			updateResult := make(chan error, 1)
			go func() {
				_, err := activeSession.updateRequestForFrame(
					testGatewayInFrame(
						requestID,
						contractv1.TrafficClass_TRAFFIC_CLASS_REGULAR,
						4,
						&contractv1.ConnectResponse_Credit{
							Credit: &contractv1.Credit{Bytes: 1},
						},
					).GetHeader(),
					func(request *requestState) error {
						close(entered)
						<-continueUpdate
						test.update(request)
						return nil
					},
				)
				updateResult <- err
			}()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("frame update did not reach transition barrier")
			}
			releaseStarted := make(chan struct{})
			releaseDone := make(chan struct{})
			go func() {
				close(releaseStarted)
				activeSession.release(request)
				close(releaseDone)
			}()
			<-releaseStarted
			select {
			case <-releaseDone:
				close(continueUpdate)
				<-updateResult
				t.Fatal("release() crossed an in-progress frame update")
			case <-time.After(20 * time.Millisecond):
			}
			close(continueUpdate)
			select {
			case err := <-updateResult:
				if err != nil {
					t.Fatalf("updateRequestForFrame() error = %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("frame update did not complete after barrier release")
			}
			select {
			case <-releaseDone:
			case <-time.After(time.Second):
				t.Fatal("release() remained blocked after frame update")
			}

			test.assertLate(t, activeSession, requestID)
		})
	}
}

func testTransitionSession(t *testing.T) (*session, *requestState, []byte) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	requestID := bytes.Repeat([]byte{0x56}, protocolv1.RequestIDBytes)
	key := requestKey{}
	copy(key[:], requestID)
	limits := testProtocolLimits()
	request := &requestState{
		key:              key,
		requestID:        requestID,
		ctx:              ctx,
		cancel:           cancel,
		done:             make(chan struct{}),
		incomingSequence: 3,
		sendCredit:       16,
		creditChanged:    make(chan struct{}, 1),
		route: route{RouteSpec: RouteSpec{
			TrafficClass:     contractv1.TrafficClass_TRAFFIC_CLASS_REGULAR,
			MaxResponseBytes: limits.GetMaxMessageBytes(),
		}},
	}
	activeSession := &session{
		ctx:           ctx,
		tunnelID:      bytes.Repeat([]byte{0x42}, protocolv1.TunnelIDBytes),
		limits:        limits,
		requests:      map[requestKey]*requestState{key: request},
		terminal:      map[requestKey]*terminalRequest{},
		activeByClass: map[contractv1.TrafficClass]uint32{request.route.TrafficClass: 1},
		activeChanged: make(chan struct{}, 1),
	}

	return activeSession, request, requestID
}

func testProtocolLimits() *contractv1.Limits {
	return &contractv1.Limits{
		MaxFrameBytes:         512,
		MaxDataBytes:          64,
		MaxMessageBytes:       64,
		MaxInFlightRequests:   1,
		MaxMetadataEntries:    1,
		MaxMetadataValueBytes: 64,
		MaxCreditBytes:        64,
	}
}

func TestNegotiatedLimitsAreEnforcedAfterHandshake(t *testing.T) {
	t.Parallel()

	limits := &contractv1.Limits{
		MaxFrameBytes:         512,
		MaxDataBytes:          4,
		MaxMessageBytes:       64,
		MaxInFlightRequests:   1,
		MaxMetadataEntries:    1,
		MaxMetadataValueBytes: 4,
		MaxCreditBytes:        4,
	}
	requestID := bytes.Repeat([]byte{0x52}, protocolv1.RequestIDBytes)
	tests := []struct {
		name  string
		frame proto.Message
		want  bool
	}{
		{
			name: "selected data limit",
			frame: testGatewayInFrame(
				requestID,
				contractv1.TrafficClass_TRAFFIC_CLASS_REGULAR,
				1,
				&contractv1.ConnectResponse_Data{Data: &contractv1.Data{Payload: []byte("12345")}},
			),
		},
		{
			name: "selected credit limit",
			frame: testGatewayInFrame(
				requestID,
				contractv1.TrafficClass_TRAFFIC_CLASS_REGULAR,
				1,
				&contractv1.ConnectResponse_Credit{Credit: &contractv1.Credit{Bytes: 5}},
			),
		},
		{
			name: "selected metadata value limit",
			frame: testGatewayInFrame(
				requestID,
				contractv1.TrafficClass_TRAFFIC_CLASS_REGULAR,
				1,
				&contractv1.ConnectResponse_Open{Open: &contractv1.Open{
					RouteId:  contractv1.RouteId_ROUTE_ID_USER_GET_ME,
					Deadline: validFutureTimestamp(),
					Metadata: []*contractv1.Metadata{{
						Key:   contractv1.MetadataKey_METADATA_KEY_CONTENT_TYPE,
						Value: []byte("application/protobuf"),
					}},
				}},
			),
		},
		{
			name: "within selected limits",
			frame: testGatewayInFrame(
				requestID,
				contractv1.TrafficClass_TRAFFIC_CLASS_REGULAR,
				1,
				&contractv1.ConnectResponse_Data{Data: &contractv1.Data{Payload: []byte("1234")}},
			),
			want: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := withinNegotiatedLimits(test.frame, limits); got != test.want {
				t.Fatalf("withinNegotiatedLimits() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestNegotiatedFrameSizeIsEnforcedAfterHandshake(t *testing.T) {
	t.Parallel()

	frame := testGatewayInFrame(
		nil,
		contractv1.TrafficClass_TRAFFIC_CLASS_UNSPECIFIED,
		0,
		&contractv1.ConnectResponse_Pong{Pong: &contractv1.Pong{Nonce: 1}},
	)
	limits := &contractv1.Limits{
		MaxFrameBytes:         uint32(proto.Size(frame) - 1),
		MaxDataBytes:          1,
		MaxMessageBytes:       1,
		MaxInFlightRequests:   1,
		MaxMetadataEntries:    1,
		MaxMetadataValueBytes: 1,
		MaxCreditBytes:        1,
	}
	if withinNegotiatedLimits(frame, limits) {
		t.Fatal("withinNegotiatedLimits() accepted a frame over the selected frame limit")
	}
}

func validGatewayOutDataFrame(
	tunnelID []byte,
	requestID []byte,
	class contractv1.TrafficClass,
) *contractv1.ConnectRequest {
	return &contractv1.ConnectRequest{
		Header: &contractv1.FrameHeader{
			ProtocolVersion: protocolVersion,
			TunnelId:        bytes.Clone(tunnelID),
			RequestId:       bytes.Clone(requestID),
			Sequence:        1,
			TrafficClass:    class,
		},
		Payload: &contractv1.ConnectRequest_Data{Data: &contractv1.Data{Payload: []byte{1}}},
	}
}

func validFutureTimestamp() *timestamppb.Timestamp {
	return timestamppb.New(time.Now().Add(time.Minute))
}
