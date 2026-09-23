//go:build integration

package main

import (
	"io"
	"strings"
	"testing"
	"time"

	"github.com/v0hmly/marketmesh/e2e/tunnel/spec"
)

func TestConfiguredRecordCapacityFitsCanonicalRunArtifact(t *testing.T) {
	startedAt := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	request := spec.RequestObservation{
		ID:          strings.Repeat("f", 64),
		Class:       spec.RequestClassMutating,
		ScheduledAt: startedAt,
		Attempts: []spec.AttemptObservation{{
			Number: 1, StartedAt: startedAt,
			FinishedAt: startedAt.Add(requestTimeout),
			Outcome:    spec.AttemptOutcomeFailure,
		}},
		Mutation: &spec.MutationObservation{
			IdempotencyKey: strings.Repeat("f", 64),
			LedgerKnown:    true, AppliedCount: 1,
		},
	}
	run := spec.Run{
		SchemaVersion: spec.RunSchemaVersion,
		Requests:      make([]spec.RequestObservation, recordCapacity),
	}
	for index := range run.Requests {
		run.Requests[index] = request
	}
	if err := spec.WriteRun(io.Discard, run); err != nil {
		t.Fatalf("configured maximum run exceeds canonical artifact budget: %v", err)
	}
}
