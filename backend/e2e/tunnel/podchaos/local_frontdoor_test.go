package podchaos

import "testing"

func TestFrontDoorTargetsUseOnlyActualInventoryAddresses(t *testing.T) {
	t.Parallel()

	inventory := validTopologyInventory(t, "mm32-frontdoor")
	dcA, dcB, err := frontDoorTargets(inventory)
	if err != nil {
		t.Fatalf("frontDoorTargets() error = %v", err)
	}
	if dcA != "http://172.28.1.2:30080" || dcB != "http://172.28.3.2:30080" {
		t.Fatalf("frontDoorTargets() = %q, %q", dcA, dcB)
	}
}
