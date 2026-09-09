package postgres

import (
	"context"
	"errors"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/v0hmly/marketmesh/services/auth/internal/adapter/out/registrationwire"
	"github.com/v0hmly/marketmesh/services/auth/internal/domain/registrationevent"
	"strings"
	"testing"
	"time"
)

func TestRegistrationCTEParametersAndWirePrivacy(t *testing.T) {
	executor := &executorStub{tag: pgconn.NewCommandTag("INSERT 0 1")}
	repository, _ := New(executor)
	value := testCredential(t, "private@example.com")
	event := registrationevent.Event{ID: [16]byte{9}, SubjectID: value.SubjectID(), OccurredAt: time.Now().UTC().Truncate(time.Microsecond)}
	created, err := repository.CreateRegistration(context.Background(), value, event)
	if err != nil || !created {
		t.Fatal(err)
	}
	if executor.sql != createRegistrationSQL || len(executor.arguments) != 6 || strings.Contains(executor.sql, value.Identifier().String()) {
		t.Fatal("unexpected parameterized statement")
	}
	payload := executor.arguments[5].([]byte)
	decoded, err := registrationwire.Unmarshal(payload)
	if err != nil || decoded != event {
		t.Fatal("event wire mismatch", err)
	}
	if strings.Contains(string(payload), value.Identifier().String()) || strings.Contains(string(payload), value.PasswordDigest().String()) {
		t.Fatal("credentials leaked into event")
	}
	executor.tag = pgconn.NewCommandTag("INSERT 0 0")
	if created, err := repository.CreateRegistration(context.Background(), value, event); err != nil || created {
		t.Fatal("duplicate not distinguished")
	}
}
func TestRegistrationRejectsInvalidEventsAndMasksErrors(t *testing.T) {
	executor := &executorStub{}
	repository, _ := New(executor)
	value := testCredential(t, "private@example.com")
	event := registrationevent.Event{ID: [16]byte{9}, SubjectID: value.SubjectID(), OccurredAt: time.Now().UTC().Truncate(time.Microsecond)}
	wrong := event
	wrong.SubjectID[0]++
	if _, err := repository.CreateRegistration(context.Background(), value, wrong); !errors.Is(err, registrationevent.ErrInvalidEvent) || executor.sql != "" {
		t.Fatal("subject mismatch reached SQL")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := repository.CreateRegistration(ctx, value, event); !errors.Is(err, context.Canceled) || executor.sql != "" {
		t.Fatal("canceled operation reached SQL")
	}
	if _, err := repository.CreateRegistration(nil, value, event); err == nil {
		t.Fatal("nil context accepted")
	}
	cause := errors.New("private@example.com secret-digest SQL")
	executor.err = cause
	if _, err := repository.CreateRegistration(context.Background(), value, event); err == nil || !errors.Is(err, cause) || strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "private") {
		t.Fatal("unsafe error")
	}
}
