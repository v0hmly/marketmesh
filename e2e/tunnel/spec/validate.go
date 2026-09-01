package spec

import (
	"fmt"
	"regexp"
	"strings"
	"time"
)

const (
	maxScenarioDuration = 24 * time.Hour
	maxRequestRecords   = uint64(10_000_000)
	maxRecoveryStreak   = uint32(10_000)
)

var contractIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// ValidationError reports every independently invalid scenario field.
type ValidationError struct {
	Problems []string
}

// Error implements error.
func (e *ValidationError) Error() string {
	return "spec: invalid scenario: " + strings.Join(e.Problems, "; ")
}

// ValidateScenario checks the complete scenario before any ledger is evaluated.
func ValidateScenario(scenario Scenario) error {
	problems := make([]string, 0)
	if scenario.SchemaVersion != ScenarioSchemaVersion {
		problems = append(
			problems,
			fmt.Sprintf("unsupported schema version %q", scenario.SchemaVersion),
		)
	}
	if !contractIDPattern.MatchString(scenario.ID) {
		problems = append(problems, "id must match [a-z0-9][a-z0-9._-]{0,63}")
	}
	if !isKnownScenarioKind(scenario.Kind) {
		problems = append(problems, fmt.Sprintf("unknown scenario kind %q", scenario.Kind))
	}
	if scenario.WarmUp.Value() > maxScenarioDuration {
		problems = append(problems, "warm_up exceeds 24h")
	}

	targetClasses := validateTargets(scenario, &problems)
	validateFaults(scenario, targetClasses, &problems)
	if len(problems) > 0 {
		return &ValidationError{Problems: problems}
	}
	return nil
}

func validateTargets(scenario Scenario, problems *[]string) map[RequestClass]struct{} {
	targetClasses := make(map[RequestClass]struct{}, 2)
	if len(scenario.Targets) != 2 {
		*problems = append(*problems, "targets must contain read_idempotent and mutating exactly once")
	}
	for index, target := range scenario.Targets {
		if !isKnownRequestClass(target.Class) {
			*problems = append(
				*problems,
				fmt.Sprintf("targets[%d] has unknown class %q", index, target.Class),
			)
			continue
		}
		if _, exists := targetClasses[target.Class]; exists {
			*problems = append(
				*problems,
				fmt.Sprintf("targets contains duplicate class %q", target.Class),
			)
		}
		targetClasses[target.Class] = struct{}{}
		if target.MinEligible == 0 || target.MinEligible > maxRequestRecords {
			*problems = append(
				*problems,
				fmt.Sprintf("targets[%d].min_eligible must be in [1,%d]", index, maxRequestRecords),
			)
		}
		if uint64(target.MaxErrorRatePPM) > partsPerMillion {
			*problems = append(
				*problems,
				fmt.Sprintf("targets[%d].max_error_rate_ppm exceeds 1000000", index),
			)
		}
		if target.MaxDowntime.Value() > maxScenarioDuration {
			*problems = append(
				*problems,
				fmt.Sprintf("targets[%d].max_downtime exceeds 24h", index),
			)
		}
		if scenario.Kind == ScenarioKindPlannedRolling {
			if target.MaxErrorRatePPM != 0 {
				*problems = append(
					*problems,
					fmt.Sprintf("planned target %q must have zero error budget", target.Class),
				)
			}
			if target.MaxDowntime != 0 {
				*problems = append(
					*problems,
					fmt.Sprintf("planned target %q must have zero downtime", target.Class),
				)
			}
		}
	}
	for _, class := range []RequestClass{
		RequestClassReadIdempotent,
		RequestClassMutating,
	} {
		if _, exists := targetClasses[class]; !exists {
			*problems = append(*problems, fmt.Sprintf("missing target class %q", class))
		}
	}
	return targetClasses
}

func validateFaults(
	scenario Scenario,
	targetClasses map[RequestClass]struct{},
	problems *[]string,
) {
	if len(scenario.Faults) == 0 {
		*problems = append(*problems, "faults must not be empty")
		return
	}
	seen := make(map[string]struct{}, len(scenario.Faults))
	for index, fault := range scenario.Faults {
		if !contractIDPattern.MatchString(fault.ID) {
			*problems = append(
				*problems,
				fmt.Sprintf("faults[%d].id has invalid format", index),
			)
		}
		if _, exists := seen[fault.ID]; exists {
			*problems = append(*problems, fmt.Sprintf("duplicate fault id %q", fault.ID))
		}
		seen[fault.ID] = struct{}{}
		if !isKnownFaultTarget(fault.Target) {
			*problems = append(
				*problems,
				fmt.Sprintf("faults[%d] has unknown target %q", index, fault.Target),
			)
		}
		if !isKnownFaultMode(fault.Mode) {
			*problems = append(
				*problems,
				fmt.Sprintf("faults[%d] has unknown mode %q", index, fault.Mode),
			)
		}
		if !validFaultPair(fault.Target, fault.Mode) {
			*problems = append(
				*problems,
				fmt.Sprintf("faults[%d] has incompatible target and mode", index),
			)
		}
		validateRecovery(scenario.Kind, index, fault, targetClasses, problems)
	}
}

func validateRecovery(
	kind ScenarioKind,
	index int,
	fault FaultExpectation,
	targetClasses map[RequestClass]struct{},
	problems *[]string,
) {
	if kind == ScenarioKindPlannedRolling {
		if fault.Mode != FaultModeRollingUpdate {
			*problems = append(*problems, fmt.Sprintf("faults[%d] must be a rolling_update", index))
		}
		if fault.Recovery != nil {
			*problems = append(*problems, fmt.Sprintf("faults[%d] must not define emergency recovery", index))
		}
		return
	}
	if kind != ScenarioKindEmergencyOutage {
		return
	}
	if fault.Mode == FaultModeRollingUpdate {
		*problems = append(*problems, fmt.Sprintf("faults[%d] cannot be rolling_update", index))
	}
	if fault.Recovery == nil {
		*problems = append(*problems, fmt.Sprintf("faults[%d] must define recovery", index))
		return
	}
	recovery := fault.Recovery
	if recovery.Anchor != RecoveryAnchorFaultStarted && recovery.Anchor != RecoveryAnchorFaultEnded {
		*problems = append(*problems, fmt.Sprintf("faults[%d] has unknown recovery anchor", index))
	}
	if recovery.MaxDuration <= 0 || recovery.MaxDuration.Value() > maxScenarioDuration {
		*problems = append(*problems, fmt.Sprintf("faults[%d].recovery.max_duration must be in (0,24h]", index))
	}
	if recovery.SuccessStreak == 0 || recovery.SuccessStreak > maxRecoveryStreak {
		*problems = append(
			*problems,
			fmt.Sprintf("faults[%d].recovery.success_streak must be in [1,%d]", index, maxRecoveryStreak),
		)
	}
	if len(recovery.Classes) == 0 {
		*problems = append(*problems, fmt.Sprintf("faults[%d].recovery.classes must not be empty", index))
	}
	seenClasses := make(map[RequestClass]struct{}, len(recovery.Classes))
	for _, class := range recovery.Classes {
		if _, exists := targetClasses[class]; !exists {
			*problems = append(
				*problems,
				fmt.Sprintf("faults[%d].recovery has untargeted class %q", index, class),
			)
		}
		if _, exists := seenClasses[class]; exists {
			*problems = append(
				*problems,
				fmt.Sprintf("faults[%d].recovery has duplicate class %q", index, class),
			)
		}
		seenClasses[class] = struct{}{}
	}
}

func isKnownScenarioKind(kind ScenarioKind) bool {
	return kind == ScenarioKindPlannedRolling || kind == ScenarioKindEmergencyOutage
}

func isKnownRequestClass(class RequestClass) bool {
	return class == RequestClassReadIdempotent || class == RequestClassMutating
}

func isKnownFaultTarget(target FaultTarget) bool {
	switch target {
	case FaultTargetGatewayIn,
		FaultTargetGatewayOut,
		FaultTargetInternalService,
		FaultTargetKubernetesService,
		FaultTargetNetwork,
		FaultTargetDC:
		return true
	default:
		return false
	}
}

func isKnownFaultMode(mode FaultMode) bool {
	switch mode {
	case FaultModeRollingUpdate,
		FaultModePodOutage,
		FaultModeServiceEndpointsOutage,
		FaultModeNetworkPartition,
		FaultModeDCOutage:
		return true
	default:
		return false
	}
}

func validFaultPair(target FaultTarget, mode FaultMode) bool {
	switch target {
	case FaultTargetGatewayIn, FaultTargetGatewayOut, FaultTargetInternalService:
		return mode == FaultModeRollingUpdate || mode == FaultModePodOutage
	case FaultTargetKubernetesService:
		return mode == FaultModeRollingUpdate || mode == FaultModeServiceEndpointsOutage
	case FaultTargetNetwork:
		return mode == FaultModeNetworkPartition
	case FaultTargetDC:
		return mode == FaultModeDCOutage
	default:
		return false
	}
}
