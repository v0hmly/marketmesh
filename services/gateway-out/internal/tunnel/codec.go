package tunnel

import (
	"errors"

	contractv1 "github.com/v0hmly/marketmesh/api/gen/go/tunnel/v1"
	protocolv1 "github.com/v0hmly/marketmesh/api/tunnel/v1"
	"google.golang.org/protobuf/proto"
)

// strictCodec сохраняет исходные MM-10 fail-closed wire-проверки внутри gRPC.
type strictCodec struct{}

func (strictCodec) Marshal(value any) ([]byte, error) {
	message, ok := value.(proto.Message)
	if !ok {
		return nil, errors.New("gateway-out tunnel codec requires protobuf message")
	}

	switch frame := value.(type) {
	case *contractv1.ConnectRequest:
		if err := protocolv1.ValidateGatewayOutFrame(frame); err != nil {
			return nil, err
		}
	case *contractv1.ConnectResponse:
		if err := protocolv1.ValidateGatewayInFrame(frame); err != nil {
			return nil, err
		}
	}

	return proto.MarshalOptions{Deterministic: true}.Marshal(message)
}

func (strictCodec) Unmarshal(data []byte, value any) error {
	switch target := value.(type) {
	case *contractv1.ConnectResponse:
		decoded, err := protocolv1.DecodeGatewayInFrame(data)
		if err != nil {
			return err
		}
		proto.Reset(target)
		proto.Merge(target, decoded)
		return nil
	case *contractv1.ConnectRequest:
		decoded, err := protocolv1.DecodeGatewayOutFrame(data)
		if err != nil {
			return err
		}
		proto.Reset(target)
		proto.Merge(target, decoded)
		return nil
	default:
		message, ok := value.(proto.Message)
		if !ok {
			return errors.New("gateway-out tunnel codec requires protobuf message")
		}
		return (proto.UnmarshalOptions{
			DiscardUnknown: false,
			RecursionLimit: 32,
		}).Unmarshal(data, message)
	}
}

func (strictCodec) Name() string {
	return "proto"
}
