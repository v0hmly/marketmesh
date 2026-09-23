package tunnelv1_test

import (
	"testing"

	codec "github.com/v0hmly/marketmesh/api/tunnel/v1"
	"google.golang.org/protobuf/proto"
)

func FuzzDecodeGatewayOutFrame(f *testing.F) {
	f.Add(mustMarshal(f, validGatewayOutHello()))
	f.Add([]byte{})
	f.Add([]byte{0xff})

	f.Fuzz(func(t *testing.T, wire []byte) {
		frame, err := codec.DecodeGatewayOutFrame(wire)
		if err != nil {
			return
		}

		roundTrip, err := proto.Marshal(frame)
		if err != nil {
			t.Fatalf("proto.Marshal() accepted frame error = %v", err)
		}
		if _, err := codec.DecodeGatewayOutFrame(roundTrip); err != nil {
			t.Fatalf("DecodeGatewayOutFrame() round trip error = %v", err)
		}
	})
}

func FuzzDecodeGatewayInFrame(f *testing.F) {
	f.Add(mustMarshal(f, validOpenFrame()))
	f.Add([]byte{})
	f.Add([]byte{0xff})

	f.Fuzz(func(t *testing.T, wire []byte) {
		frame, err := codec.DecodeGatewayInFrame(wire)
		if err != nil {
			return
		}

		roundTrip, err := proto.Marshal(frame)
		if err != nil {
			t.Fatalf("proto.Marshal() accepted frame error = %v", err)
		}
		if _, err := codec.DecodeGatewayInFrame(roundTrip); err != nil {
			t.Fatalf("DecodeGatewayInFrame() round trip error = %v", err)
		}
	})
}
