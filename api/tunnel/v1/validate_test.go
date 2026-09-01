package tunnelv1_test

import (
	"errors"
	"testing"
	"time"

	contractv1 "github.com/v0hmly/marketmesh/api/gen/go/tunnel/v1"
	codec "github.com/v0hmly/marketmesh/api/tunnel/v1"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func TestDecodeGatewayOutFrame(t *testing.T) {
	t.Parallel()

	wire := mustMarshal(t, validGatewayOutHello())
	frame, err := codec.DecodeGatewayOutFrame(wire)
	if err != nil {
		t.Fatalf("DecodeGatewayOutFrame() error = %v", err)
	}
	if frame.GetHello() == nil {
		t.Fatal("DecodeGatewayOutFrame() payload is not Hello")
	}
}

func TestDecodeGatewayInFrame(t *testing.T) {
	t.Parallel()

	wire := mustMarshal(t, validOpenFrame())
	frame, err := codec.DecodeGatewayInFrame(wire)
	if err != nil {
		t.Fatalf("DecodeGatewayInFrame() error = %v", err)
	}
	if frame.GetOpen() == nil {
		t.Fatal("DecodeGatewayInFrame() payload is not Open")
	}
}

func TestDecodeGatewayInFrame_RejectsWireViolations(t *testing.T) {
	t.Parallel()

	validWire := mustMarshal(t, validOpenFrame())
	cancelWire := mustMarshal(t, &contractv1.Cancel{
		Reason: contractv1.CancelReason_CANCEL_REASON_CALLER,
	})
	ambiguousWire := append([]byte{}, validWire...)
	ambiguousWire = protowire.AppendTag(ambiguousWire, 14, protowire.BytesType)
	ambiguousWire = protowire.AppendBytes(ambiguousWire, cancelWire)

	unknownTypeWire := append([]byte{}, validWire...)
	unknownTypeWire = protowire.AppendTag(unknownTypeWire, 19, protowire.BytesType)
	unknownTypeWire = protowire.AppendBytes(unknownTypeWire, nil)

	nestedUnknown := validOpenFrame()
	nestedUnknown.GetOpen().ProtoReflect().SetUnknown([]byte{0x28, 0x01})
	nestedUnknownWire := mustMarshal(t, nestedUnknown)

	tests := []struct {
		name    string
		wire    []byte
		wantErr error
	}{
		{
			name:    "empty frame",
			wire:    []byte{},
			wantErr: codec.ErrEmptyFrame,
		},
		{
			name:    "frame exceeds absolute limit",
			wire:    make([]byte, codec.MaxEncodedFrameBytes+1),
			wantErr: codec.ErrFrameTooLarge,
		},
		{
			name:    "unknown frame type",
			wire:    unknownTypeWire,
			wantErr: codec.ErrUnknownFrameType,
		},
		{
			name:    "open and cancel are ambiguous",
			wire:    ambiguousWire,
			wantErr: codec.ErrUnknownFrameType,
		},
		{
			name:    "unknown nested field",
			wire:    nestedUnknownWire,
			wantErr: codec.ErrUnknownField,
		},
		{
			name:    "malformed wire",
			wire:    []byte{0x0a, 0xff},
			wantErr: codec.ErrMalformedFrame,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, err := codec.DecodeGatewayInFrame(tt.wire)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("DecodeGatewayInFrame() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateGatewayOutFrame(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		frame   func() *contractv1.ConnectRequest
		wantErr error
	}{
		{
			name:  "valid Hello",
			frame: validGatewayOutHello,
		},
		{
			name: "version one is not offered",
			frame: func() *contractv1.ConnectRequest {
				frame := validGatewayOutHello()
				frame.GetHello().SupportedProtocolVersions = []uint32{2}
				return frame
			},
			wantErr: codec.ErrUnsupportedVersion,
		},
		{
			name: "unknown advertised route",
			frame: func() *contractv1.ConnectRequest {
				frame := validGatewayOutHello()
				frame.GetHello().RouteIds = []contractv1.RouteId{999}
				return frame
			},
			wantErr: codec.ErrInvalidFrame,
		},
		{
			name: "duplicated capability",
			frame: func() *contractv1.ConnectRequest {
				frame := validGatewayOutHello()
				frame.GetHello().Capabilities = []contractv1.Capability{
					contractv1.Capability_CAPABILITY_DRAIN,
					contractv1.Capability_CAPABILITY_DRAIN,
				}
				return frame
			},
			wantErr: codec.ErrInvalidFrame,
		},
		{
			name: "frame limit does not exceed data limit",
			frame: func() *contractv1.ConnectRequest {
				frame := validGatewayOutHello()
				frame.GetHello().Limits.MaxFrameBytes = frame.GetHello().Limits.MaxDataBytes
				return frame
			},
			wantErr: codec.ErrInvalidFrame,
		},
		{
			name: "nil HalfClose payload",
			frame: func() *contractv1.ConnectRequest {
				return &contractv1.ConnectRequest{
					Header: validRequestHeader(contractv1.TrafficClass_TRAFFIC_CLASS_REGULAR),
					Payload: &contractv1.ConnectRequest_HalfClose{
						HalfClose: nil,
					},
				}
			},
			wantErr: codec.ErrInvalidFrame,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := codec.ValidateGatewayOutFrame(tt.frame())
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("ValidateGatewayOutFrame() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateGatewayInFrame(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		frame   func() *contractv1.ConnectResponse
		wantErr error
	}{
		{
			name:  "valid Hello",
			frame: validGatewayInHello,
		},
		{
			name: "missing gateway-in instance id",
			frame: func() *contractv1.ConnectResponse {
				frame := validGatewayInHello()
				frame.GetHello().InstanceId = nil
				return frame
			},
			wantErr: codec.ErrInvalidFrame,
		},
		{
			name:  "valid Open",
			frame: validOpenFrame,
		},
		{
			name: "incompatible frame version",
			frame: func() *contractv1.ConnectResponse {
				frame := validOpenFrame()
				frame.Header.ProtocolVersion = 2
				return frame
			},
			wantErr: codec.ErrUnsupportedVersion,
		},
		{
			name: "unknown route",
			frame: func() *contractv1.ConnectResponse {
				frame := validOpenFrame()
				frame.GetOpen().RouteId = 999
				return frame
			},
			wantErr: codec.ErrInvalidFrame,
		},
		{
			name: "route traffic class mismatch",
			frame: func() *contractv1.ConnectResponse {
				frame := validOpenFrame()
				frame.Header.TrafficClass = contractv1.TrafficClass_TRAFFIC_CLASS_REALTIME
				return frame
			},
			wantErr: codec.ErrInvalidFrame,
		},
		{
			name: "deadline is absent",
			frame: func() *contractv1.ConnectResponse {
				frame := validOpenFrame()
				frame.GetOpen().Deadline = nil
				return frame
			},
			wantErr: codec.ErrInvalidFrame,
		},
		{
			name: "arbitrary metadata key",
			frame: func() *contractv1.ConnectResponse {
				frame := validOpenFrame()
				frame.GetOpen().Metadata = []*contractv1.Metadata{
					{
						Key:   999,
						Value: []byte("forbidden"),
					},
				}
				return frame
			},
			wantErr: codec.ErrInvalidFrame,
		},
		{
			name: "malformed traceparent",
			frame: func() *contractv1.ConnectResponse {
				frame := validOpenFrame()
				frame.GetOpen().Metadata = append(
					frame.GetOpen().Metadata,
					&contractv1.Metadata{
						Key:   contractv1.MetadataKey_METADATA_KEY_TRACEPARENT,
						Value: []byte("00-invalid"),
					},
				)
				return frame
			},
			wantErr: codec.ErrInvalidFrame,
		},
		{
			name: "malformed tracestate",
			frame: func() *contractv1.ConnectResponse {
				frame := validOpenFrame()
				frame.GetOpen().Metadata = append(
					frame.GetOpen().Metadata,
					&contractv1.Metadata{
						Key:   contractv1.MetadataKey_METADATA_KEY_TRACESTATE,
						Value: []byte("missing-value"),
					},
				)
				return frame
			},
			wantErr: codec.ErrInvalidFrame,
		},
		{
			name: "idempotency key exceeds limit",
			frame: func() *contractv1.ConnectResponse {
				frame := validOpenFrame()
				frame.GetOpen().IdempotencyKey = make([]byte, codec.MaxIdempotencyKeyBytes+1)
				return frame
			},
			wantErr: codec.ErrInvalidFrame,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := codec.ValidateGatewayInFrame(tt.frame())
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("ValidateGatewayInFrame() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestContractHasNoUniversalProxySelectors(t *testing.T) {
	t.Parallel()

	assertFieldNames(t, (&contractv1.Open{}).ProtoReflect().Descriptor(), []protoreflect.Name{
		"route_id",
		"deadline",
		"idempotency_key",
		"metadata",
	})
	assertFieldNames(t, (&contractv1.Result{}).ProtoReflect().Descriptor(), []protoreflect.Name{
		"code",
		"retry_after",
		"metadata",
	})

	requestFields := (&contractv1.ConnectRequest{}).ProtoReflect().Descriptor().Fields()
	if requestFields.ByName("open") != nil {
		t.Fatal("ConnectRequest unexpectedly permits gateway-out to send Open")
	}
	responseFields := (&contractv1.ConnectResponse{}).ProtoReflect().Descriptor().Fields()
	if responseFields.ByName("result") != nil || responseFields.ByName("revoke_session") != nil {
		t.Fatal("ConnectResponse unexpectedly permits gateway-in to send Result or RevokeSession")
	}
}

func assertFieldNames(
	t *testing.T,
	descriptor protoreflect.MessageDescriptor,
	want []protoreflect.Name,
) {
	t.Helper()

	fields := descriptor.Fields()
	if fields.Len() != len(want) {
		t.Fatalf("%s field count = %d, want %d", descriptor.FullName(), fields.Len(), len(want))
	}
	for index, name := range want {
		if fields.Get(index).Name() != name {
			t.Fatalf(
				"%s field %d = %s, want %s",
				descriptor.FullName(),
				index,
				fields.Get(index).Name(),
				name,
			)
		}
	}
}

func validGatewayOutHello() *contractv1.ConnectRequest {
	return &contractv1.ConnectRequest{
		Header: &contractv1.FrameHeader{},
		Payload: &contractv1.ConnectRequest_Hello{
			Hello: &contractv1.GatewayOutHello{
				InstanceId:                repeatedByte(0x11, codec.InstanceIDBytes),
				SupportedProtocolVersions: []uint32{1},
				Capabilities: []contractv1.Capability{
					contractv1.Capability_CAPABILITY_DRAIN,
					contractv1.Capability_CAPABILITY_SESSION_REVOCATION,
				},
				TrafficClasses: []contractv1.TrafficClass{
					contractv1.TrafficClass_TRAFFIC_CLASS_CONTROL_AUTH,
					contractv1.TrafficClass_TRAFFIC_CLASS_REGULAR,
				},
				RouteIds: []contractv1.RouteId{
					contractv1.RouteId_ROUTE_ID_AUTH_LOGIN,
					contractv1.RouteId_ROUTE_ID_USER_GET_ME,
				},
				Limits: validLimits(),
			},
		},
	}
}

func validGatewayInHello() *contractv1.ConnectResponse {
	return &contractv1.ConnectResponse{
		Header: &contractv1.FrameHeader{
			TunnelId: repeatedByte(0x22, codec.TunnelIDBytes),
		},
		Payload: &contractv1.ConnectResponse_Hello{
			Hello: &contractv1.GatewayInHello{
				InstanceId:              repeatedByte(0x44, codec.InstanceIDBytes),
				SelectedProtocolVersion: 1,
				Capabilities: []contractv1.Capability{
					contractv1.Capability_CAPABILITY_DRAIN,
				},
				TrafficClasses: []contractv1.TrafficClass{
					contractv1.TrafficClass_TRAFFIC_CLASS_REGULAR,
				},
				RouteIds: []contractv1.RouteId{
					contractv1.RouteId_ROUTE_ID_USER_GET_ME,
				},
				Limits: validLimits(),
			},
		},
	}
}

func validOpenFrame() *contractv1.ConnectResponse {
	return &contractv1.ConnectResponse{
		Header: validRequestHeader(contractv1.TrafficClass_TRAFFIC_CLASS_REGULAR),
		Payload: &contractv1.ConnectResponse_Open{
			Open: &contractv1.Open{
				RouteId:  contractv1.RouteId_ROUTE_ID_USER_GET_ME,
				Deadline: timestamppb.New(time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)),
				Metadata: []*contractv1.Metadata{
					{
						Key:   contractv1.MetadataKey_METADATA_KEY_CONTENT_TYPE,
						Value: []byte("application/protobuf"),
					},
				},
			},
		},
	}
}

func validRequestHeader(class contractv1.TrafficClass) *contractv1.FrameHeader {
	return &contractv1.FrameHeader{
		ProtocolVersion: 1,
		TunnelId:        repeatedByte(0x22, codec.TunnelIDBytes),
		RequestId:       repeatedByte(0x33, codec.RequestIDBytes),
		Sequence:        1,
		TrafficClass:    class,
	}
}

func validLimits() *contractv1.Limits {
	return &contractv1.Limits{
		MaxFrameBytes:         codec.MaxEncodedFrameBytes,
		MaxDataBytes:          codec.MaxDataBytes,
		MaxMessageBytes:       codec.MaxMessageBytes,
		MaxInFlightRequests:   128,
		MaxMetadataEntries:    codec.MaxMetadataEntries,
		MaxMetadataValueBytes: codec.MaxMetadataValueBytes,
		MaxCreditBytes:        codec.MaxCreditBytes,
	}
}

func repeatedByte(value byte, count int) []byte {
	result := make([]byte, count)
	for index := range result {
		result[index] = value
	}
	return result
}

func mustMarshal(t testing.TB, message proto.Message) []byte {
	t.Helper()

	wire, err := proto.Marshal(message)
	if err != nil {
		t.Fatalf("proto.Marshal() error = %v", err)
	}
	return wire
}
