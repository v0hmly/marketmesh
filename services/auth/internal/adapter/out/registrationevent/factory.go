// Package registrationevent creates registration facts from trusted runtime primitives.
package registrationevent

import (
	"context"
	"crypto/rand"
	"errors"
	"github.com/v0hmly/marketmesh/services/auth/internal/domain/credential"
	domain "github.com/v0hmly/marketmesh/services/auth/internal/domain/registrationevent"
	"go.opentelemetry.io/otel/trace"
	"io"
	"time"
)

type Factory struct {
	random io.Reader
	clock  func() time.Time
}

func New() *Factory { return &Factory{random: rand.Reader, clock: time.Now} }
func (f *Factory) New(ctx context.Context, subject credential.SubjectID) (domain.Event, error) {
	if ctx == nil || f == nil || f.random == nil || f.clock == nil {
		return domain.Event{}, errors.New("registration event: required dependency missing")
	}
	if err := ctx.Err(); err != nil {
		return domain.Event{}, err
	}
	event := domain.Event{SubjectID: subject, OccurredAt: f.clock().UTC().Truncate(time.Microsecond)}
	if _, err := io.ReadFull(f.random, event.ID[:]); err != nil {
		return domain.Event{}, errors.New("registration event: randomness unavailable")
	}
	span := trace.SpanContextFromContext(ctx)
	if span.IsValid() {
		event.TraceID = [16]byte(span.TraceID())
	}
	if err := event.Validate(); err != nil {
		return domain.Event{}, err
	}
	if err := ctx.Err(); err != nil {
		return domain.Event{}, err
	}
	return event, nil
}
