package podchaos

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

func TestDecodeTopologyInventoryProducesExactTargets(t *testing.T) {
	t.Parallel()

	runID := "mm32-safe"
	inventory := validTopologyInventory(t, runID)
	encoded, err := json.Marshal(inventory)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	decoded, err := DecodeTopologyInventory(bytes.NewReader(encoded), runID)
	if err != nil {
		t.Fatalf("DecodeTopologyInventory() error = %v", err)
	}
	targets := decoded.KubernetesTargets()
	if len(targets) != 4 {
		t.Fatalf("len(KubernetesTargets()) = %d, want 4", len(targets))
	}
	for index, logical := range []string{
		"dc-a-dmz", "dc-a-internal", "dc-b-dmz", "dc-b-internal",
	} {
		if targets[index].KubeconfigPath != inventory.Clusters[index].Kubeconfig ||
			targets[index].ContextName != "kind-"+runID+"-"+logical {
			t.Errorf("target[%d] = %+v, want exact inventory boundary", index, targets[index])
		}
	}
}

func TestDecodeTopologyInventoryRejectsUnsafeBoundaries(t *testing.T) {
	t.Parallel()

	runID := "mm32-safe"
	tests := map[string]func(*TopologyInventory){
		"wrong schema":        func(value *TopologyInventory) { value.APIVersion = "v2" },
		"wrong target schema": func(value *TopologyInventory) { value.TargetAPIVersion = "v2" },
		"wrong task":          func(value *TopologyInventory) { value.Task = "MM-32" },
		"wrong run":           func(value *TopologyInventory) { value.Instance = "mm32-other" },
		"wrong target command": func(value *TopologyInventory) {
			value.Commands.TargetsValidate = "validate"
		},
		"wrong owner": func(value *TopologyInventory) {
			value.Ownership.KubernetesLabels["marketmesh.dev/owner-task"] = "MM-32"
		},
		"extra owner": func(value *TopologyInventory) {
			value.Ownership.DockerLabels["foreign"] = "resource"
		},
		"missing cluster": func(value *TopologyInventory) { value.Clusters = value.Clusters[:3] },
		"foreign logical cluster": func(value *TopologyInventory) {
			value.Clusters[0].LogicalName = "dc-c-dmz"
		},
		"derived name mismatch": func(value *TopologyInventory) {
			value.Clusters[0].ResourceName = "foreign"
		},
		"relative kubeconfig": func(value *TopologyInventory) {
			value.Clusters[0].Kubeconfig = "relative.yaml"
		},
		"duplicate target": func(value *TopologyInventory) {
			value.Clusters[1].Kubeconfig = value.Clusters[0].Kubeconfig
			value.Clusters[1].Context = value.Clusters[0].Context
		},
		"duplicate address": func(value *TopologyInventory) {
			value.Clusters[1].ControlPlaneAddress = value.Clusters[0].ControlPlaneAddress
		},
		"public address": func(value *TopologyInventory) {
			value.Clusters[0].ControlPlaneAddress = "203.0.113.10"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			inventory := validTopologyInventory(t, runID)
			mutate(&inventory)
			encoded, err := json.Marshal(inventory)
			if err != nil {
				t.Fatalf("Marshal() error = %v", err)
			}
			if _, err := DecodeTopologyInventory(bytes.NewReader(encoded), runID); err == nil {
				t.Fatal("DecodeTopologyInventory() error = nil, want rejection")
			}
		})
	}
}

func TestDecodeTopologyInventoryIsStrictAndBounded(t *testing.T) {
	t.Parallel()

	runID := "mm32-safe"
	inventory := validTopologyInventory(t, runID)
	encoded, err := json.Marshal(inventory)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	tests := map[string][]byte{
		"unknown":   bytes.Replace(encoded, []byte(`"task":"MM-28"`), []byte(`"unknown":true,"task":"MM-28"`), 1),
		"duplicate": bytes.Replace(encoded, []byte(`"task":"MM-28"`), []byte(`"task":"MM-28","task":"MM-28"`), 1),
		"trailing":  append(append([]byte(nil), encoded...), []byte(` {}`)...),
		"oversize":  []byte(`{"padding":"` + strings.Repeat("x", maxTopologyInventoryBytes) + `"}`),
	}
	for name, input := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := DecodeTopologyInventory(bytes.NewReader(input), runID); err == nil {
				t.Fatal("DecodeTopologyInventory() error = nil, want rejection")
			}
		})
	}
}

func validTopologyInventory(t *testing.T, runID string) TopologyInventory {
	t.Helper()
	clusters := make([]TopologyInventoryCluster, 0, 4)
	for index, value := range []struct {
		logical string
		dc      string
		zone    string
	}{
		{logical: "dc-a-dmz", dc: "dc-a", zone: ZoneDMZ},
		{logical: "dc-a-internal", dc: "dc-a", zone: ZoneInternal},
		{logical: "dc-b-dmz", dc: "dc-b", zone: ZoneDMZ},
		{logical: "dc-b-internal", dc: "dc-b", zone: ZoneInternal},
	} {
		name := runID + "-" + value.logical
		clusters = append(clusters, TopologyInventoryCluster{
			LogicalName: value.logical, ResourceName: name,
			DC: value.dc, Zone: value.zone, NetworkName: name,
			ControlPlaneAddress: "172.28." + string(rune('1'+index)) + ".2",
			Kubeconfig:          inventoryPath(t, value.logical), Context: "kind-" + name,
			Namespace: topologyNamespace, WorkloadIdentityFormat: workloadIdentityFormat,
		})
	}
	commandPrefix := "go run ./tools/e2e-topology --instance " + runID +
		" --docker-context desktop-linux"
	return TopologyInventory{
		APIVersion: topologyInventoryAPIVersion, TargetAPIVersion: topologyTargetAPIVersion,
		Task:     topologyOwnerTask,
		Instance: runID, DockerContext: "desktop-linux",
		Namespace: topologyNamespace, TunnelPort: topologyTunnelPort,
		Ownership: TopologyInventoryOwnership{
			DockerLabels: map[string]string{
				"com.marketmesh.task": topologyOwnerTask, "com.marketmesh.topology": runID,
			},
			KubernetesLabels: map[string]string{
				"marketmesh.dev/owner-task":        topologyOwnerTask,
				"marketmesh.dev/topology-instance": runID,
			},
		},
		Commands: TopologyInventoryCommands{
			Ready: commandPrefix + " ready", Inspect: commandPrefix + " inspect",
			Down: commandPrefix + " down", TargetsResolve: commandPrefix + " targets resolve",
			TargetsValidate: commandPrefix + " targets validate --snapshot -",
			TargetsRebind:   commandPrefix + " targets rebind --transition -",
		},
		Clusters: clusters,
	}
}

func inventoryPath(t *testing.T, logical string) string {
	t.Helper()
	return filepath.Join(t.TempDir(), logical+".yaml")
}
