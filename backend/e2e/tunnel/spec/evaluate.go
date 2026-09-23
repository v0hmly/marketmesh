package spec

import (
	"fmt"
	"math/bits"
	"regexp"
	"slices"
	"time"
)

var opaqueIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

type evaluatedRequest struct {
	index      int
	class      RequestClass
	scheduled  time.Time
	finished   time.Time
	eligible   bool
	successful bool
	missing    bool
	unknown    bool
	retried    bool
}

type evaluationState struct {
	scenario         Scenario
	run              Run
	measurementStart time.Time
	targets          map[RequestClass]ClassTarget
	planned          map[RequestClass]uint64
	recorded         map[RequestClass]uint64
	requests         map[RequestClass][]evaluatedRequest
	faultEvents      map[string]FaultEvent
	exclusions       []ExclusionInterval
	integrity        []Violation
	mutationSafety   []Violation
}

// Evaluate applies the scenario contract to a complete run ledger. A malformed
// ledger produces a failed report; only an invalid scenario returns an error.
func Evaluate(scenario Scenario, run Run) (Report, error) {
	if err := ValidateScenario(scenario); err != nil {
		return Report{}, err
	}

	state := newEvaluationState(scenario, run)
	state.validateRun()
	state.evaluateRequests()

	report := Report{
		SchemaVersion: ReportSchemaVersion,
		ScenarioID:    scenario.ID,
		RunID:         run.RunID,
		StartedAt:     run.StartedAt,
		EndedAt:       run.EndedAt,
		MeasuredAt:    state.measurementStart,
		Status:        ReportStatusPass,
		Classes:       make([]ClassSummary, 0, len(scenario.Targets)),
		Downtime:      []DowntimeWindow{},
		Recovery:      []RecoverySummary{},
		Checks: []CheckResult{
			newCheck("integrity", state.integrity),
			newCheck("mutation_safety", state.mutationSafety),
		},
	}

	for _, class := range orderedClasses() {
		summary, downtime, violations := state.classResult(class)
		report.Classes = append(report.Classes, summary)
		report.Downtime = append(report.Downtime, downtime...)
		report.Checks = append(
			report.Checks,
			newCheck("class/"+string(class), violations),
		)
	}

	recovery, recoveryChecks := state.recoveryResults()
	report.Recovery = append(report.Recovery, recovery...)
	report.Checks = append(report.Checks, recoveryChecks...)
	for _, check := range report.Checks {
		if !check.Passed {
			report.Status = ReportStatusFail
			break
		}
	}
	return report, nil
}

func newEvaluationState(scenario Scenario, run Run) *evaluationState {
	targets := make(map[RequestClass]ClassTarget, len(scenario.Targets))
	for _, target := range scenario.Targets {
		targets[target.Class] = target
	}
	return &evaluationState{
		scenario:         scenario,
		run:              run,
		measurementStart: run.StartedAt.Add(scenario.WarmUp.Value()),
		targets:          targets,
		planned:          make(map[RequestClass]uint64, len(scenario.Targets)),
		recorded:         make(map[RequestClass]uint64, len(scenario.Targets)),
		requests:         make(map[RequestClass][]evaluatedRequest, len(scenario.Targets)),
		faultEvents:      make(map[string]FaultEvent, len(run.Faults)),
		exclusions:       []ExclusionInterval{},
		integrity:        []Violation{},
		mutationSafety:   []Violation{},
	}
}

func (s *evaluationState) evaluateRequests() {
	seenRequestIDs := make(map[string]struct{}, len(s.run.Requests))
	seenIdempotencyKeys := make(map[string]struct{}, len(s.run.Requests))
	for index, request := range s.run.Requests {
		if !isKnownRequestClass(request.Class) {
			s.integrity = appendViolation(
				s.integrity,
				"unknown_request_class",
				fmt.Sprintf("requests[%d] has an unknown class", index),
			)
			continue
		}
		s.recorded[request.Class]++
		result := evaluatedRequest{
			index:     index,
			class:     request.Class,
			scheduled: request.ScheduledAt,
			missing:   request.Missing,
		}

		if !opaqueIDPattern.MatchString(request.ID) {
			s.integrity = appendViolation(
				s.integrity,
				"invalid_request_id",
				fmt.Sprintf("requests[%d] has an invalid opaque id", index),
			)
		}
		if _, exists := seenRequestIDs[request.ID]; exists {
			s.integrity = appendViolation(
				s.integrity,
				"duplicate_request_id",
				fmt.Sprintf("requests[%d] repeats a request id", index),
			)
		}
		seenRequestIDs[request.ID] = struct{}{}

		validSchedule := s.validateRequestSchedule(index, request.ScheduledAt)
		result.eligible = validSchedule &&
			!request.ScheduledAt.Before(s.measurementStart) &&
			request.ScheduledAt.Before(s.run.EndedAt) &&
			!s.isExcluded(request.ScheduledAt)

		s.evaluateAttempts(index, request, &result)
		s.evaluateMutation(index, request, result.successful, seenIdempotencyKeys)
		s.requests[request.Class] = append(s.requests[request.Class], result)
	}

	for _, class := range orderedClasses() {
		if s.recorded[class] != s.planned[class] {
			s.integrity = appendViolation(
				s.integrity,
				"planned_recorded_mismatch",
				fmt.Sprintf(
					"class %s planned %d requests but recorded %d",
					class,
					s.planned[class],
					s.recorded[class],
				),
			)
		}
	}
}

func (s *evaluationState) validateRequestSchedule(index int, scheduled time.Time) bool {
	if scheduled.IsZero() || scheduled.Before(s.run.StartedAt) || !scheduled.Before(s.run.EndedAt) {
		s.integrity = appendViolation(
			s.integrity,
			"request_outside_run",
			fmt.Sprintf("requests[%d] is scheduled outside the run", index),
		)
		return false
	}
	return true
}

func (s *evaluationState) evaluateAttempts(
	index int,
	request RequestObservation,
	result *evaluatedRequest,
) {
	if request.Missing {
		if len(request.Attempts) != 0 {
			s.integrity = appendViolation(
				s.integrity,
				"missing_request_has_attempts",
				fmt.Sprintf("requests[%d] is missing but contains attempts", index),
			)
		}
		return
	}
	if len(request.Attempts) == 0 {
		result.unknown = true
		s.integrity = appendViolation(
			s.integrity,
			"request_without_result",
			fmt.Sprintf("requests[%d] has no terminal attempt", index),
		)
		return
	}

	result.retried = len(request.Attempts) > 1
	allAttemptsValid := true
	allAttemptsSuccessful := true
	for attemptIndex, attempt := range request.Attempts {
		if attempt.Number != uint32(attemptIndex+1) {
			allAttemptsValid = false
			s.integrity = appendViolation(
				s.integrity,
				"invalid_attempt_sequence",
				fmt.Sprintf("requests[%d].attempts[%d] has a non-sequential number", index, attemptIndex),
			)
		}
		validTiming := !attempt.StartedAt.IsZero() &&
			!attempt.FinishedAt.IsZero() &&
			!attempt.StartedAt.Before(request.ScheduledAt) &&
			!attempt.FinishedAt.Before(attempt.StartedAt) &&
			!attempt.FinishedAt.After(s.run.EndedAt)
		if !validTiming {
			allAttemptsValid = false
			s.integrity = appendViolation(
				s.integrity,
				"invalid_attempt_timing",
				fmt.Sprintf("requests[%d].attempts[%d] has invalid timing", index, attemptIndex),
			)
		}
		if attempt.Outcome == AttemptOutcomeUnknown {
			result.unknown = true
		}
		if attempt.Outcome != AttemptOutcomeSuccess &&
			attempt.Outcome != AttemptOutcomeFailure &&
			attempt.Outcome != AttemptOutcomeUnknown {
			allAttemptsValid = false
			result.unknown = true
			s.integrity = appendViolation(
				s.integrity,
				"unknown_attempt_outcome",
				fmt.Sprintf("requests[%d].attempts[%d] has an unknown outcome", index, attemptIndex),
			)
		}
		if attempt.Outcome != AttemptOutcomeSuccess {
			allAttemptsSuccessful = false
		}
		if attempt.FinishedAt.After(result.finished) {
			result.finished = attempt.FinishedAt
		}
	}

	// A retry is always visible as a failed logical SLO sample. This prevents a
	// later successful attempt from hiding the first transport failure.
	result.successful = allAttemptsValid && allAttemptsSuccessful && len(request.Attempts) == 1
}

func (s *evaluationState) evaluateMutation(
	index int,
	request RequestObservation,
	requestSuccessful bool,
	seenKeys map[string]struct{},
) {
	if request.Class != RequestClassMutating {
		if request.Mutation != nil {
			s.mutationSafety = appendViolation(
				s.mutationSafety,
				"mutation_ledger_on_read",
				fmt.Sprintf("requests[%d] is read-only but has mutation data", index),
			)
		}
		return
	}
	if request.Mutation == nil {
		s.mutationSafety = appendViolation(
			s.mutationSafety,
			"missing_mutation_ledger",
			fmt.Sprintf("requests[%d] has no mutation ledger", index),
		)
		return
	}
	mutation := request.Mutation
	if !opaqueIDPattern.MatchString(mutation.IdempotencyKey) {
		s.mutationSafety = appendViolation(
			s.mutationSafety,
			"invalid_idempotency_key",
			fmt.Sprintf("requests[%d] has an invalid opaque idempotency key", index),
		)
	}
	if _, exists := seenKeys[mutation.IdempotencyKey]; exists {
		s.mutationSafety = appendViolation(
			s.mutationSafety,
			"duplicate_idempotency_key",
			fmt.Sprintf("requests[%d] repeats an idempotency key", index),
		)
	}
	seenKeys[mutation.IdempotencyKey] = struct{}{}
	if len(request.Attempts) > 1 {
		s.mutationSafety = appendViolation(
			s.mutationSafety,
			"mutating_retry",
			fmt.Sprintf("requests[%d] contains more than one mutating attempt", index),
		)
	}
	if !mutation.LedgerKnown {
		s.mutationSafety = appendViolation(
			s.mutationSafety,
			"unknown_mutation_ledger",
			fmt.Sprintf("requests[%d] has an unknown internal ledger result", index),
		)
	}
	if mutation.AppliedCount > 1 {
		s.mutationSafety = appendViolation(
			s.mutationSafety,
			"duplicate_mutation",
			fmt.Sprintf("requests[%d] was applied more than once", index),
		)
	}
	if requestSuccessful && mutation.AppliedCount != 1 {
		s.mutationSafety = appendViolation(
			s.mutationSafety,
			"acknowledged_mutation_not_applied_once",
			fmt.Sprintf("requests[%d] succeeded but was not applied exactly once", index),
		)
	}
}

func (s *evaluationState) classResult(
	class RequestClass,
) (ClassSummary, []DowntimeWindow, []Violation) {
	target := s.targets[class]
	requests := slices.Clone(s.requests[class])
	slices.SortStableFunc(requests, func(left, right evaluatedRequest) int {
		if comparison := left.scheduled.Compare(right.scheduled); comparison != 0 {
			return comparison
		}
		return left.index - right.index
	})

	summary := ClassSummary{
		Class:    class,
		Planned:  s.planned[class],
		Recorded: s.recorded[class],
		ErrorBudget: ErrorBudgetSummary{
			RatePPM: target.MaxErrorRatePPM,
		},
	}
	eligibleRequests := make([]evaluatedRequest, 0, len(requests))
	for _, request := range requests {
		if !request.eligible {
			continue
		}
		eligibleRequests = append(eligibleRequests, request)
		summary.Eligible++
		if request.successful {
			summary.Successful++
		} else {
			summary.Failed++
		}
		if request.missing {
			summary.Missing++
		}
		if request.unknown {
			summary.Unknown++
		}
		if request.retried {
			summary.Retried++
		}
	}
	if summary.Eligible > 0 {
		summary.AvailabilityPPM = ratioPPM(summary.Successful, summary.Eligible)
	}
	summary.ErrorBudget.Allowed = allowedFailures(summary.Eligible, target.MaxErrorRatePPM)
	summary.ErrorBudget.Consumed = summary.Failed
	if summary.ErrorBudget.Allowed > summary.ErrorBudget.Consumed {
		summary.ErrorBudget.Remaining = summary.ErrorBudget.Allowed - summary.ErrorBudget.Consumed
	}

	violations := make([]Violation, 0)
	if summary.Eligible < target.MinEligible {
		violations = appendViolation(
			violations,
			"insufficient_eligible_requests",
			fmt.Sprintf(
				"class %s has %d eligible requests, minimum is %d",
				class,
				summary.Eligible,
				target.MinEligible,
			),
		)
	}
	if summary.Failed > summary.ErrorBudget.Allowed {
		violations = appendViolation(
			violations,
			"error_budget_exhausted",
			fmt.Sprintf(
				"class %s consumed %d failures, allowed %d",
				class,
				summary.Failed,
				summary.ErrorBudget.Allowed,
			),
		)
	}
	if summary.Missing > 0 {
		violations = appendViolation(
			violations,
			"missing_requests",
			fmt.Sprintf("class %s has %d missing requests", class, summary.Missing),
		)
	}
	if summary.Unknown > 0 {
		violations = appendViolation(
			violations,
			"unknown_request_results",
			fmt.Sprintf("class %s has %d unknown results", class, summary.Unknown),
		)
	}

	downtime := downtimeWindows(class, eligibleRequests, s.run.EndedAt)
	for _, window := range downtime {
		if window.Duration > target.MaxDowntime {
			violations = appendViolation(
				violations,
				"downtime_exceeded",
				fmt.Sprintf(
					"class %s downtime %s exceeds %s",
					class,
					window.Duration,
					target.MaxDowntime,
				),
			)
		}
	}
	return summary, downtime, violations
}

func downtimeWindows(
	class RequestClass,
	requests []evaluatedRequest,
	runEndedAt time.Time,
) []DowntimeWindow {
	windows := make([]DowntimeWindow, 0)
	var openedAt time.Time
	for _, request := range requests {
		if !request.successful {
			if openedAt.IsZero() {
				openedAt = request.scheduled
			}
			continue
		}
		if openedAt.IsZero() {
			continue
		}
		endedAt := request.finished
		if endedAt.Before(openedAt) {
			endedAt = openedAt
		}
		windows = append(windows, DowntimeWindow{
			Class:     class,
			StartedAt: openedAt,
			EndedAt:   endedAt,
			Duration:  Duration(endedAt.Sub(openedAt)),
		})
		openedAt = time.Time{}
	}
	if !openedAt.IsZero() {
		endedAt := runEndedAt
		if endedAt.Before(openedAt) {
			endedAt = openedAt
		}
		windows = append(windows, DowntimeWindow{
			Class:     class,
			StartedAt: openedAt,
			EndedAt:   endedAt,
			Duration:  Duration(endedAt.Sub(openedAt)),
		})
	}
	return windows
}

func allowedFailures(eligible uint64, ratePPM uint32) uint64 {
	high, low := bits.Mul64(eligible, uint64(ratePPM))
	allowed, _ := bits.Div64(high, low, partsPerMillion)
	return allowed
}

func ratioPPM(successful uint64, eligible uint64) uint64 {
	high, low := bits.Mul64(successful, partsPerMillion)
	ratio, _ := bits.Div64(high, low, eligible)
	return ratio
}

func orderedClasses() []RequestClass {
	return []RequestClass{
		RequestClassReadIdempotent,
		RequestClassMutating,
	}
}

func newCheck(name string, violations []Violation) CheckResult {
	return CheckResult{
		Name:       name,
		Passed:     len(violations) == 0,
		Violations: violations,
	}
}

func appendViolation(
	violations []Violation,
	code string,
	message string,
) []Violation {
	return append(violations, Violation{Code: code, Message: message})
}
