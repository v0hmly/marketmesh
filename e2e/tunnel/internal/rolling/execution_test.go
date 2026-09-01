package rolling

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/v0hmly/marketmesh/e2e/tunnel/probe"
	"github.com/v0hmly/marketmesh/e2e/tunnel/spec"
)

func TestExecutePublishesPassingAtomicBundle(t *testing.T) {
	t.Parallel()

	client, internal, scenario := passingExecutionEvidence()
	traffic := &trafficRunnerStub{snapshot: client}
	archive := newLedgerArchiveStub(internal)
	artifactDirectory := filepath.Join(t.TempDir(), "mm34-artifacts")
	closed := false
	result, err := Execute(
		t.Context(),
		validExecutionConfig(scenario, artifactDirectory),
		traffic,
		archive,
		func() { closed = true },
		func(context.Context) error { return nil },
	)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result.Report.Status != spec.ReportStatusPass {
		t.Fatalf("report status = %s", result.Report.Status)
	}
	if !closed {
		t.Fatal("traffic closer was not called")
	}
	for _, name := range []string{
		probe.RunArtifactName,
		probe.JSONReportArtifactName,
		probe.JUnitArtifactName,
		probe.TextReportArtifactName,
	} {
		if _, err := os.Stat(filepath.Join(artifactDirectory, name)); err != nil {
			t.Fatalf("artifact %s: %v", name, err)
		}
	}
}

func TestExecutePublishesFailedEvidenceAndReturnsCause(t *testing.T) {
	t.Parallel()

	client, internal, scenario := passingExecutionEvidence()
	want := errors.New("rolling action failed")
	artifactDirectory := filepath.Join(t.TempDir(), "failed-artifacts")
	result, err := Execute(
		t.Context(),
		validExecutionConfig(scenario, artifactDirectory),
		&trafficRunnerStub{snapshot: client},
		newLedgerArchiveStub(internal),
		func() {},
		func(context.Context) error { return want },
	)
	if !errors.Is(err, want) {
		t.Fatalf("Execute() error = %v, want wrapped cause", err)
	}
	if result.Report.Status != spec.ReportStatusFail {
		t.Fatalf("report status = %s, want fail", result.Report.Status)
	}
	if _, statErr := os.Stat(filepath.Join(artifactDirectory, probe.RunArtifactName)); statErr != nil {
		t.Fatalf("failed run artifact: %v", statErr)
	}
}

func TestExecuteFailsBeforeTrafficWhenArchiveIsNotReady(t *testing.T) {
	t.Parallel()

	_, _, scenario := passingExecutionEvidence()
	want := errors.New("archive discovery failed")
	archive := newLedgerArchiveStub(probe.InternalSnapshot{})
	archive.readyErr = want
	traffic := &trafficRunnerStub{}
	_, err := Execute(
		t.Context(),
		validExecutionConfig(scenario, filepath.Join(t.TempDir(), "artifacts")),
		traffic,
		archive,
		func() {},
		func(context.Context) error { return nil },
	)
	if !errors.Is(err, want) {
		t.Fatalf("Execute() error = %v, want wrapped archive error", err)
	}
	if traffic.called {
		t.Fatal("traffic started before archive was ready")
	}
}

func TestExecuteRejectsExistingArtifactTargetBeforeStartingDependencies(t *testing.T) {
	t.Parallel()

	_, internal, scenario := passingExecutionEvidence()
	artifactDirectory := filepath.Join(t.TempDir(), "existing-artifacts")
	if err := os.Mkdir(artifactDirectory, 0o700); err != nil {
		t.Fatalf("os.Mkdir() error = %v", err)
	}
	traffic := &trafficRunnerStub{}
	archive := newLedgerArchiveStub(internal)
	actionCalled := false
	_, err := Execute(
		t.Context(),
		validExecutionConfig(scenario, artifactDirectory),
		traffic,
		archive,
		func() {},
		func(context.Context) error {
			actionCalled = true
			return nil
		},
	)
	if !errors.Is(err, os.ErrExist) {
		t.Fatalf("Execute() error = %v, want existing target", err)
	}
	if traffic.called || actionCalled {
		t.Fatal("execution dependencies started for an existing artifact target")
	}
}

func validExecutionConfig(
	scenario spec.Scenario,
	artifactDirectory string,
) ExecutionConfig {
	return ExecutionConfig{
		RunID:             "mm34-run",
		Scenario:          scenario,
		ArtifactDirectory: artifactDirectory,
		TotalTimeout:      time.Minute,
		StartupTimeout:    time.Second,
		StopTimeout:       time.Second,
	}
}

func passingExecutionEvidence() (probe.Snapshot, probe.InternalSnapshot, spec.Scenario) {
	startedAt := time.Now().UTC()
	readID := "11111111111111111111111111111111"
	mutatingID := "22222222222222222222222222222222"
	digest := sha256.Sum256([]byte(mutatingID))
	client := probe.Snapshot{
		StartedAt:      startedAt,
		FinishedOffset: 100 * time.Millisecond,
		IsComplete:     true,
		Records: []probe.ClientRecord{
			{
				RequestID: readID, Class: probe.TrafficClassRead, Sequence: 1,
				ScheduledOffset: 10 * time.Millisecond, StartedOffset: 11 * time.Millisecond,
				DeadlineOffset: 61 * time.Millisecond, FinishedOffset: 20 * time.Millisecond,
				Latency: 9 * time.Millisecond, Outcome: probe.OutcomeSuccess,
				RouteID: probe.FakeReadRoute, DataCenter: probe.DataCenterA,
				Source: "fake-a-1", InternalSequence: 1, CompletionSequence: 1, Dispatched: true,
			},
			{
				RequestID: mutatingID, IdempotencyKey: mutatingID,
				Class: probe.TrafficClassMutating, Sequence: 1,
				ScheduledOffset: 30 * time.Millisecond, StartedOffset: 31 * time.Millisecond,
				DeadlineOffset: 81 * time.Millisecond, FinishedOffset: 40 * time.Millisecond,
				Latency: 9 * time.Millisecond, Outcome: probe.OutcomeSuccess,
				RouteID: probe.FakeMutatingRoute, DataCenter: probe.DataCenterB,
				Source: "fake-b-1", InternalSequence: 1, CompletionSequence: 2, Dispatched: true,
			},
		},
		Events: []probe.Event{
			{
				Sequence: 1, Offset: 20 * time.Millisecond, Kind: probe.EventKindMarker,
				Marker: probe.Marker{
					FaultID: "mm34-test-fault", Phase: probe.MarkerPhaseStarted,
					Result: probe.MarkerResultUnknown,
				},
			},
			{
				Sequence: 2, Offset: 80 * time.Millisecond, Kind: probe.EventKindMarker,
				Marker: probe.Marker{
					FaultID: "mm34-test-fault", Phase: probe.MarkerPhaseRecovered,
					Result: probe.MarkerResultSuccess,
				},
			},
		},
	}
	internal := probe.InternalSnapshot{
		IsComplete: true,
		Records: []probe.InternalRecord{
			{
				RequestID: readID, Class: probe.TrafficClassRead, Sequence: 1, Attempts: 1,
				Outcome: probe.OutcomeSuccess, RouteID: probe.FakeReadRoute,
				DataCenter: probe.DataCenterA, Source: "fake-a-1",
			},
			{
				RequestID: mutatingID, IdempotencyKeySHA256: hex.EncodeToString(digest[:]),
				Class: probe.TrafficClassMutating, Sequence: 1, Attempts: 1,
				Outcome: probe.OutcomeSuccess, RouteID: probe.FakeMutatingRoute,
				DataCenter: probe.DataCenterB, Source: "fake-b-1",
			},
		},
	}
	scenario := spec.Scenario{
		SchemaVersion: spec.ScenarioSchemaVersion,
		ID:            "mm34-execution-test",
		Kind:          spec.ScenarioKindPlannedRolling,
		Targets: []spec.ClassTarget{
			{Class: spec.RequestClassReadIdempotent, MinEligible: 1},
			{Class: spec.RequestClassMutating, MinEligible: 1},
		},
		Faults: []spec.FaultExpectation{{
			ID: "mm34-test-fault", Target: spec.FaultTargetGatewayIn,
			Mode: spec.FaultModeRollingUpdate,
		}},
	}

	return client, internal, scenario
}

type trafficRunnerStub struct {
	snapshot probe.Snapshot
	err      error
	called   bool
}

func (runner *trafficRunnerStub) Run(ctx context.Context) (probe.Snapshot, error) {
	runner.called = true
	<-ctx.Done()
	return runner.snapshot, runner.err
}

type ledgerArchiveStub struct {
	ready    chan struct{}
	snapshot probe.InternalSnapshot
	readyErr error
	runErr   error
}

func newLedgerArchiveStub(snapshot probe.InternalSnapshot) *ledgerArchiveStub {
	return &ledgerArchiveStub{ready: make(chan struct{}), snapshot: snapshot}
}

func (archive *ledgerArchiveStub) Run(ctx context.Context) error {
	close(archive.ready)
	<-ctx.Done()
	return archive.runErr
}

func (archive *ledgerArchiveStub) WaitReady(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-archive.ready:
		return archive.readyErr
	}
}

func (archive *ledgerArchiveStub) Snapshot() probe.InternalSnapshot {
	return archive.snapshot
}
