package registrationevent

import (
	"errors"
	"github.com/v0hmly/marketmesh/services/auth/internal/domain/credential"
	"testing"
	"time"
)

func TestEventValidation(t *testing.T) {
	valid := Event{ID: [16]byte{1}, SubjectID: credential.SubjectID{2}, OccurredAt: time.Now().UTC().Truncate(time.Microsecond)}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*Event){func(e *Event) { e.ID = [16]byte{} }, func(e *Event) { e.SubjectID = credential.SubjectID{} }, func(e *Event) { e.OccurredAt = time.Time{} }, func(e *Event) { e.OccurredAt = time.Unix(0, 0) }, func(e *Event) { e.OccurredAt = time.Date(3000, 1, 1, 0, 0, 0, 0, time.UTC) }, func(e *Event) { e.OccurredAt = e.OccurredAt.Add(time.Nanosecond) }} {
		e := valid
		change(&e)
		if !errors.Is(e.Validate(), ErrInvalidEvent) {
			t.Fatal("invalid event accepted")
		}
	}
}
