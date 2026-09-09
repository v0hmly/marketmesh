package register_test

import (
	"context"
	"errors"
	"github.com/v0hmly/marketmesh/services/auth/internal/application/register"
	"github.com/v0hmly/marketmesh/services/auth/internal/domain/credential"
	"github.com/v0hmly/marketmesh/services/auth/internal/domain/registrationevent"
	"testing"
	"time"
)

type registrationWriterStub struct {
	calls   int
	value   credential.Credential
	event   registrationevent.Event
	created bool
	err     error
}

func (w *registrationWriterStub) CreateRegistration(_ context.Context, c credential.Credential, e registrationevent.Event) (bool, error) {
	w.calls++
	w.value = c
	w.event = e
	return w.created, w.err
}

type eventFactoryStub struct {
	event registrationevent.Event
	err   error
	calls int
}

func (f *eventFactoryStub) New(_ context.Context, subject credential.SubjectID) (registrationevent.Event, error) {
	f.calls++
	return f.event, f.err
}
func TestRegistrationEventsAtomicPortAndDuplicatePrivacy(t *testing.T) {
	for _, created := range []bool{true, false} {
		w := &registrationWriterStub{created: created}
		f := &eventFactoryStub{event: registrationevent.Event{ID: [16]byte{4}, SubjectID: subjectID(1), OccurredAt: time.Now().UTC().Truncate(time.Microsecond)}}
		uc, err := register.NewWithEvents(w, &hasherStub{digest: mustDigest(t, "encoded")}, &generatorStub{subjectID: subjectID(1)}, f)
		if err != nil {
			t.Fatal(err)
		}
		if err := uc.Execute(context.Background(), " Alice@Example.COM ", []byte("correct horse battery staple")); err != nil {
			t.Fatal(err)
		}
		if w.calls != 1 || f.calls != 1 || w.event != f.event || w.value.SubjectID() != w.event.SubjectID || w.value.Identifier().String() != "alice@example.com" {
			t.Fatal("credential/event not sent together")
		}
	}
}
func TestRegistrationEventFailureDoesNotStoreCredentials(t *testing.T) {
	for _, f := range []*eventFactoryStub{{err: errors.New("entropy unavailable")}, {event: registrationevent.Event{}}, {event: registrationevent.Event{ID: [16]byte{4}, SubjectID: subjectID(2), OccurredAt: time.Now().UTC().Truncate(time.Microsecond)}}} {
		w := &registrationWriterStub{}
		uc, err := register.NewWithEvents(w, &hasherStub{digest: mustDigest(t, "encoded")}, &generatorStub{subjectID: subjectID(1)}, f)
		if err != nil {
			t.Fatal(err)
		}
		if err := uc.Execute(context.Background(), "alice@example.com", []byte("correct horse battery staple")); err == nil || w.calls != 0 {
			t.Fatal("invalid event reached storage")
		}
	}
}
func TestRegistrationEventsConstructorAndCancellation(t *testing.T) {
	w := &registrationWriterStub{}
	h := &hasherStub{}
	g := &generatorStub{}
	f := &eventFactoryStub{}
	for _, args := range []struct {
		w register.RegistrationWriter
		h register.PasswordHasher
		g register.SubjectIDGenerator
		f register.EventFactory
	}{{nil, h, g, f}, {w, nil, g, f}, {w, h, nil, f}, {w, h, g, nil}} {
		if _, err := register.NewWithEvents(args.w, args.h, args.g, args.f); err == nil {
			t.Fatal("missing dependency accepted")
		}
	}
	uc, _ := register.NewWithEvents(w, h, g, f)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := uc.Execute(ctx, "alice@example.com", []byte("correct horse battery staple")); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if w.calls+h.calls+g.calls+f.calls != 0 {
		t.Fatal("canceled request reached dependencies")
	}
}
