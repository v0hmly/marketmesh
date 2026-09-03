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

	config, err := NewConfig("/workspace/marketmesh", "mm44")
	if err != nil {
		t.Fatalf("NewConfig() error = %v", err)
	}
	document, err := New(config, nil, nil).inventoryDocument(map[string]string{
		"dc-a-dmz":      "192.168.139.10",
		"dc-a-internal": "192.168.139.11",
		"dc-b-dmz":      "192.168.139.12",
		"dc-b-internal": "192.168.139.13",
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
	if document.Task != TaskKey {
		t.Errorf("task = %q, want %q", document.Task, TaskKey)
	}
	if document.Runtime != RuntimeName {
		t.Errorf("runtime = %q, want %q", document.Runtime, RuntimeName)
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
	if strings.Contains(string(encoded), "docker_context") {
		t.Errorf("inventory JSON retains the removed docker_context field: %s", encoded)
	}
	for name, command := range map[string]string{
		"ready":            document.Commands.Ready,
		"inspect":          document.Commands.Inspect,
		"down":             document.Commands.Down,
		"targets rebind":   document.Commands.TargetsRebind,
		"targets resolve":  document.Commands.TargetsResolve,
		"targets validate": document.Commands.TargetsValidate,
	} {
		for _, expected := range []string{"--instance mm44", name} {
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
		if cluster.Context == "" || cluster.Namespace == "" ||
			net.ParseIP(cluster.ControlPlaneAddress).To4() == nil {
			t.Errorf("cluster inventory is incomplete: %+v", cluster)
		}
		if cluster.Context != config.Instance+"-"+cluster.LogicalName {
			t.Errorf("context = %q, want instance-scoped name", cluster.Context)
		}
	}
	if document.Ownership.KubernetesLabels["marketmesh.dev/owner-task"] != TaskKey {
		t.Errorf("kubernetes owner label = %q, want %q",
			document.Ownership.KubernetesLabels["marketmesh.dev/owner-task"], TaskKey)
	}
}
