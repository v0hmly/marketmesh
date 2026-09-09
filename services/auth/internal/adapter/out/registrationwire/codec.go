// Package registrationwire owns the versioned protobuf representation of registration facts.
package registrationwire

import (
	"bytes"
	"time"

	authv1 "github.com/v0hmly/marketmesh/api/gen/go/auth/v1"
	"github.com/v0hmly/marketmesh/services/auth/internal/domain/credential"
	"github.com/v0hmly/marketmesh/services/auth/internal/domain/registrationevent"
	"google.golang.org/protobuf/proto"
)

const MaxPayloadBytes = 8192

func Marshal(event registrationevent.Event) ([]byte, error) {
	if err := event.Validate(); err != nil {
		return nil, err
	}
	message := &authv1.AccountRegisteredEvent{
		EventId: event.ID[:], EventType: registrationevent.Type, SchemaVersion: registrationevent.SchemaVersion,
		OccurredAtUnixNano: event.OccurredAt.UnixNano(), Producer: registrationevent.Producer, SubjectId: event.SubjectID.Bytes(),
	}
	if event.TraceID != ([16]byte{}) {
		message.TraceId = event.TraceID[:]
	}
	if event.CausationID != ([16]byte{}) {
		message.CausationId = event.CausationID[:]
	}
	return proto.MarshalOptions{Deterministic: true}.Marshal(message)
}

func Unmarshal(payload []byte) (registrationevent.Event, error) {
	var event registrationevent.Event
	invalid := registrationevent.ErrInvalidEvent
	if len(payload) == 0 || len(payload) > MaxPayloadBytes {
		return event, invalid
	}
	var message authv1.AccountRegisteredEvent
	if proto.Unmarshal(payload, &message) != nil || len(message.ProtoReflect().GetUnknown()) != 0 || message.GetEventType() != registrationevent.Type || message.GetSchemaVersion() != registrationevent.SchemaVersion || message.GetProducer() != registrationevent.Producer || len(message.GetEventId()) != 16 {
		return event, invalid
	}
	subject, err := credential.NewSubjectID(message.GetSubjectId())
	if err != nil {
		return event, invalid
	}
	copy(event.ID[:], message.GetEventId())
	event.SubjectID = subject
	event.OccurredAt = time.Unix(0, message.GetOccurredAtUnixNano()).UTC()
	for _, item := range []struct {
		raw    []byte
		target *[16]byte
	}{{message.GetTraceId(), &event.TraceID}, {message.GetCausationId(), &event.CausationID}} {
		if len(item.raw) == 0 {
			continue
		}
		if len(item.raw) != 16 {
			return registrationevent.Event{}, invalid
		}
		copy(item.target[:], item.raw)
		if *item.target == ([16]byte{}) {
			return registrationevent.Event{}, invalid
		}
	}
	if err := event.Validate(); err != nil {
		return registrationevent.Event{}, err
	}
	// Persisted payloads are canonical and immutable; duplicate fields and hidden data are rejected.
	canonical, err := Marshal(event)
	if err != nil || !bytes.Equal(canonical, payload) {
		return registrationevent.Event{}, invalid
	}
	return event, nil
}

func ValidatePayload(payload []byte, id [16]byte) error {
	event, err := Unmarshal(payload)
	if err != nil {
		return err
	}
	if event.ID != id {
		return registrationevent.ErrInvalidEvent
	}
	return nil
}
