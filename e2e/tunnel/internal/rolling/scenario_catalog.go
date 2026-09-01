package rolling

import (
	"bytes"
	"embed"
	"errors"
	"fmt"

	"github.com/v0hmly/marketmesh/e2e/tunnel/spec"
)

//go:embed testdata/scenarios/*.json
var scenarioCatalog embed.FS

// ScenarioForPlan returns the embedded MM-27 contract for one exact plan.
func ScenarioForPlan(plan Plan) (spec.Scenario, error) {
	if err := validatePlan(plan); err != nil {
		return spec.Scenario{}, err
	}
	name := ""
	switch plan.Variant {
	case VariantA:
		name = "rolling-update-mm34-a.json"
	case VariantB:
		name = "rolling-update-mm34-b.json"
	default:
		return spec.Scenario{}, errors.New("rolling: unknown scenario variant")
	}
	scenario, err := loadScenario(name)
	if err != nil {
		return spec.Scenario{}, err
	}
	if err := ValidateScenarioForPlan(scenario, plan); err != nil {
		return spec.Scenario{}, err
	}

	return scenario, nil
}

// ScenarioForRollback returns the embedded six-target readiness-fault matrix.
func ScenarioForRollback() (spec.Scenario, error) {
	scenario, err := loadScenario("rolling-rollback-mm34.json")
	if err != nil {
		return spec.Scenario{}, err
	}
	if err := ValidateScenarioForRollback(scenario); err != nil {
		return spec.Scenario{}, err
	}

	return scenario, nil
}

func loadScenario(name string) (spec.Scenario, error) {
	data, err := scenarioCatalog.ReadFile("testdata/scenarios/" + name)
	if err != nil {
		return spec.Scenario{}, fmt.Errorf("rolling: reading embedded scenario: %w", err)
	}

	return spec.DecodeScenario(bytes.NewReader(data))
}
