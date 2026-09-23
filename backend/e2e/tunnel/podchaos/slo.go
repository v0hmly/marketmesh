package podchaos

import (
	"fmt"

	"github.com/v0hmly/marketmesh/e2e/tunnel/spec"
)

// ValidateSLOScenario binds the external MM-27 contract to the exact MM-32
// fault matrix. Thresholds remain in the decoded scenario document; this
// function only prevents a weaker or incomplete document from authorizing a
// destructive run.
func ValidateSLOScenario(scenario spec.Scenario, execution Execution) error {
	if err := spec.ValidateScenario(scenario); err != nil {
		return err
	}
	faults, err := execution.Faults()
	if err != nil {
		return err
	}
	if scenario.Kind != spec.ScenarioKindEmergencyOutage ||
		len(scenario.Faults) != len(faults) {
		return fmt.Errorf(
			"%w: SLO scenario does not describe the complete pod outage matrix",
			ErrInvalidConfiguration,
		)
	}
	for _, target := range scenario.Targets {
		if target.MinEligible < 100 || target.MaxErrorRatePPM != 0 ||
			target.MaxDowntime.Value() != 0 {
			return fmt.Errorf(
				"%w: SLO target must require 100%% eligible-request success",
				ErrInvalidConfiguration,
			)
		}
	}

	for index, fault := range faults {
		expectation := scenario.Faults[index]
		target := spec.FaultTargetGatewayOut
		if fault.Step.Component == ComponentGatewayIn {
			target = spec.FaultTargetGatewayIn
		}
		if expectation.ID != fault.ID || expectation.Target != target ||
			expectation.Mode != spec.FaultModePodOutage || expectation.Recovery == nil ||
			expectation.Recovery.Anchor != spec.RecoveryAnchorFaultStarted ||
			expectation.Recovery.MaxDuration.Value() <= 0 ||
			expectation.Recovery.SuccessStreak == 0 ||
			!hasBothSLOClasses(expectation.Recovery.Classes) {
			return fmt.Errorf(
				"%w: SLO fault %d does not match the MM-32 matrix",
				ErrInvalidConfiguration,
				index,
			)
		}
	}
	return nil
}

func hasBothSLOClasses(classes []spec.RequestClass) bool {
	if len(classes) != 2 {
		return false
	}
	seen := map[spec.RequestClass]bool{}
	for _, class := range classes {
		if class != spec.RequestClassReadIdempotent && class != spec.RequestClassMutating {
			return false
		}
		seen[class] = true
	}
	return seen[spec.RequestClassReadIdempotent] && seen[spec.RequestClassMutating]
}
