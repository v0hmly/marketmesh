package rolling

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/v0hmly/marketmesh/e2e/tunnel/spec"
)

func TestMM34ScenarioFixturesMatchPlans(t *testing.T) {
	t.Parallel()
	for _, variant := range []Variant{VariantA, VariantB} {
		variant := variant
		t.Run(string(variant), func(t *testing.T) {
			t.Parallel()
			plan, err := NewPlan(variant, testTransitions())
			if err != nil {
				t.Fatalf("NewPlan() error = %v", err)
			}
			path := filepath.Join(
				"testdata", "scenarios", "rolling-update-mm34-"+string(variant)+".json",
			)
			file, err := os.Open(path)
			if err != nil {
				t.Fatalf("os.Open(%q) error = %v", path, err)
			}
			t.Cleanup(func() { _ = file.Close() })
			scenario, err := spec.DecodeScenario(file)
			if err != nil {
				t.Fatalf("spec.DecodeScenario() error = %v", err)
			}
			if err := ValidateScenarioForPlan(scenario, plan); err != nil {
				t.Fatalf("ValidateScenarioForPlan() error = %v", err)
			}
			if len(scenario.Faults) != 12 {
				t.Fatalf("scenario faults = %d, want 12", len(scenario.Faults))
			}
		})
	}
}

func TestValidateScenarioForPlanRejectsOtherOrder(t *testing.T) {
	t.Parallel()
	planA, err := NewPlan(VariantA, testTransitions())
	if err != nil {
		t.Fatalf("NewPlan(A) error = %v", err)
	}
	planB, err := NewPlan(VariantB, testTransitions())
	if err != nil {
		t.Fatalf("NewPlan(B) error = %v", err)
	}
	faults, err := faultExpectations(planA)
	if err != nil {
		t.Fatalf("faultExpectations() error = %v", err)
	}
	scenario := spec.Scenario{
		SchemaVersion: spec.ScenarioSchemaVersion,
		ID:            "rolling-update-mm34-a",
		Kind:          spec.ScenarioKindPlannedRolling,
		Targets: []spec.ClassTarget{
			{Class: spec.RequestClassReadIdempotent, MinEligible: 1},
			{Class: spec.RequestClassMutating, MinEligible: 1},
		},
		Faults: faults,
	}
	if err := ValidateScenarioForPlan(scenario, planB); err == nil {
		t.Fatal("ValidateScenarioForPlan() error = nil, want variant mismatch")
	}
}
