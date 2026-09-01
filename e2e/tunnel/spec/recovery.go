package spec

import (
	"fmt"
	"slices"
	"time"
)

func (s *evaluationState) recoveryResults() ([]RecoverySummary, []CheckResult) {
	summaries := make([]RecoverySummary, 0)
	checks := make([]CheckResult, 0)
	for _, expectation := range s.scenario.Faults {
		if expectation.Recovery == nil {
			continue
		}
		for _, class := range expectation.Recovery.Classes {
			summary, violations := s.recoveryResult(expectation, class)
			summaries = append(summaries, summary)
			checks = append(
				checks,
				newCheck("recovery/"+expectation.ID+"/"+string(class), violations),
			)
		}
	}
	return summaries, checks
}

func (s *evaluationState) recoveryResult(
	expectation FaultExpectation,
	class RequestClass,
) (RecoverySummary, []Violation) {
	recovery := expectation.Recovery
	summary := RecoverySummary{
		FaultID:     expectation.ID,
		Target:      expectation.Target,
		Class:       class,
		MaxDuration: recovery.MaxDuration,
	}
	violations := make([]Violation, 0)
	event, exists := s.faultEvents[expectation.ID]
	if !exists {
		violations = appendViolation(
			violations,
			"recovery_event_missing",
			fmt.Sprintf("fault %s has no event", expectation.ID),
		)
		return summary, violations
	}

	anchor, anchorKnown := recoveryAnchorTime(event, recovery.Anchor)
	if !anchorKnown || anchor.IsZero() {
		violations = appendViolation(
			violations,
			"recovery_anchor_unknown",
			fmt.Sprintf("fault %s has no recovery anchor", expectation.ID),
		)
		return summary, violations
	}
	summary.AnchorAt = anchor

	recoveredAt, recovered := s.findStableRecovery(
		class,
		anchor,
		recovery.SuccessStreak,
	)
	if recovered {
		duration := recoveredAt.Sub(anchor)
		if duration < 0 {
			duration = 0
		}
		summary.RecoveredAt = &recoveredAt
		summary.Duration = Duration(duration)
		summary.Passed = duration <= recovery.MaxDuration.Value()
	} else {
		duration := s.run.EndedAt.Sub(anchor)
		if duration < 0 {
			duration = 0
		}
		summary.Duration = Duration(duration)
	}

	if !recovered {
		violations = appendViolation(
			violations,
			"recovery_not_observed",
			fmt.Sprintf(
				"fault %s class %s did not reach a stable success streak",
				expectation.ID,
				class,
			),
		)
	} else if !summary.Passed {
		violations = appendViolation(
			violations,
			"recovery_time_exceeded",
			fmt.Sprintf(
				"fault %s class %s recovered in %s, maximum is %s",
				expectation.ID,
				class,
				summary.Duration,
				summary.MaxDuration,
			),
		)
	}
	return summary, violations
}

func recoveryAnchorTime(event FaultEvent, anchor RecoveryAnchor) (time.Time, bool) {
	switch anchor {
	case RecoveryAnchorFaultStarted:
		return event.StartedAt, !event.StartedAt.IsZero()
	case RecoveryAnchorFaultEnded:
		if event.EndedAt == nil {
			return time.Time{}, false
		}
		return *event.EndedAt, !event.EndedAt.IsZero()
	default:
		return time.Time{}, false
	}
}

func (s *evaluationState) findStableRecovery(
	class RequestClass,
	anchor time.Time,
	requiredStreak uint32,
) (time.Time, bool) {
	requests := slices.Clone(s.requests[class])
	slices.SortStableFunc(requests, func(left, right evaluatedRequest) int {
		if comparison := left.scheduled.Compare(right.scheduled); comparison != 0 {
			return comparison
		}
		return left.index - right.index
	})

	var streak uint32
	var recoveredAt time.Time
	for _, request := range requests {
		if !request.eligible || request.scheduled.Before(anchor) {
			continue
		}
		if !request.successful {
			streak = 0
			recoveredAt = time.Time{}
			continue
		}
		streak++
		if request.finished.After(recoveredAt) {
			recoveredAt = request.finished
		}
		if streak >= requiredStreak {
			return recoveredAt, true
		}
	}
	return time.Time{}, false
}
