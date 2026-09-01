package spec

import (
	"fmt"
	"time"
)

func (s *evaluationState) validateRun() {
	if s.run.SchemaVersion != RunSchemaVersion {
		s.integrity = appendViolation(
			s.integrity,
			"unsupported_run_schema",
			fmt.Sprintf("unsupported run schema version %q", s.run.SchemaVersion),
		)
	}
	if s.run.ScenarioID != s.scenario.ID {
		s.integrity = appendViolation(
			s.integrity,
			"scenario_id_mismatch",
			"run scenario_id does not match the evaluated scenario",
		)
	}
	if !contractIDPattern.MatchString(s.run.RunID) {
		s.integrity = appendViolation(
			s.integrity,
			"invalid_run_id",
			"run_id has invalid format",
		)
	}
	validWindow := !s.run.StartedAt.IsZero() &&
		!s.run.EndedAt.IsZero() &&
		s.run.EndedAt.After(s.run.StartedAt) &&
		s.run.EndedAt.After(s.measurementStart)
	if !validWindow {
		s.integrity = appendViolation(
			s.integrity,
			"invalid_run_window",
			"run window must contain a non-empty interval after warm-up",
		)
	}

	s.validatePlannedRequests()
	capacityValid := validWindow && s.validateCapacity()
	if capacityValid {
		s.validateExclusions()
	} else if len(s.run.Exclusions) > 0 {
		s.integrity = appendViolation(
			s.integrity,
			"untrusted_exclusions",
			"exclusions cannot be applied without complete capacity evidence",
		)
	}
	s.validateFaultEvents(validWindow)
}

func (s *evaluationState) validatePlannedRequests() {
	seen := make(map[RequestClass]struct{}, len(s.run.Planned))
	if len(s.run.Planned) != len(orderedClasses()) {
		s.integrity = appendViolation(
			s.integrity,
			"invalid_planned_classes",
			"planned must contain both request classes exactly once",
		)
	}
	for index, planned := range s.run.Planned {
		if !isKnownRequestClass(planned.Class) {
			s.integrity = appendViolation(
				s.integrity,
				"unknown_planned_class",
				fmt.Sprintf("planned[%d] has an unknown class", index),
			)
			continue
		}
		if _, exists := seen[planned.Class]; exists {
			s.integrity = appendViolation(
				s.integrity,
				"duplicate_planned_class",
				fmt.Sprintf("planned[%d] repeats class %s", index, planned.Class),
			)
			continue
		}
		seen[planned.Class] = struct{}{}
		if planned.Count == 0 || planned.Count > maxRequestRecords {
			s.integrity = appendViolation(
				s.integrity,
				"invalid_planned_count",
				fmt.Sprintf("planned[%d].count must be in [1,%d]", index, maxRequestRecords),
			)
		}
		s.planned[planned.Class] = planned.Count
	}
}

func (s *evaluationState) validateCapacity() bool {
	valid := true
	if len(s.run.Capacity) == 0 {
		s.integrity = appendViolation(
			s.integrity,
			"unknown_capacity_interval",
			"capacity does not cover the measured window",
		)
		return false
	}

	expectedStart := s.measurementStart
	for index, interval := range s.run.Capacity {
		if !interval.StartedAt.Equal(expectedStart) {
			valid = false
			s.integrity = appendViolation(
				s.integrity,
				"unknown_capacity_interval",
				fmt.Sprintf("capacity[%d] does not start at the preceding boundary", index),
			)
		}
		validBounds := interval.EndedAt.After(interval.StartedAt) &&
			!interval.StartedAt.Before(s.measurementStart) &&
			!interval.EndedAt.After(s.run.EndedAt)
		if !validBounds {
			valid = false
			s.integrity = appendViolation(
				s.integrity,
				"invalid_capacity_interval",
				fmt.Sprintf("capacity[%d] has invalid bounds", index),
			)
		}
		if interval.PhysicallyAvailableDC > 2 {
			valid = false
			s.integrity = appendViolation(
				s.integrity,
				"invalid_capacity_value",
				fmt.Sprintf("capacity[%d] exceeds the two-DC model", index),
			)
		}
		expectedStart = interval.EndedAt
	}
	if !expectedStart.Equal(s.run.EndedAt) {
		valid = false
		s.integrity = appendViolation(
			s.integrity,
			"unknown_capacity_interval",
			"capacity does not end at the run boundary",
		)
	}
	return valid
}

func (s *evaluationState) validateExclusions() {
	valid := true
	var precedingEnd = s.measurementStart
	for index, interval := range s.run.Exclusions {
		validBounds := interval.EndedAt.After(interval.StartedAt) &&
			!interval.StartedAt.Before(s.measurementStart) &&
			!interval.EndedAt.After(s.run.EndedAt) &&
			!interval.StartedAt.Before(precedingEnd)
		if !validBounds {
			valid = false
			s.integrity = appendViolation(
				s.integrity,
				"invalid_exclusion_interval",
				fmt.Sprintf("exclusions[%d] has invalid or overlapping bounds", index),
			)
		}
		if interval.Reason != ExclusionReasonAllDCPhysicallyUnavailable {
			valid = false
			s.integrity = appendViolation(
				s.integrity,
				"invalid_exclusion_reason",
				fmt.Sprintf("exclusions[%d] has an unsupported reason", index),
			)
		}
		if validBounds && !s.zeroCapacityCovers(interval) {
			valid = false
			s.integrity = appendViolation(
				s.integrity,
				"unproven_all_dc_outage",
				fmt.Sprintf("exclusions[%d] is not fully covered by zero-capacity evidence", index),
			)
		}
		precedingEnd = interval.EndedAt
	}
	if valid {
		s.exclusions = append(s.exclusions, s.run.Exclusions...)
	}
}

func (s *evaluationState) zeroCapacityCovers(exclusion ExclusionInterval) bool {
	cursor := exclusion.StartedAt
	for _, capacity := range s.run.Capacity {
		if !capacity.EndedAt.After(cursor) || !capacity.StartedAt.Before(exclusion.EndedAt) {
			continue
		}
		if capacity.StartedAt.After(cursor) || capacity.PhysicallyAvailableDC != 0 {
			return false
		}
		if capacity.EndedAt.After(cursor) {
			cursor = capacity.EndedAt
		}
		if !cursor.Before(exclusion.EndedAt) {
			return true
		}
	}
	return false
}

func (s *evaluationState) validateFaultEvents(validWindow bool) {
	expected := make(map[string]FaultExpectation, len(s.scenario.Faults))
	for _, fault := range s.scenario.Faults {
		expected[fault.ID] = fault
	}
	for index, event := range s.run.Faults {
		if _, exists := expected[event.ID]; !exists {
			s.integrity = appendViolation(
				s.integrity,
				"unexpected_fault_event",
				fmt.Sprintf("faults[%d] has no scenario expectation", index),
			)
			continue
		}
		if _, exists := s.faultEvents[event.ID]; exists {
			s.integrity = appendViolation(
				s.integrity,
				"duplicate_fault_event",
				fmt.Sprintf("faults[%d] repeats a fault id", index),
			)
			continue
		}
		if validWindow {
			validStart := !event.StartedAt.Before(s.measurementStart) &&
				event.StartedAt.Before(s.run.EndedAt)
			if !validStart {
				s.integrity = appendViolation(
					s.integrity,
					"fault_outside_measured_window",
					fmt.Sprintf("faults[%d] starts outside the measured window", index),
				)
			}
			validEnd := event.EndedAt != nil &&
				event.EndedAt.After(event.StartedAt) &&
				!event.EndedAt.After(s.run.EndedAt)
			if !validEnd {
				s.integrity = appendViolation(
					s.integrity,
					"unknown_fault_interval",
					fmt.Sprintf("faults[%d] has no finite end inside the run", index),
				)
			}
		}
		s.faultEvents[event.ID] = event
	}
	for _, fault := range s.scenario.Faults {
		if _, exists := s.faultEvents[fault.ID]; !exists {
			s.integrity = appendViolation(
				s.integrity,
				"missing_fault_event",
				fmt.Sprintf("expected fault %s is absent", fault.ID),
			)
		}
	}
}

func (s *evaluationState) isExcluded(at time.Time) bool {
	for _, exclusion := range s.exclusions {
		if !at.Before(exclusion.StartedAt) && at.Before(exclusion.EndedAt) {
			return true
		}
	}
	return false
}
