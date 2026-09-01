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
