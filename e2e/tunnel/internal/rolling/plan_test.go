package rolling

import (
	"strings"
	"testing"
)

func TestNewPlan(t *testing.T) {
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
	tests := []struct {
		name     string
		variant  Variant
		expected []string
	}{
		{
			name:    "variant a",
			variant: VariantA,
			expected: []string{
				"dc-a/gateway-in", "dc-a/gateway-out", "dc-a/fake-internal",
				"dc-b/gateway-in", "dc-b/gateway-out", "dc-b/fake-internal",
			},
		},
		{
			name:    "variant b",
			variant: VariantB,
			expected: []string{
				"dc-b/fake-internal", "dc-b/gateway-out", "dc-b/gateway-in",
				"dc-a/fake-internal", "dc-a/gateway-out", "dc-a/gateway-in",
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			plan, err := NewPlan(test.variant, transitions)
			if err != nil {
				t.Fatalf("NewPlan() error = %v", err)
			}
			if len(plan.Steps) != len(test.expected)*2 {
				t.Fatalf("len(Steps) = %d, want %d", len(plan.Steps), len(test.expected)*2)
			}
			for index, expected := range test.expected {
				imageStep := plan.Steps[index*2]
				configStep := plan.Steps[index*2+1]
				actual := imageStep.Target.DC + "/" + string(imageStep.Target.Component)
				if actual != expected {
					t.Errorf("target %d = %q, want %q", index, actual, expected)
				}
				if imageStep.Change.Kind != ChangeImage || configStep.Change.Kind != ChangeConfig {
					t.Errorf("target %d change order = %s/%s", index, imageStep.Change.Kind, configStep.Change.Kind)
				}
			}
		})
	}
}

func TestNewPlanRejectsMutableImage(t *testing.T) {
	t.Parallel()
	_, err := NewPlan(VariantA, map[Component]Transition{
		ComponentGatewayIn: {
			Image: "registry.test/gateway-in:latest", ImageRevision: "image-v2", ConfigRevision: "config-v2",
		},
	})
	if err == nil {
		t.Fatal("NewPlan() error = nil, want mutable image rejection")
	}
}

func TestNewPlanAcceptsBoundedLocalImageTags(t *testing.T) {
	t.Parallel()
	transitions := map[Component]Transition{}
	for _, component := range []Component{
		ComponentGatewayIn,
		ComponentGatewayOut,
		ComponentFakeInternal,
	} {
		transitions[component] = Transition{
			Image:          "docker.io/marketmesh/" + string(component) + ":mm34-0123456789ab",
			ImageRevision:  "image-v2",
			ConfigRevision: "config-v2",
		}
	}
	if _, err := NewPlan(VariantA, transitions); err != nil {
		t.Fatalf("NewPlan() error = %v", err)
	}
}

func TestValidatePlanRejectsPartialPlan(t *testing.T) {
	t.Parallel()
	target, _ := targetFor("dc-a", ComponentGatewayIn)
	err := validatePlan(Plan{
		Variant: VariantA,
		Steps: []Step{{
			Target: target,
			Change: Change{Kind: ChangeConfig, Revision: "config-v2", ConfigRevision: "config-v2"},
		}},
	})
	if err == nil {
		t.Fatal("validatePlan() error = nil, want partial plan rejection")
	}
}
