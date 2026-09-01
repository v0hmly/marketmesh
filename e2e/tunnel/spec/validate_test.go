package spec_test

import (
	"errors"
	"testing"

	"github.com/v0hmly/marketmesh/e2e/tunnel/spec"
)

func TestValidateScenarioRejectsAmbiguousThresholds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*spec.Scenario)
	}{
		{
			name: "missing request class",
			mutate: func(scenario *spec.Scenario) {
				scenario.Targets = scenario.Targets[:1]
			},
		},
		{
			name: "planned update with error budget",
			mutate: func(scenario *spec.Scenario) {
				scenario.Targets[0].MaxErrorRatePPM = 1
			},
		},
		{
			name: "incompatible fault pair",
			mutate: func(scenario *spec.Scenario) {
				scenario.Faults[0].Target = spec.FaultTargetDC
			},
		},
		{
			name: "emergency without recovery",
			mutate: func(scenario *spec.Scenario) {
				*scenario = emergencyScenario()
				scenario.Faults[0].Recovery = nil
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			scenario := plannedScenario()
			test.mutate(&scenario)
			err := spec.ValidateScenario(scenario)
			var validationErr *spec.ValidationError
			if !errors.As(err, &validationErr) {
				t.Fatalf("ValidateScenario() error = %v, want ValidationError", err)
			}
			if len(validationErr.Problems) == 0 {
				t.Fatal("ValidationError has no problems")
			}
		})
	}
}
