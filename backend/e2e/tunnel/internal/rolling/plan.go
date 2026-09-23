package rolling

import (
	"errors"
	"fmt"
)

// NewPlan expands a variant into image then config transitions for each target.
func NewPlan(variant Variant, transitions map[Component]Transition) (Plan, error) {
	targets, err := orderedTargets(variant)
	if err != nil {
		return Plan{}, err
	}
	steps := make([]Step, 0, len(targets)*2)
	for _, target := range targets {
		transition, found := transitions[target.Component]
		if !found {
			return Plan{}, fmt.Errorf("rolling: transition for %s is required", target.Component)
		}
		imageChange := Change{
			Kind:     ChangeImage,
			Revision: transition.ImageRevision,
			Image:    transition.Image,
		}
		if err := validateChange(imageChange); err != nil {
			return Plan{}, fmt.Errorf("rolling: validating %s image transition: %w", target.Component, err)
		}
		configChange := Change{
			Kind:           ChangeConfig,
			Revision:       transition.ConfigRevision,
			ConfigRevision: transition.ConfigRevision,
		}
		if err := validateChange(configChange); err != nil {
			return Plan{}, fmt.Errorf("rolling: validating %s config transition: %w", target.Component, err)
		}
		steps = append(
			steps,
			Step{Target: target, Change: imageChange},
			Step{Target: target, Change: configChange},
		)
	}

	return Plan{Variant: variant, Steps: steps}, nil
}

func orderedTargets(variant Variant) ([]Target, error) {
	var definitions []struct {
		dc        string
		component Component
	}
	switch variant {
	case VariantA:
		definitions = []struct {
			dc        string
			component Component
		}{
			{dc: "dc-a", component: ComponentGatewayIn},
			{dc: "dc-a", component: ComponentGatewayOut},
			{dc: "dc-a", component: ComponentFakeInternal},
			{dc: "dc-b", component: ComponentGatewayIn},
			{dc: "dc-b", component: ComponentGatewayOut},
			{dc: "dc-b", component: ComponentFakeInternal},
		}
	case VariantB:
		definitions = []struct {
			dc        string
			component Component
		}{
			{dc: "dc-b", component: ComponentFakeInternal},
			{dc: "dc-b", component: ComponentGatewayOut},
			{dc: "dc-b", component: ComponentGatewayIn},
			{dc: "dc-a", component: ComponentFakeInternal},
			{dc: "dc-a", component: ComponentGatewayOut},
			{dc: "dc-a", component: ComponentGatewayIn},
		}
	default:
		return nil, errors.New("rolling: unknown plan variant")
	}

	targets := make([]Target, 0, len(definitions))
	for _, definition := range definitions {
		target, found := targetFor(definition.dc, definition.component)
		if !found {
			return nil, errors.New("rolling: invalid built-in target")
		}
		targets = append(targets, target)
	}

	return targets, nil
}

func validatePlan(plan Plan) error {
	targets, err := orderedTargets(plan.Variant)
	if err != nil {
		return err
	}
	if len(plan.Steps) != len(targets)*2 {
		return errors.New("rolling: plan must contain both transitions for every target")
	}
	for index, target := range targets {
		imageStep := plan.Steps[index*2]
		configStep := plan.Steps[index*2+1]
		if imageStep.Target != target || configStep.Target != target ||
			imageStep.Change.Kind != ChangeImage || configStep.Change.Kind != ChangeConfig {
			return errors.New("rolling: plan order does not match its variant")
		}
		if err := validateChange(imageStep.Change); err != nil {
			return err
		}
		if err := validateChange(configStep.Change); err != nil {
			return err
		}
	}

	return nil
}
