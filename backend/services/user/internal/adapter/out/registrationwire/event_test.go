package registrationwire

import (
	"bytes"
	"testing"
	"time"

	authv1 "github.com/v0hmly/marketmesh/api/gen/go/auth/v1"
	"github.com/v0hmly/marketmesh/services/user/internal/domain/profile"
	"github.com/v0hmly/marketmesh/services/user/internal/domain/registrationevent"
	"google.golang.org/protobuf/proto"
)

func TestImmutableRegistrationWire(t *testing.T) {
	subject, _ := profile.NewSubjectID(bytes.Repeat([]byte{2}, 16))
	event := registrationevent.Event{ID: [16]byte{1}, SubjectID: subject, OccurredAt: time.Date(2026, 9, 9, 0, 0, 0, 123000, time.UTC), TraceID: [16]byte{3}, CausationID: [16]byte{4}}
	payload, err := Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Unmarshal(payload)
	if err != nil || decoded != event {
		t.Fatal("roundtrip", decoded, err)
	}
	if err := ValidatePayload(payload, [16]byte{9}); err == nil {
		t.Fatal("mismatched event ID accepted")
	}
	for _, change := range []func(*authv1.AccountRegisteredEvent){
		func(m *authv1.AccountRegisteredEvent) { m.SchemaVersion++ }, func(m *authv1.AccountRegisteredEvent) { m.EventType = "unknown" },
		func(m *authv1.AccountRegisteredEvent) { m.Producer = "user" }, func(m *authv1.AccountRegisteredEvent) { m.SubjectId = nil },
		func(m *authv1.AccountRegisteredEvent) { m.EventId = nil }, func(m *authv1.AccountRegisteredEvent) { m.TraceId = []byte{1} },
		func(m *authv1.AccountRegisteredEvent) { m.CausationId = make([]byte, 16) }, func(m *authv1.AccountRegisteredEvent) { m.OccurredAtUnixNano++ },
	} {
		var m authv1.AccountRegisteredEvent
		if err := proto.Unmarshal(payload, &m); err != nil {
			t.Fatal(err)
		}
		change(&m)
		bad, _ := proto.Marshal(&m)
		if _, err := Unmarshal(bad); err == nil {
			t.Fatal("invalid event accepted")
		}
	}
	for _, bad := range [][]byte{nil, bytes.Repeat([]byte{1}, MaxPayloadBytes+1), append(append([]byte{}, payload...), 0x48, 1), append(append([]byte{}, payload...), payload...)} {
		if _, err := Unmarshal(bad); err == nil {
			t.Fatal("noncanonical event accepted")
		}
	}
}
