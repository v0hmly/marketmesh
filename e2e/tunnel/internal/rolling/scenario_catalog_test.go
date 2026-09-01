package rolling

import (
	"strings"
	"testing"
)

func TestEmbeddedScenariosExactlyMatchPlans(t *testing.T) {
	t.Parallel()

	digest := "registry.test/marketmesh/component@sha256:" + strings.Repeat("a", 64)
	transitions := map[Component]Transition{
		ComponentGatewayIn: {
			Image: digest, ImageRevision: "gateway-in-image-v2", ConfigRevision: "gateway-in-config-v2",
		},
		ComponentGatewayOut: {
			Image: digest, ImageRevision: "gateway-out-image-v2", ConfigRevision: "gateway-out-config-v2",
		},
		ComponentFakeInternal: {
			Image: digest, ImageRevision: "fake-internal-image-v2", ConfigRevision: "fake-internal-config-v2",
		},
	}
	for _, variant := range []Variant{VariantA, VariantB} {
		variant := variant
		t.Run(string(variant), func(t *testing.T) {
			t.Parallel()
			plan, err := NewPlan(variant, transitions)
			if err != nil {
				t.Fatalf("NewPlan() error = %v", err)
			}
			if _, err := ScenarioForPlan(plan); err != nil {
				t.Fatalf("ScenarioForPlan() error = %v", err)
			}
		})
	}
	if _, err := ScenarioForRollback(); err != nil {
		t.Fatalf("ScenarioForRollback() error = %v", err)
	}
}
