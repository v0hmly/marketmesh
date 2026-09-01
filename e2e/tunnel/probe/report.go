package probe

import (
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/v0hmly/marketmesh/e2e/tunnel/spec"
)

// ReportInput contains external evidence that the probe cannot infer. Capacity
// and exclusions remain the published MM-27 types; the scenario owner must
// derive them from the disposable topology rather than from request outcomes.
type ReportInput struct {
	RunID           string
	Client          Snapshot
	Internal        InternalSnapshot
	Capacity        []spec.CapacityInterval
	Exclusions      []spec.ExclusionInterval
	CleanupComplete bool
}

// ReportResult preserves the complete machine-readable run, the safe summary,
// and the reconciliation used to add probe-specific integrity checks.
type ReportResult struct {
	Run            spec.Run
	Report         spec.Report
	Reconciliation Reconciliation
}

// BuildReport converts probe evidence into the published MM-27 run contract
// and delegates all SLO calculations to [spec.Evaluate]. Invalid or incomplete
// probe evidence produces a failed report; only an invalid scenario returns an
// error.
func BuildReport(scenario spec.Scenario, input ReportInput) (ReportResult, error) {
	reconciliation := Reconcile(input.Client, input.Internal)
	run, markerViolations := buildSpecRun(scenario, input, reconciliation)
	report, err := spec.Evaluate(scenario, run)
	if err != nil {
		return ReportResult{}, err
	}

	appendReportCheck(
		&report,
		"probe/reconciliation",
		reconciliationViolations(reconciliation),
	)
	appendReportCheck(&report, "probe/markers", markerViolations)
	cleanupViolations := []spec.Violation{}
	if !input.CleanupComplete {
		cleanupViolations = append(cleanupViolations, spec.Violation{
			Code:    "cleanup_incomplete",
			Message: "disposable E2E cleanup did not complete",
		})
	}
	appendReportCheck(&report, "probe/cleanup", cleanupViolations)

	return ReportResult{
		Run:            run,
		Report:         report,
		Reconciliation: reconciliation,
	}, nil
}

func buildSpecRun(
	scenario spec.Scenario,
	input ReportInput,
	reconciliation Reconciliation,
) (spec.Run, []spec.Violation) {
	startedAt := input.Client.StartedAt
	endedAt := startedAt.Add(input.Client.FinishedOffset)
	planned := map[spec.RequestClass]uint64{
		spec.RequestClassReadIdempotent: 0,
		spec.RequestClassMutating:       0,
	}
	internalByID := make(map[string][]InternalRecord, len(input.Internal.Records))
	for _, record := range input.Internal.Records {
		internalByID[record.RequestID] = append(internalByID[record.RequestID], record)
	}
	invalidRequests := make(map[string]struct{}, len(reconciliation.Invalid))
	for _, requestID := range reconciliation.Invalid {
		invalidRequests[requestID] = struct{}{}
	}

	requests := make([]spec.RequestObservation, 0, len(input.Client.Records))
	for _, record := range input.Client.Records {
		class := specRequestClass(record.Class)
		planned[class]++
		observation := spec.RequestObservation{
			ID:          record.RequestID,
			Class:       class,
			ScheduledAt: startedAt.Add(record.ScheduledOffset),
			Missing:     record.Outcome == OutcomeUnknown,
			Attempts:    []spec.AttemptObservation{},
		}
		if !observation.Missing {
			attemptStarted := observation.ScheduledAt
			if record.Dispatched {
				attemptStarted = startedAt.Add(record.StartedOffset)
			}
			observation.Attempts = append(observation.Attempts, spec.AttemptObservation{
				Number:     1,
				StartedAt:  attemptStarted,
				FinishedAt: startedAt.Add(record.FinishedOffset),
				Outcome:    specAttemptOutcome(record.Outcome),
			})
		}
		if record.Class == TrafficClassMutating {
			matching := internalByID[record.RequestID]
			_, invalid := invalidRequests[record.RequestID]
			ledgerKnown := input.Internal.IsComplete && !invalid && len(matching) > 0
			observation.Mutation = &spec.MutationObservation{
				IdempotencyKey: record.IdempotencyKey,
				LedgerKnown:    ledgerKnown,
				AppliedCount:   appliedMutationCount(matching),
			}
		}
		requests = append(requests, observation)
	}

	faults, markerViolations := specFaultEvents(scenario, input.Client)
	return spec.Run{
		SchemaVersion: spec.RunSchemaVersion,
		ScenarioID:    scenario.ID,
		RunID:         input.RunID,
		StartedAt:     startedAt,
		EndedAt:       endedAt,
		Planned: []spec.PlannedRequests{
			{Class: spec.RequestClassReadIdempotent, Count: planned[spec.RequestClassReadIdempotent]},
			{Class: spec.RequestClassMutating, Count: planned[spec.RequestClassMutating]},
		},
		Capacity:   slices.Clone(input.Capacity),
		Exclusions: slices.Clone(input.Exclusions),
		Faults:     faults,
		Requests:   requests,
	}, markerViolations
}

func specRequestClass(class TrafficClass) spec.RequestClass {
	switch class {
	case TrafficClassRead:
		return spec.RequestClassReadIdempotent
	case TrafficClassMutating:
		return spec.RequestClassMutating
	default:
		return spec.RequestClassUnknown
	}
}

func specAttemptOutcome(outcome Outcome) spec.AttemptOutcome {
	switch outcome {
	case OutcomeSuccess:
		return spec.AttemptOutcomeSuccess
	case OutcomeUnknown:
		return spec.AttemptOutcomeUnknown
	default:
		return spec.AttemptOutcomeFailure
	}
}

func appliedMutationCount(records []InternalRecord) uint32 {
	var count uint32
	for _, record := range records {
		if record.Class == TrafficClassMutating && record.Outcome == OutcomeSuccess {
			count++
		}
	}
	return count
}

func specFaultEvents(
	scenario spec.Scenario,
	client Snapshot,
) ([]spec.FaultEvent, []spec.Violation) {
	markersByID := make(map[string][]Event)
	for _, event := range client.Events {
		if event.Kind != EventKindMarker {
			continue
		}
		markersByID[event.Marker.FaultID] = append(markersByID[event.Marker.FaultID], event)
	}
	expected := make(map[string]struct{}, len(scenario.Faults))
	result := make([]spec.FaultEvent, 0, len(scenario.Faults))
	violations := []spec.Violation{}
	for _, fault := range scenario.Faults {
		expected[fault.ID] = struct{}{}
		markers := markersByID[fault.ID]
		starts := markerEvents(markers, MarkerPhaseStarted, false)
		recovered := markerEvents(markers, MarkerPhaseRecovered, true)
		after := markerEvents(markers, MarkerPhaseAfter, true)
		if len(starts) != 1 {
			violations = append(violations, spec.Violation{
				Code:    "fault_start_marker_invalid",
				Message: fmt.Sprintf("fault %s must have exactly one started marker", fault.ID),
			})
			continue
		}

		startedAt := client.StartedAt.Add(starts[0].Offset)
		event := spec.FaultEvent{ID: fault.ID, StartedAt: startedAt}
		terminal := recovered
		if len(terminal) == 0 {
			terminal = after
		}
		if len(terminal) != 1 {
			violations = append(violations, spec.Violation{
				Code:    "fault_end_marker_invalid",
				Message: fmt.Sprintf("fault %s must have exactly one successful terminal marker", fault.ID),
			})
		} else {
			endedAt := client.StartedAt.Add(terminal[0].Offset)
			if !endedAt.After(startedAt) {
				violations = append(violations, spec.Violation{
					Code:    "fault_marker_order_invalid",
					Message: fmt.Sprintf("fault %s terminal marker must follow its start", fault.ID),
				})
			} else {
				event.EndedAt = &endedAt
			}
		}
		for _, marker := range markers {
			if marker.Marker.Result == MarkerResultFailure {
				violations = append(violations, spec.Violation{
					Code:    "fault_marker_failed",
					Message: fmt.Sprintf("fault %s contains a failed lifecycle marker", fault.ID),
				})
				break
			}
		}
		result = append(result, event)
	}
	for faultID := range markersByID {
		if _, found := expected[faultID]; !found {
			violations = append(violations, spec.Violation{
				Code:    "unexpected_fault_marker",
				Message: "timeline contains a marker for an unexpected fault",
			})
		}
	}

	return result, violations
}

func markerEvents(events []Event, phase MarkerPhase, requireSuccess bool) []Event {
	result := make([]Event, 0, 1)
	for _, event := range events {
		if event.Marker.Phase != phase {
			continue
		}
		if requireSuccess && event.Marker.Result != MarkerResultSuccess {
			continue
		}
		result = append(result, event)
	}
	return result
}

func reconciliationViolations(result Reconciliation) []spec.Violation {
	violations := []spec.Violation{}
	if !result.IsComplete {
		violations = append(violations, spec.Violation{
			Code:    "reconciliation_incomplete",
			Message: fmt.Sprintf("reconciliation has %d incomplete evidence reasons", len(result.IncompleteReasons)),
		})
	}
	violations = appendCountViolation(violations, "missing_internal_results", len(result.Missing))
	violations = appendCountViolation(violations, "lost_client_responses", len(result.LostResponses))
	violations = appendCountViolation(violations, "unexpected_internal_results", len(result.Unexpected))
	violations = appendCountViolation(violations, "duplicate_results", len(result.Duplicate))
	violations = appendCountViolation(violations, "late_results", len(result.Late))
	violations = appendCountViolation(violations, "reordered_results", len(result.Reordered))
	violations = appendCountViolation(violations, "invalid_ledger_records", len(result.Invalid))
	return violations
}

func appendCountViolation(
	violations []spec.Violation,
	code string,
	count int,
) []spec.Violation {
	if count == 0 {
		return violations
	}
	return append(violations, spec.Violation{
		Code:    code,
		Message: fmt.Sprintf("probe observed %d %s", count, strings.ReplaceAll(code, "_", " ")),
	})
}

func appendReportCheck(report *spec.Report, name string, violations []spec.Violation) {
	passed := len(violations) == 0
	report.Checks = append(report.Checks, spec.CheckResult{
		Name: name, Passed: passed, Violations: violations,
	})
	if !passed {
		report.Status = spec.ReportStatusFail
	}
}

// WriteTextReport writes a deterministic human-readable summary without
// request IDs, idempotency keys, topology addresses, payloads, or raw errors.
func WriteTextReport(writer io.Writer, report spec.Report) error {
	if writer == nil {
		return errors.New("probe: text report writer must not be nil")
	}
	if _, err := fmt.Fprintf(
		writer,
		"status: %s\nscenario: %s\nrun: %s\n",
		report.Status,
		report.ScenarioID,
		report.RunID,
	); err != nil {
		return fmt.Errorf("probe: write text report header: %w", err)
	}
	for _, summary := range report.Classes {
		if _, err := fmt.Fprintf(
			writer,
			"class %s: availability_ppm=%d eligible=%d successful=%d failed=%d missing=%d unknown=%d retried=%d\n",
			summary.Class,
			summary.AvailabilityPPM,
			summary.Eligible,
			summary.Successful,
			summary.Failed,
			summary.Missing,
			summary.Unknown,
			summary.Retried,
		); err != nil {
			return fmt.Errorf("probe: write text class summary: %w", err)
		}
	}
	for _, recovery := range report.Recovery {
		if _, err := fmt.Fprintf(
			writer,
			"recovery %s/%s: passed=%t duration=%s maximum=%s\n",
			recovery.FaultID,
			recovery.Class,
			recovery.Passed,
			recovery.Duration,
			recovery.MaxDuration,
		); err != nil {
			return fmt.Errorf("probe: write text recovery summary: %w", err)
		}
	}
	for _, check := range report.Checks {
		status := "pass"
		if !check.Passed {
			status = "fail"
		}
		if _, err := fmt.Fprintf(writer, "check %s: %s\n", check.Name, status); err != nil {
			return fmt.Errorf("probe: write text check: %w", err)
		}
		for _, violation := range check.Violations {
			if _, err := fmt.Fprintf(
				writer,
				"  %s: %s\n",
				violation.Code,
				violation.Message,
			); err != nil {
				return fmt.Errorf("probe: write text violation: %w", err)
			}
		}
	}
	return nil
}
