package audit_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/v0hmly/marketmesh/platform/logger"
	"github.com/v0hmly/marketmesh/platform/telemetry"
	"github.com/v0hmly/marketmesh/services/auth/internal/adapter/out/audit"
	"github.com/v0hmly/marketmesh/services/auth/internal/application/login"
)

func TestRecorderEmitsOnlyFiniteNonPIIFields(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	log, err := logger.New(logger.Config{Service: "auth", Version: "test", Environment: "test", Output: &output})
	if err != nil {
		t.Fatalf("logger.New() error = %v", err)
	}
	pipeline := telemetry.NewNoop()
	recorder, err := audit.New(log, pipeline.Meter("test"))
	if err != nil {
		t.Fatalf("audit.New() error = %v", err)
	}
	recorder.LoginSucceeded(context.Background())
	recorder.LoginFailed(context.Background(), login.FailureReasonRejected)

	logs := output.String()
	for _, expected := range []string{`"outcome":"success"`, `"outcome":"failure"`, `"reason":"rejected"`} {
		if !strings.Contains(logs, expected) {
			t.Fatalf("logs do not contain %q: %s", expected, logs)
		}
	}
	for _, forbidden := range []string{"identifier", "subject_id", "password", "digest", "salt", "payload"} {
		if strings.Contains(logs, forbidden) {
			t.Fatalf("logs contain forbidden field %q: %s", forbidden, logs)
		}
	}
}

func TestNewRejectsNilLogger(t *testing.T) {
	t.Parallel()

	if _, err := audit.New(nil, telemetry.NewNoop().Meter("test")); err == nil {
		t.Fatal("audit.New(nil) error = nil")
	}
}
