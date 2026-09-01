package podchaos

import (
	"errors"
	"testing"
)

func TestInstanceIDMappingMatchesWorkloadContract(t *testing.T) {
	t.Parallel()

	gatewayInID, err := gatewayInInstanceID("mm29-gateway-in-abcde")
	if err != nil {
		t.Fatalf("gatewayInInstanceID() error = %v", err)
	}
	if gatewayInID != "7cd7ff5ff944d0a0fe93e4dfa09f8bc6" {
		t.Fatalf("gatewayInInstanceID() = %q", gatewayInID)
	}

	gatewayOutIDs, err := gatewayOutInstanceIDs("mm29-gateway-out-abcde")
	if err != nil {
		t.Fatalf("gatewayOutInstanceIDs() error = %v", err)
	}
	want := [gatewayOutSlots]string{
		"34bdddb00486da820b49a485fbc2d85b",
		"5058ee28dfc7787563d2cf9c1a4b50d7",
	}
	if gatewayOutIDs != want {
		t.Fatalf("gatewayOutInstanceIDs() = %v, want %v", gatewayOutIDs, want)
	}
}

func TestInstanceIDMappingRejectsUnsafePodNames(t *testing.T) {
	t.Parallel()

	if _, err := gatewayInInstanceID("--all"); !errors.Is(err, ErrUnsafeState) {
		t.Fatalf("gatewayInInstanceID() error = %v, want ErrUnsafeState", err)
	}
	if _, err := gatewayOutInstanceIDs("pod/name"); !errors.Is(err, ErrUnsafeState) {
		t.Fatalf("gatewayOutInstanceIDs() error = %v, want ErrUnsafeState", err)
	}
}
