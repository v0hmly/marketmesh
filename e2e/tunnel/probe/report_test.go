package probe

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/v0hmly/marketmesh/e2e/tunnel/spec"
)

func TestBuildReportDelegatesPassingRunToSpec(t *testing.T) {
	t.Parallel()

	scenario, input := passingReportFixture()
	result, err := BuildReport(scenario, input)
	if err != nil {
		t.Fatalf("BuildReport() error = %v", err)
	}
	if result.Report.Status != spec.ReportStatusPass {
		t.Fatalf("Report.Status = %q, checks = %#v", result.Report.Status, result.Report.Checks)
	}
	if !result.Reconciliation.IsComplete || result.Reconciliation.HasIntegrityFault {
		t.Fatalf("Reconciliation = %#v", result.Reconciliation)
	}
	if len(result.Run.Requests) != 2 || len(result.Run.Faults) != 1 {
		t.Fatalf("Run = %#v", result.Run)
	}
	mutation := result.Run.Requests[1].Mutation
	if mutation == nil || !mutation.LedgerKnown || mutation.AppliedCount != 1 {
		t.Fatalf("Mutation = %#v", mutation)
	}
}

func TestBuildReportFailsForDuplicateAndIncompleteCleanup(t *testing.T) {
	t.Parallel()

	scenario, input := passingReportFixture()
	input.CleanupComplete = false
	input.Internal.Records = append(input.Internal.Records, input.Internal.Records[1])
	result, err := BuildReport(scenario, input)
	if err != nil {
		t.Fatalf("BuildReport() error = %v", err)
	}
	if result.Report.Status != spec.ReportStatusFail {
		t.Fatalf("Report.Status = %q, want fail", result.Report.Status)
	}
	assertReportViolation(t, result.Report, "probe/reconciliation", "duplicate_results")
	assertReportViolation(t, result.Report, "probe/cleanup", "cleanup_incomplete")
	if mutation := result.Run.Requests[1].Mutation; mutation == nil || mutation.AppliedCount != 2 {
		t.Fatalf("Mutation = %#v, want two observed applications", mutation)
	}
}

func TestBuildReportFailsClosedForOpenFaultMarker(t *testing.T) {
	t.Parallel()

	scenario, input := passingReportFixture()
	input.Client.Events = input.Client.Events[:1]
	result, err := BuildReport(scenario, input)
	if err != nil {
		t.Fatalf("BuildReport() error = %v", err)
	}
	if result.Report.Status != spec.ReportStatusFail {
		t.Fatalf("Report.Status = %q, want fail", result.Report.Status)
	}
	assertReportViolation(t, result.Report, "probe/markers", "fault_end_marker_invalid")
	assertReportViolation(t, result.Report, "integrity", "unknown_fault_interval")
}

func TestWriteTextReportOmitsRequestAndIdempotencyIDs(t *testing.T) {
	t.Parallel()

	scenario, input := passingReportFixture()
	result, err := BuildReport(scenario, input)
	if err != nil {
		t.Fatalf("BuildReport() error = %v", err)
	}
	var output bytes.Buffer
	if err := WriteTextReport(&output, result.Report); err != nil {
		t.Fatalf("WriteTextReport() error = %v", err)
	}
	if !strings.Contains(output.String(), "status: pass") {
		t.Fatalf("text report = %q", output.String())
	}
	for _, forbidden := range []string{requestID1, requestID2} {
		if strings.Contains(output.String(), forbidden) {
			t.Fatalf("text report contains request data %q", forbidden)
		}
	}
	if err := WriteTextReport(nil, result.Report); err == nil {
		t.Fatal("WriteTextReport(nil) error = nil")
	}
}

func passingReportFixture() (spec.Scenario, ReportInput) {
	startedAt := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
	client := Snapshot{
		StartedAt:      startedAt,
		FinishedOffset: 10 * time.Second,
		Records: []ClientRecord{
			{
				RequestID: requestID1, Class: TrafficClassRead, Sequence: 1,
				ScheduledOffset: time.Second, StartedOffset: time.Second,
				DeadlineOffset: 4 * time.Second, FinishedOffset: 2 * time.Second,
				Latency: time.Second, Outcome: OutcomeSuccess,
				RouteID: FakeReadRoute, DataCenter: DataCenterA, Source: "fake-a-1",
				InternalSequence: 1, CompletionSequence: 5, Dispatched: true,
			},
			{
				RequestID: requestID2, IdempotencyKey: requestID2,
				Class: TrafficClassMutating, Sequence: 1,
				ScheduledOffset: 2 * time.Second, StartedOffset: 2 * time.Second,
				DeadlineOffset: 5 * time.Second, FinishedOffset: 3 * time.Second,
				Latency: time.Second, Outcome: OutcomeSuccess,
				RouteID: FakeMutatingRoute, DataCenter: DataCenterB, Source: "fake-b-1",
				InternalSequence: 1, CompletionSequence: 8, Dispatched: true,
			},
		},
		Events: []Event{
			{
				Sequence: 9, Offset: 4 * time.Second, Kind: EventKindMarker,
				Marker: Marker{
					FaultID: "gateway-in-roll", Component: ComponentGatewayIn,
					Phase: MarkerPhaseStarted, Result: MarkerResultUnknown,
				},
			},
			{
				Sequence: 10, Offset: 6 * time.Second, Kind: EventKindMarker,
				Marker: Marker{
					FaultID: "gateway-in-roll", Component: ComponentGatewayIn,
					Phase: MarkerPhaseRecovered, Result: MarkerResultSuccess,
				},
			},
		},
		IsComplete: true,
	}
	internal := InternalSnapshot{
		Records: []InternalRecord{
			{
				RequestID: requestID1, Class: TrafficClassRead, Sequence: 1,
				Attempts: 1, AcceptedOffset: time.Second,
				CompletedOffset: 1500 * time.Millisecond, Outcome: OutcomeSuccess,
				RouteID: FakeReadRoute, DataCenter: DataCenterA, Source: "fake-a-1",
			},
			{
				RequestID: requestID2, IdempotencyKeySHA256: digestString(requestID2),
				Class: TrafficClassMutating, Sequence: 1, Attempts: 1,
				AcceptedOffset: 2 * time.Second, CompletedOffset: 2500 * time.Millisecond,
				Outcome: OutcomeSuccess, RouteID: FakeMutatingRoute,
				DataCenter: DataCenterB, Source: "fake-b-1",
			},
		},
		IsComplete: true,
	}
	scenario := spec.Scenario{
		SchemaVersion: spec.ScenarioSchemaVersion,
		ID:            "planned-gateway-in",
		Kind:          spec.ScenarioKindPlannedRolling,
		Targets: []spec.ClassTarget{
			{Class: spec.RequestClassReadIdempotent, MinEligible: 1},
			{Class: spec.RequestClassMutating, MinEligible: 1},
		},
		Faults: []spec.FaultExpectation{{
			ID: "gateway-in-roll", Target: spec.FaultTargetGatewayIn,
			Mode: spec.FaultModeRollingUpdate,
		}},
	}
	return scenario, ReportInput{
		RunID: "run-31", Client: client, Internal: internal,
		Capacity: []spec.CapacityInterval{{
			StartedAt: startedAt, EndedAt: startedAt.Add(10 * time.Second),
			PhysicallyAvailableDC: 2,
		}},
		Exclusions:      []spec.ExclusionInterval{},
		CleanupComplete: true,
	}
}

func assertReportViolation(
	t *testing.T,
	report spec.Report,
	checkName string,
	code string,
) {
	t.Helper()
	for _, check := range report.Checks {
		if check.Name != checkName {
			continue
		}
		for _, violation := range check.Violations {
			if violation.Code == code {
				return
			}
		}
		t.Fatalf("check %q violations = %#v, want %q", checkName, check.Violations, code)
	}
	t.Fatalf("report has no check %q", checkName)
}
