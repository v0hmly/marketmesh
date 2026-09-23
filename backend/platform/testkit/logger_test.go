package testkit_test

import (
	"strings"
	"sync"
	"testing"

	platformlogger "github.com/v0hmly/marketmesh/platform/logger"
	"github.com/v0hmly/marketmesh/platform/testkit"
)

func TestLoggerCapturesStructuredMaskedEventsConcurrently(t *testing.T) {
	t.Parallel()

	log, capture := testkit.NewLogger(t)

	const writers = 16
	var group sync.WaitGroup
	for writer := range writers {
		group.Go(func() {
			log.Info(
				"worker completed",
				platformlogger.Int("worker", writer),
				platformlogger.String("token", "must-not-leak"),
			)
		})
	}
	group.Wait()

	events := capture.Events(t)
	if len(events) != writers {
		t.Fatalf("events = %d, want %d", len(events), writers)
	}
	for _, event := range events {
		if event["message"] != "worker completed" {
			t.Errorf("message = %v, want worker completed", event["message"])
		}
		if event["token"] != platformlogger.DefaultMaskValue {
			t.Errorf("token = %v, want mask", event["token"])
		}
	}
	if output := capture.String(); strings.Contains(output, "must-not-leak") {
		t.Fatalf("captured logs contain a sensitive value: %s", output)
	}
}

func TestLogCaptureEventsReturnsDefensiveSnapshot(t *testing.T) {
	t.Parallel()

	log, capture := testkit.NewLogger(t)
	log.Info("original", platformlogger.String("key", "value"))

	first := capture.Events(t)
	first[0]["key"] = "changed"
	second := capture.Events(t)
	if second[0]["key"] != "value" {
		t.Fatalf("captured event was mutated through snapshot: %v", second[0])
	}
}

func TestLoggerCleanupClearsCapturedBytes(t *testing.T) {
	t.Parallel()

	var capture *testkit.LogCapture
	t.Run("owner", func(t *testing.T) {
		log, ownedCapture := testkit.NewLogger(t)
		capture = ownedCapture
		log.Info("temporary event")
		if capture.String() == "" {
			t.Fatal("logger did not capture event")
		}
	})
	if capture.String() != "" {
		t.Fatalf("logger cleanup retained captured bytes: %s", capture.String())
	}
}
