package tunnelv1

import (
	"errors"
	"fmt"

	contractv1 "github.com/v0hmly/marketmesh/api/gen/go/tunnel/v1"
	"google.golang.org/protobuf/encoding/protowire"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
)

const (
	protocolVersion     uint32 = 1
	maxRecursionDepth          = 32
	outLastPayloadField        = protowire.Number(19)
	inLastPayloadField         = protowire.Number(18)
	firstPayloadField          = protowire.Number(10)
)

var (
	// ErrEmptyFrame означает отсутствие protobuf-кадра.
	ErrEmptyFrame = errors.New("empty tunnel frame")
	// ErrFrameTooLarge означает превышение абсолютного wire-лимита v1.
	ErrFrameTooLarge = errors.New("tunnel frame exceeds protocol limit")
	// ErrMalformedFrame означает синтаксически некорректный protobuf wire format.
	ErrMalformedFrame = errors.New("malformed tunnel frame")
	// ErrUnknownFrameType означает неизвестный или неоднозначный тип payload.
	ErrUnknownFrameType = errors.New("unknown tunnel frame type")
	// ErrUnknownField означает наличие неизвестного поля в сообщении v1.
	ErrUnknownField = errors.New("unknown tunnel frame field")
	// ErrUnsupportedVersion означает несовместимую версию протокола.
	ErrUnsupportedVersion = errors.New("unsupported tunnel protocol version")
	// ErrInvalidFrame означает нарушение структурного ограничения v1.
	ErrInvalidFrame = errors.New("invalid tunnel frame")
)

// DecodeGatewayOutFrame разбирает один ограниченный кадр от gateway-out.
// Неизвестные поля и frame types отклоняются, а исходные bytes не включаются
// в error.
func DecodeGatewayOutFrame(data []byte) (*contractv1.ConnectRequest, error) {
	if err := validateEncodedFrame(data, outLastPayloadField); err != nil {
		return nil, err
	}

	frame := &contractv1.ConnectRequest{}
	if err := unmarshalFrame(data, frame); err != nil {
		return nil, err
	}
	if err := ValidateGatewayOutFrame(frame); err != nil {
		return nil, err
	}

	return frame, nil
}

// DecodeGatewayInFrame разбирает один ограниченный кадр от gateway-in.
// Неизвестные поля и frame types отклоняются, а исходные bytes не включаются
// в error.
func DecodeGatewayInFrame(data []byte) (*contractv1.ConnectResponse, error) {
	if err := validateEncodedFrame(data, inLastPayloadField); err != nil {
		return nil, err
	}

	frame := &contractv1.ConnectResponse{}
	if err := unmarshalFrame(data, frame); err != nil {
		return nil, err
	}
	if err := ValidateGatewayInFrame(frame); err != nil {
		return nil, err
	}

	return frame, nil
}

func unmarshalFrame(data []byte, frame proto.Message) error {
	options := proto.UnmarshalOptions{
		DiscardUnknown: false,
		RecursionLimit: maxRecursionDepth,
		NoLazyDecoding: true,
	}
	if err := options.Unmarshal(data, frame); err != nil {
		return ErrMalformedFrame
	}

	return nil
}

func validateEncodedFrame(data []byte, lastPayloadField protowire.Number) error {
	if len(data) == 0 {
		return ErrEmptyFrame
	}
	if len(data) > MaxEncodedFrameBytes {
		return ErrFrameTooLarge
	}

	headerCount := 0
	payloadCount := 0
	remaining := data
	for len(remaining) > 0 {
		number, wireType, tagLength := protowire.ConsumeTag(remaining)
		if tagLength < 0 {
			return ErrMalformedFrame
		}
		remaining = remaining[tagLength:]

		fieldLength := protowire.ConsumeFieldValue(number, wireType, remaining)
		if fieldLength < 0 {
			return ErrMalformedFrame
		}

		switch {
		case number == 1:
			headerCount++
		case number >= firstPayloadField && number <= lastPayloadField:
			payloadCount++
		default:
			return ErrUnknownFrameType
		}
		if wireType != protowire.BytesType {
			return ErrMalformedFrame
		}

		remaining = remaining[fieldLength:]
	}

	if headerCount != 1 || payloadCount != 1 {
		return ErrUnknownFrameType
	}

	return nil
}

func rejectUnknownFields(message protoreflect.Message, path string) error {
	if len(message.GetUnknown()) != 0 {
		return fmt.Errorf("%w: %s", ErrUnknownField, path)
	}

	var nestedError error
	message.Range(func(field protoreflect.FieldDescriptor, value protoreflect.Value) bool {
		if field.Kind() != protoreflect.MessageKind && field.Kind() != protoreflect.GroupKind {
			return true
		}

		fieldPath := path + "." + string(field.Name())
		switch {
		case field.IsList():
			list := value.List()
			for index := range list.Len() {
				nestedError = rejectUnknownFields(
					list.Get(index).Message(),
					fmt.Sprintf("%s[%d]", fieldPath, index),
				)
				if nestedError != nil {
					return false
				}
			}
		case field.IsMap():
			if field.MapValue().Kind() != protoreflect.MessageKind {
				return true
			}
			value.Map().Range(func(_ protoreflect.MapKey, mapValue protoreflect.Value) bool {
				nestedError = rejectUnknownFields(mapValue.Message(), fieldPath)
				return nestedError == nil
			})
		default:
			nestedError = rejectUnknownFields(value.Message(), fieldPath)
		}

		return nestedError == nil
	})

	return nestedError
}

func invalidFrame(field string, reason string) error {
	return fmt.Errorf("%w: %s %s", ErrInvalidFrame, field, reason)
}

func unsupportedVersion(field string) error {
	return fmt.Errorf("%w: %s", ErrUnsupportedVersion, field)
}
