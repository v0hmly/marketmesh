package topology

import "testing"

func TestValidateImageRef(t *testing.T) {
	t.Parallel()

	valid := []string{
		"marketmesh/gateway-in:mm29-0123456789ab",
		"marketmesh/gateway-out:mm29-0123456789ab",
		"marketmesh/fake-internal:mm29-0123456789ab",
		"registry.example.com/team/app:v1.2.0",
	}
	for _, ref := range valid {
		if err := validateImageRef(ref); err != nil {
			t.Errorf("validateImageRef(%q) error = %v, want nil", ref, err)
		}
	}

	invalid := []string{
		"",
		"marketmesh/gateway-in",
		"marketmesh/gateway-in:",
		"Marketmesh/gateway-in:mm29",
		"marketmesh/gateway-in:mm29 latest",
		"marketmesh/gateway-in:mm29;rm",
		"marketmesh/gateway-in:mm29$(id)",
		"-bad/gateway-in:mm29",
	}
	for _, ref := range invalid {
		if err := validateImageRef(ref); err == nil {
			t.Errorf("validateImageRef(%q) error = nil, want rejection", ref)
		}
	}
}

func TestImageTarName(t *testing.T) {
	t.Parallel()

	got := imageTarName("marketmesh/gateway-in:mm29-0123456789ab")
	if got != "marketmesh_gateway-in_mm29-0123456789ab.tar" {
		t.Errorf("imageTarName() = %q", got)
	}
}

func TestImageImported(t *testing.T) {
	t.Parallel()

	names := []string{
		"docker.io/marketmesh/gateway-in:mm29-0123456789ab",
		"sha256:abcdef",
	}
	if !imageImported(names, "marketmesh/gateway-in:mm29-0123456789ab") {
		t.Error("imageImported() = false, want true for docker.io-normalized reference")
	}
	if imageImported(names, "marketmesh/gateway-out:mm29-0123456789ab") {
		t.Error("imageImported() = true, want false for a missing reference")
	}
	if imageImported(
		[]string{"docker.io/marketmesh/gateway-out:mm29-x"},
		"docker.io/marketmesh/gateway-in:mm29-x",
	) {
		t.Error("imageImported() must not match via double docker.io prefixing")
	}
	if !imageImported([]string{"marketmesh/gateway-in:mm29-x"}, "marketmesh/gateway-in:mm29-x") {
		t.Error("imageImported() = false, want true for an exact match")
	}
}
