package spec_test

import (
	"slices"
	"testing"
	"time"

	"github.com/v0hmly/marketmesh/e2e/tunnel/spec"
)

var testStartedAt = time.Date(2026, time.September, 1, 10, 0, 0, 0, time.UTC)

func TestEvaluatePlannedRollingPassesOnlyCompleteZeroDowntimeRun(t *testing.T) {
	t.Parallel()

	scenario := plannedScenario()
	report, err := spec.Evaluate(scenario, passingPlannedRun(scenario))
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if report.Status != spec.ReportStatusPass {
		t.Fatalf("status = %q, want pass; checks = %+v", report.Status, report.Checks)
	}
	if len(report.Downtime) != 0 {
		t.Fatalf("downtime = %+v, want none", report.Downtime)
	}
	for _, summary := range report.Classes {
		if summary.Eligible != 3 || summary.Successful != 3 {
			t.Errorf("class %s counts = eligible %d, successful %d", summary.Class, summary.Eligible, summary.Successful)
		}
		if summary.AvailabilityPPM != 1_000_000 {
			t.Errorf("class %s availability = %d ppm", summary.Class, summary.AvailabilityPPM)
		}
	}
}

func TestEvaluateFailsClosedForIncompleteOrUnknownLedger(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		mutate   func(*spec.Run)
		wantCode string
	}{
		{
			name: "omitted request",
			mutate: func(run *spec.Run) {
				run.Requests = run.Requests[:len(run.Requests)-1]
			},
			wantCode: "planned_recorded_mismatch",
		},
		{
			name: "explicit missing result",
			mutate: func(run *spec.Run) {
				run.Requests[1].Missing = true
				run.Requests[1].Attempts = []spec.AttemptObservation{}
			},
			wantCode: "missing_requests",
		},
		{
			name: "unknown terminal result",
			mutate: func(run *spec.Run) {
				run.Requests[1].Attempts[0].Outcome = spec.AttemptOutcomeUnknown
			},
			wantCode: "unknown_request_results",
		},
		{
			name: "retry cannot hide first failure",
			mutate: func(run *spec.Run) {
				first := run.Requests[1].Attempts[0]
				first.Outcome = spec.AttemptOutcomeFailure
				second := first
				second.Number = 2
				second.StartedAt = first.FinishedAt
				second.FinishedAt = second.StartedAt.Add(100 * time.Millisecond)
				second.Outcome = spec.AttemptOutcomeSuccess
				run.Requests[1].Attempts = []spec.AttemptObservation{first, second}
			},
			wantCode: "error_budget_exhausted",
		},
		{
			name: "capacity gap",
			mutate: func(run *spec.Run) {
				run.Capacity[0].EndedAt = run.EndedAt.Add(-time.Second)
			},
			wantCode: "unknown_capacity_interval",
		},
		{
			name: "unproven exclusion",
			mutate: func(run *spec.Run) {
				run.Exclusions = []spec.ExclusionInterval{
					{
						StartedAt: run.StartedAt.Add(3 * time.Second),
						EndedAt:   run.StartedAt.Add(5 * time.Second),
						Reason:    spec.ExclusionReasonAllDCPhysicallyUnavailable,
					},
				}
			},
			wantCode: "unproven_all_dc_outage",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			scenario := plannedScenario()
			run := passingPlannedRun(scenario)
			test.mutate(&run)

			report, err := spec.Evaluate(scenario, run)
			if err != nil {
				t.Fatalf("Evaluate() error = %v", err)
			}
			if report.Status != spec.ReportStatusFail {
				t.Fatalf("status = %q, want fail", report.Status)
			}
			if !reportHasViolation(report, test.wantCode) {
				t.Fatalf("checks = %+v, want violation %q", report.Checks, test.wantCode)
			}
		})
	}
}

func TestEvaluateRejectsDuplicateOrRetriedMutation(t *testing.T) {
	t.Parallel()

	scenario := plannedScenario()
	run := passingPlannedRun(scenario)
	mutating := requestIndexes(run.Requests, spec.RequestClassMutating)
	run.Requests[mutating[1]].Mutation.IdempotencyKey = run.Requests[mutating[0]].Mutation.IdempotencyKey
	run.Requests[mutating[2]].Mutation.AppliedCount = 2
	first := run.Requests[mutating[2]].Attempts[0]
	second := first
	second.Number = 2
	second.StartedAt = first.FinishedAt
	second.FinishedAt = second.StartedAt.Add(100 * time.Millisecond)
	run.Requests[mutating[2]].Attempts = append(run.Requests[mutating[2]].Attempts, second)

	report, err := spec.Evaluate(scenario, run)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	for _, code := range []string{
		"duplicate_idempotency_key",
		"duplicate_mutation",
		"mutating_retry",
	} {
		if !reportHasViolation(report, code) {
			t.Errorf("checks = %+v, want violation %q", report.Checks, code)
		}
	}
}

func TestEvaluateMeasuresBoundedStableRecovery(t *testing.T) {
	t.Parallel()

	scenario := emergencyScenario()
	run := passingEmergencyRun(scenario)
	report, err := spec.Evaluate(scenario, run)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if report.Status != spec.ReportStatusPass {
		t.Fatalf("status = %q, want pass; checks = %+v", report.Status, report.Checks)
	}
	if len(report.Recovery) != 2 {
		t.Fatalf("recovery count = %d, want 2", len(report.Recovery))
	}
	for _, recovery := range report.Recovery {
		if !recovery.Passed || recovery.RecoveredAt == nil {
			t.Errorf("recovery = %+v, want bounded recovery", recovery)
		}
		if recovery.Duration != spec.Duration(2100*time.Millisecond) {
			t.Errorf("recovery duration = %s, want 2.1s", recovery.Duration)
		}
	}
}

func TestEvaluateFailsRecoveryAfterBound(t *testing.T) {
	t.Parallel()

	scenario := emergencyScenario()
	for index := range scenario.Faults {
		scenario.Faults[index].Recovery.MaxDuration = spec.Duration(2 * time.Second)
	}
	report, err := spec.Evaluate(scenario, passingEmergencyRun(scenario))
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if !reportHasViolation(report, "recovery_time_exceeded") {
		t.Fatalf("checks = %+v, want recovery_time_exceeded", report.Checks)
	}
}

func TestEvaluateExcludesOnlyProvenAllDCOutage(t *testing.T) {
	t.Parallel()

	scenario := emergencyScenario()
	for index := range scenario.Targets {
		scenario.Targets[index].MinEligible = 3
		scenario.Targets[index].MaxErrorRatePPM = 0
	}
	run := passingEmergencyRun(scenario)
	run.Capacity = []spec.CapacityInterval{
		{
			StartedAt:             run.StartedAt.Add(time.Second),
			EndedAt:               run.StartedAt.Add(3 * time.Second),
			PhysicallyAvailableDC: 2,
		},
		{
			StartedAt:             run.StartedAt.Add(3 * time.Second),
			EndedAt:               run.StartedAt.Add(4 * time.Second),
			PhysicallyAvailableDC: 0,
		},
		{
			StartedAt:             run.StartedAt.Add(4 * time.Second),
			EndedAt:               run.EndedAt,
			PhysicallyAvailableDC: 2,
		},
	}
	run.Exclusions = []spec.ExclusionInterval{
		{
			StartedAt: run.StartedAt.Add(3 * time.Second),
			EndedAt:   run.StartedAt.Add(4 * time.Second),
			Reason:    spec.ExclusionReasonAllDCPhysicallyUnavailable,
		},
	}

	report, err := spec.Evaluate(scenario, run)
	if err != nil {
		t.Fatalf("Evaluate() error = %v", err)
	}
	if report.Status != spec.ReportStatusPass {
		t.Fatalf("status = %q, want pass; checks = %+v", report.Status, report.Checks)
	}
	for _, summary := range report.Classes {
		if summary.Eligible != 3 || summary.Missing != 0 {
			t.Errorf("class %s summary = %+v, want excluded outage request", summary.Class, summary)
		}
	}
}

func plannedScenario() spec.Scenario {
	return spec.Scenario{
		SchemaVersion: spec.ScenarioSchemaVersion,
		ID:            "planned-test",
		Kind:          spec.ScenarioKindPlannedRolling,
		WarmUp:        spec.Duration(time.Second),
		Targets: []spec.ClassTarget{
			{
				Class:           spec.RequestClassReadIdempotent,
				MinEligible:     3,
				MaxErrorRatePPM: 0,
				MaxDowntime:     0,
			},
			{
				Class:           spec.RequestClassMutating,
				MinEligible:     3,
				MaxErrorRatePPM: 0,
				MaxDowntime:     0,
			},
		},
		Faults: []spec.FaultExpectation{
			{
				ID:     "rolling-gateway-in",
				Target: spec.FaultTargetGatewayIn,
				Mode:   spec.FaultModeRollingUpdate,
			},
		},
	}
}

func passingPlannedRun(scenario spec.Scenario) spec.Run {
	endedAt := testStartedAt.Add(5 * time.Second)
	run := spec.Run{
		SchemaVersion: spec.RunSchemaVersion,
		ScenarioID:    scenario.ID,
		RunID:         "run-planned",
		StartedAt:     testStartedAt,
		EndedAt:       testStartedAt.Add(10 * time.Second),
		Planned: []spec.PlannedRequests{
			{Class: spec.RequestClassReadIdempotent, Count: 4},
			{Class: spec.RequestClassMutating, Count: 4},
		},
		Capacity: []spec.CapacityInterval{
			{
				StartedAt:             testStartedAt.Add(time.Second),
				EndedAt:               testStartedAt.Add(10 * time.Second),
				PhysicallyAvailableDC: 2,
			},
		},
		Exclusions: []spec.ExclusionInterval{},
		Faults: []spec.FaultEvent{
			{
				ID:        "rolling-gateway-in",
				StartedAt: testStartedAt.Add(3 * time.Second),
				EndedAt:   &endedAt,
			},
		},
		Requests: []spec.RequestObservation{},
	}
	for index, offset := range []time.Duration{
		500 * time.Millisecond,
		2 * time.Second,
		4 * time.Second,
		6 * time.Second,
	} {
		run.Requests = append(
			run.Requests,
			successRequest("read-"+time.Duration(index).String(), spec.RequestClassReadIdempotent, testStartedAt.Add(offset)),
		)
	}
	for index, offset := range []time.Duration{
		700 * time.Millisecond,
		2500 * time.Millisecond,
		4500 * time.Millisecond,
		6500 * time.Millisecond,
	} {
		request := successRequest(
			"mutation-"+time.Duration(index).String(),
			spec.RequestClassMutating,
			testStartedAt.Add(offset),
		)
		request.Mutation = &spec.MutationObservation{
			IdempotencyKey: "key-" + time.Duration(index).String(),
			LedgerKnown:    true,
			AppliedCount:   1,
		}
		run.Requests = append(run.Requests, request)
	}
	return run
}

func emergencyScenario() spec.Scenario {
	return spec.Scenario{
		SchemaVersion: spec.ScenarioSchemaVersion,
		ID:            "emergency-test",
		Kind:          spec.ScenarioKindEmergencyOutage,
		WarmUp:        spec.Duration(time.Second),
		Targets: []spec.ClassTarget{
			{
				Class:           spec.RequestClassReadIdempotent,
				MinEligible:     4,
				MaxErrorRatePPM: 250_000,
				MaxDowntime:     spec.Duration(2 * time.Second),
			},
			{
				Class:           spec.RequestClassMutating,
				MinEligible:     4,
				MaxErrorRatePPM: 250_000,
				MaxDowntime:     spec.Duration(2 * time.Second),
			},
		},
		Faults: []spec.FaultExpectation{
			{
				ID:     "gateway-in-pod",
				Target: spec.FaultTargetGatewayIn,
				Mode:   spec.FaultModePodOutage,
				Recovery: &spec.RecoveryTarget{
					Anchor:        spec.RecoveryAnchorFaultStarted,
					MaxDuration:   spec.Duration(3 * time.Second),
					SuccessStreak: 2,
					Classes: []spec.RequestClass{
						spec.RequestClassReadIdempotent,
						spec.RequestClassMutating,
					},
				},
			},
		},
	}
}

func passingEmergencyRun(scenario spec.Scenario) spec.Run {
	run := passingPlannedRun(plannedScenario())
	run.ScenarioID = scenario.ID
	run.RunID = "run-emergency"
	run.Planned = []spec.PlannedRequests{
		{Class: spec.RequestClassReadIdempotent, Count: 5},
		{Class: spec.RequestClassMutating, Count: 5},
	}
	faultEndedAt := testStartedAt.Add(8 * time.Second)
	run.Faults = []spec.FaultEvent{
		{
			ID:        "gateway-in-pod",
			StartedAt: testStartedAt.Add(3 * time.Second),
			EndedAt:   &faultEndedAt,
		},
	}
	run.Requests = []spec.RequestObservation{}
	for index, offset := range []time.Duration{
		500 * time.Millisecond,
		2 * time.Second,
		3100 * time.Millisecond,
		4 * time.Second,
		5 * time.Second,
	} {
		request := successRequest(
			"read-emergency-"+time.Duration(index).String(),
			spec.RequestClassReadIdempotent,
			testStartedAt.Add(offset),
		)
		if index == 2 {
			request.Attempts[0].Outcome = spec.AttemptOutcomeFailure
		}
		run.Requests = append(run.Requests, request)
	}
	for index, offset := range []time.Duration{
		700 * time.Millisecond,
		2200 * time.Millisecond,
		3100 * time.Millisecond,
		4 * time.Second,
		5 * time.Second,
	} {
		request := successRequest(
			"mutation-emergency-"+time.Duration(index).String(),
			spec.RequestClassMutating,
			testStartedAt.Add(offset),
		)
		request.Mutation = &spec.MutationObservation{
			IdempotencyKey: "emergency-key-" + time.Duration(index).String(),
			LedgerKnown:    true,
			AppliedCount:   1,
		}
		if index == 2 {
			request.Attempts[0].Outcome = spec.AttemptOutcomeFailure
			request.Mutation.AppliedCount = 0
		}
		run.Requests = append(run.Requests, request)
	}
	return run
}

func successRequest(
	id string,
	class spec.RequestClass,
	scheduledAt time.Time,
) spec.RequestObservation {
	return spec.RequestObservation{
		ID:          id,
		Class:       class,
		ScheduledAt: scheduledAt,
		Missing:     false,
		Attempts: []spec.AttemptObservation{
			{
				Number:     1,
				StartedAt:  scheduledAt,
				FinishedAt: scheduledAt.Add(100 * time.Millisecond),
				Outcome:    spec.AttemptOutcomeSuccess,
			},
		},
	}
}

func requestIndexes(
	requests []spec.RequestObservation,
	class spec.RequestClass,
) []int {
	indexes := make([]int, 0)
	for index, request := range requests {
		if request.Class == class {
			indexes = append(indexes, index)
		}
	}
	return indexes
}

func reportHasViolation(report spec.Report, code string) bool {
	for _, check := range report.Checks {
		if slices.ContainsFunc(check.Violations, func(violation spec.Violation) bool {
			return violation.Code == code
		}) {
			return true
		}
	}
	return false
}
