package topology

import (
	"os"
	"path/filepath"
	"testing"
)

func TestNormalizeDockerArchitecture(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"aarch64": "arm64",
		"arm64":   "arm64",
		"x86_64":  "amd64",
		"amd64":   "amd64",
	}
	for input, expected := range tests {
		if actual := normalizeDockerArchitecture(input); actual != expected {
			t.Errorf("normalizeDockerArchitecture(%q) = %q, want %q", input, actual, expected)
		}
	}
}

func TestIsRejectedProbe(t *testing.T) {
	t.Parallel()

	if !isRejectedProbe(Result{Stderr: "tcpprobe: connection failed\n"}) {
		t.Fatal("isRejectedProbe() = false, want true for an executed rejected probe")
	}
	if isRejectedProbe(Result{Stderr: "docker: no such container\n"}) {
		t.Fatal("isRejectedProbe() = true for an unrelated Docker failure")
	}
}

func TestDefaultRouteMatches(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		output        string
		gateway       string
		interfaceName string
		want          bool
	}{
		{
			name:          "internal route",
			output:        "default via 172.28.11.1 dev eth0\n",
			gateway:       "172.28.11.1",
			interfaceName: "eth0",
			want:          true,
		},
		{
			name:          "dmz route",
			output:        "default via 172.28.10.1 dev eth1\n",
			gateway:       "172.28.11.1",
			interfaceName: "eth0",
		},
		{
			name:          "multiple routes",
			output:        "default via 172.28.11.1 dev eth0\ndefault via 172.28.10.1 dev eth1\n",
			gateway:       "172.28.11.1",
			interfaceName: "eth0",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := defaultRouteMatches(test.output, test.gateway, test.interfaceName); got != test.want {
				t.Errorf("defaultRouteMatches() = %v, want %v", got, test.want)
			}
		})
	}
}

func TestValidateKubernetesIdentity(t *testing.T) {
	t.Parallel()

	labels := []struct {
		key   string
		value string
	}{
		{key: "marketmesh.dev/cluster", value: "dc-a-dmz"},
		{key: "marketmesh.dev/owner-task", value: TaskKey},
	}
	object := kubernetesObject{}
	object.Metadata.Name = "mm28-dc-a-dmz-control-plane"
	object.Metadata.Labels = map[string]string{
		"marketmesh.dev/cluster":    "dc-a-dmz",
		"marketmesh.dev/owner-task": TaskKey,
	}
	if err := validateKubernetesIdentity(object, object.Metadata.Name, labels); err != nil {
		t.Fatalf("validateKubernetesIdentity() error = %v", err)
	}

	object.Metadata.Labels["marketmesh.dev/owner-task"] = "MM-999"
	if err := validateKubernetesIdentity(object, object.Metadata.Name, labels); err == nil {
		t.Fatal("validateKubernetesIdentity() error = nil, want label mismatch")
	}
}

func TestRemoveInventory(t *testing.T) {
	t.Parallel()

	config, err := NewConfig(t.TempDir(), "mm28-test", "default")
	if err != nil {
		t.Fatalf("NewConfig() error = %v", err)
	}
	if err := os.MkdirAll(config.StateDir, 0o750); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	inventoryPath := filepath.Join(config.StateDir, "inventory.json")
	if err := os.WriteFile(inventoryPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	manager := New(config, nil, nil)
	if err := manager.removeInventory(); err != nil {
		t.Fatalf("removeInventory() error = %v", err)
	}
	if _, err := os.Stat(inventoryPath); !os.IsNotExist(err) {
		t.Fatalf("inventory still exists: %v", err)
	}
	if err := manager.removeInventory(); err != nil {
		t.Fatalf("idempotent removeInventory() error = %v", err)
	}
}
