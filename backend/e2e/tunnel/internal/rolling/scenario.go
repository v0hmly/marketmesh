package rolling

import (
	"errors"
	"fmt"
	"slices"

	"github.com/v0hmly/marketmesh/e2e/tunnel/spec"
)

// ValidateScenarioForPlan proves that the MM-27 scenario accounts for every
// independently measured image and config rollout in one plan.
func ValidateScenarioForPlan(scenario spec.Scenario, plan Plan) error {
	if err := spec.ValidateScenario(scenario); err != nil {
		return err
	}
	if scenario.Kind != spec.ScenarioKindPlannedRolling {
		return errors.New("rolling: scenario must be a planned rolling update")
	}
	expected, err := faultExpectations(plan)
	if err != nil {
		return err
	}
	if !slices.Equal(scenario.Faults, expected) {
		return errors.New("rolling: scenario faults do not exactly match the rollout plan")
	}

	return nil
}

// RollbackTargets returns every workload/DC pair in a deterministic order.
func RollbackTargets() []Target {
	targets := make([]Target, 0, 6)
	for _, dc := range []string{"dc-a", "dc-b"} {
		for _, component := range []Component{
			ComponentGatewayIn,
			ComponentGatewayOut,
			ComponentFakeInternal,
		} {
			target, found := targetFor(dc, component)
			if found {
				targets = append(targets, target)
			}
		}
	}

	return targets
}

// ValidateScenarioForRollback proves that the negative scenario accounts for
// every built-in workload readiness fault in both data centers.
func ValidateScenarioForRollback(scenario spec.Scenario) error {
	if err := spec.ValidateScenario(scenario); err != nil {
		return err
	}
	if scenario.Kind != spec.ScenarioKindPlannedRolling {
		return errors.New("rolling: rollback scenario must be a planned rolling update")
	}
	expected := make([]spec.FaultExpectation, 0, 6)
	for _, target := range RollbackTargets() {
		faultID, err := rollbackFaultID(target)
		if err != nil {
			return err
		}
		sloTarget, err := specTarget(target.Component)
		if err != nil {
			return err
		}
		expected = append(expected, spec.FaultExpectation{
			ID: faultID, Target: sloTarget, Mode: spec.FaultModeRollingUpdate,
		})
	}
	if !slices.Equal(scenario.Faults, expected) {
		return errors.New("rolling: scenario faults do not exactly match the rollback matrix")
	}

	return nil
}

func faultExpectations(plan Plan) ([]spec.FaultExpectation, error) {
	if err := validatePlan(plan); err != nil {
		return nil, err
	}
	faults := make([]spec.FaultExpectation, 0, len(plan.Steps))
	for _, step := range plan.Steps {
		faultID, err := faultIDForStep(plan.Variant, step)
		if err != nil {
			return nil, err
		}
		target, err := specTarget(step.Target.Component)
		if err != nil {
			return nil, err
		}
		faults = append(faults, spec.FaultExpectation{
			ID: faultID, Target: target, Mode: spec.FaultModeRollingUpdate,
		})
	}

	return faults, nil
}

func faultIDForStep(variant Variant, step Step) (string, error) {
	if variant != VariantA && variant != VariantB {
		return "", errors.New("rolling: unknown plan variant")
	}
	if err := validateTarget(step.Target); err != nil {
		return "", err
	}
	if err := validateChange(step.Change); err != nil {
		return "", err
	}

	return fmt.Sprintf(
		"mm34-%s-%s-%s-%s",
		variant,
		step.Target.DC,
		step.Target.Component,
		step.Change.Kind,
	), nil
}

func rollbackFaultID(target Target) (string, error) {
	if err := validateTarget(target); err != nil {
		return "", err
	}

	return fmt.Sprintf("mm34-rollback-%s-%s", target.DC, target.Component), nil
}

func specTarget(component Component) (spec.FaultTarget, error) {
	switch component {
	case ComponentGatewayIn:
		return spec.FaultTargetGatewayIn, nil
	case ComponentGatewayOut:
		return spec.FaultTargetGatewayOut, nil
	case ComponentFakeInternal:
		return spec.FaultTargetInternalService, nil
	default:
		return spec.FaultTargetUnknown, errors.New("rolling: component has no SLO target")
	}
}
