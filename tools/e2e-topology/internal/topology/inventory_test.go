package topology

import (
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"testing"
)

func TestInventoryIsExplicitAndAbsolute(t *testing.T) {
	t.Parallel()

	config, err := NewConfig("/workspace/marketmesh", "mm28", "orbstack")
	if err != nil {
		t.Fatalf("NewConfig() error = %v", err)
	}
	document, err := New(config, nil, nil).inventoryDocument(map[string]string{
		"dc-a-dmz":      "172.28.10.2",
		"dc-a-internal": "172.28.11.2",
		"dc-b-dmz":      "172.28.20.2",
		"dc-b-internal": "172.28.21.2",
	})
	if err != nil {
		t.Fatalf("Inventory() error = %v", err)
	}
	if document.APIVersion != InventoryAPIVersion {
		t.Errorf("api_version = %q, want %q", document.APIVersion, InventoryAPIVersion)
	}
	if document.TargetAPIVersion != TargetAPIVersion {
		t.Errorf("target_api_version = %q, want %q", document.TargetAPIVersion, TargetAPIVersion)
	}
	if document.DockerContext != "orbstack" {
		t.Errorf("docker_context = %q, want orbstack", document.DockerContext)
	}
	if document.TunnelPort != 30443 {
		t.Errorf("tunnel_port = %d, want 30443", document.TunnelPort)
	}
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if !strings.Contains(string(encoded), `"tunnel_port":30443`) {
		t.Errorf("inventory JSON does not contain the public tunnel_port contract: %s", encoded)
	}
	for name, command := range map[string]string{
		"ready":            document.Commands.Ready,
		"inspect":          document.Commands.Inspect,
		"down":             document.Commands.Down,
		"targets rebind":   document.Commands.TargetsRebind,
		"targets resolve":  document.Commands.TargetsResolve,
		"targets validate": document.Commands.TargetsValidate,
	} {
		for _, expected := range []string{"--instance mm28", "--docker-context orbstack", name} {
			if !strings.Contains(command, expected) {
				t.Errorf("%s command = %q, want %q", name, command, expected)
			}
		}
	}
	if len(document.Clusters) != 4 {
		t.Fatalf("len(clusters) = %d, want 4", len(document.Clusters))
	}
	for _, cluster := range document.Clusters {
		if !filepath.IsAbs(cluster.Kubeconfig) {
			t.Errorf("kubeconfig = %q, want absolute path", cluster.Kubeconfig)
		}
		if cluster.Context == "" || cluster.NetworkName == "" || cluster.Namespace == "" ||
			net.ParseIP(cluster.ControlPlaneAddress).To4() == nil {
			t.Errorf("cluster inventory is incomplete: %+v", cluster)
		}
	}
	if document.Ownership.DockerLabels[ownerLabelKey] != TaskKey {
		t.Errorf("docker owner label = %q, want %q", document.Ownership.DockerLabels[ownerLabelKey], TaskKey)
	}
}
