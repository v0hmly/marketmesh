package registrationevent

import (
	"bytes"
	"context"
	"errors"
	"github.com/v0hmly/marketmesh/services/auth/internal/domain/credential"
	"go.opentelemetry.io/otel/trace"
	"testing"
	"time"
)

func TestFactoryUsesOpaqueIDAndTrustedTraceOnly(t *testing.T) {
	now := time.Date(2026, 9, 9, 12, 0, 0, 123456789, time.FixedZone("test", 3600))
	factory := &Factory{random: bytes.NewReader(bytes.Repeat([]byte{7}, 16)), clock: func() time.Time { return now }}
	traceID := trace.TraceID{1}
	ctx := trace.ContextWithSpanContext(context.Background(), trace.NewSpanContext(trace.SpanContextConfig{TraceID: traceID, SpanID: trace.SpanID{2}}))
	event, err := factory.New(ctx, credential.SubjectID{3})
	if err != nil {
		t.Fatal(err)
	}
	if event.ID != ([16]byte{7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7, 7}) || event.TraceID != ([16]byte(traceID)) || event.CausationID != ([16]byte{}) || event.SubjectID != (credential.SubjectID{3}) || event.OccurredAt.Location() != time.UTC || !event.OccurredAt.Equal(now.Truncate(time.Microsecond)) {
		t.Fatal("unexpected event fields")
	}
}
func TestFactoryFreshIDsAndFailures(t *testing.T) {
	factory := New()
	a, err := factory.New(context.Background(), credential.SubjectID{1})
	if err != nil {
		t.Fatal(err)
	}
	b, err := factory.New(context.Background(), credential.SubjectID{1})
	if err != nil || a.ID == b.ID {
		t.Fatal("event ID reused")
	}
	factory.random = bytes.NewReader(nil)
	if _, err := factory.New(context.Background(), credential.SubjectID{1}); err == nil {
		t.Fatal("entropy failure ignored")
	}
	factory.random = bytes.NewReader(make([]byte, 16))
	if _, err := factory.New(context.Background(), credential.SubjectID{1}); err == nil {
		t.Fatal("zero ID accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := New().New(ctx, credential.SubjectID{1}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := New().New(nil, credential.SubjectID{1}); err == nil {
		t.Fatal("nil context accepted")
	}
	if _, err := New().New(context.Background(), credential.SubjectID{}); err == nil {
		t.Fatal("zero subject accepted")
	}
}
