package podchaos

import (
	"bytes"
	"os"
	"testing"

	"github.com/v0hmly/marketmesh/e2e/tunnel/spec"
)

func TestPublishedSLOScenarioMatchesCompleteFaultMatrix(t *testing.T) {
	t.Parallel()

	content, err := os.ReadFile("testdata/scenario.json")
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	scenario, err := spec.DecodeScenario(bytes.NewReader(content))
	if err != nil {
		t.Fatalf("DecodeScenario() error = %v", err)
	}
	if err := ValidateSLOScenario(scenario, DefaultExecution("mm32-test")); err != nil {
		t.Fatalf("ValidateSLOScenario() error = %v", err)
	}
}

func TestValidateSLOScenarioRejectsWeakenedOrReorderedContract(t *testing.T) {
	t.Parallel()

	content, err := os.ReadFile("testdata/scenario.json")
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	decode := func(t *testing.T) spec.Scenario {
		t.Helper()
		scenario, decodeErr := spec.DecodeScenario(bytes.NewReader(content))
		if decodeErr != nil {
			t.Fatalf("DecodeScenario() error = %v", decodeErr)
		}
		return scenario
	}
	tests := map[string]func(*spec.Scenario){
		"error budget": func(value *spec.Scenario) { value.Targets[0].MaxErrorRatePPM = 1 },
		"downtime": func(value *spec.Scenario) {
			value.Targets[1].MaxDowntime = spec.Duration(1)
		},
		"too few requests": func(value *spec.Scenario) { value.Targets[0].MinEligible = 99 },
		"missing fault":    func(value *spec.Scenario) { value.Faults = value.Faults[:15] },
		"reordered fault": func(value *spec.Scenario) {
			value.Faults[0], value.Faults[1] = value.Faults[1], value.Faults[0]
		},
		"wrong target": func(value *spec.Scenario) {
			value.Faults[0].Target = spec.FaultTargetGatewayOut
		},
		"missing recovery class": func(value *spec.Scenario) {
			value.Faults[0].Recovery.Classes = value.Faults[0].Recovery.Classes[:1]
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			scenario := decode(t)
			mutate(&scenario)
			if err := ValidateSLOScenario(scenario, DefaultExecution("mm32-test")); err == nil {
				t.Fatal("ValidateSLOScenario() error = nil, want rejection")
			}
		})
	}
}
