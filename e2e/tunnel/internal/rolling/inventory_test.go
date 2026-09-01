package rolling

import (
	"bytes"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"testing"
)

func TestDecodeTopologyInventory(t *testing.T) {
	t.Parallel()
	inventory := validTopologyInventory(t.TempDir())
	tests := []struct {
		name   string
		mutate func(*topologyInventory)
	}{
		{name: "valid"},
		{
			name: "wrong schema",
			mutate: func(value *topologyInventory) {
				value.APIVersion = "marketmesh.dev/e2e-topology/v2"
			},
		},
		{
			name: "wrong owner",
			mutate: func(value *topologyInventory) {
				value.Ownership.KubernetesLabels["marketmesh.dev/owner-task"] = "MM-999"
			},
		},
		{
			name: "wrong context",
			mutate: func(value *topologyInventory) {
				value.Clusters[0].Context = "kind-user-cluster"
			},
		},
		{
			name: "duplicate cluster",
			mutate: func(value *topologyInventory) {
				value.Clusters[1] = value.Clusters[0]
			},
		},
		{
			name: "relative kubeconfig",
			mutate: func(value *topologyInventory) {
				value.Clusters[0].Kubeconfig = "relative.yaml"
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			candidate := cloneTopologyInventory(inventory)
			if test.mutate != nil {
				test.mutate(&candidate)
			}
			encoded, err := json.Marshal(candidate)
			if err != nil {
				t.Fatalf("json.Marshal() error = %v", err)
			}
			clusters, err := DecodeTopologyInventory(bytes.NewReader(encoded))
			if test.name == "valid" {
				if err != nil {
					t.Fatalf("DecodeTopologyInventory() error = %v", err)
				}
				if len(clusters) != 4 || clusters[0].Context != "kind-mm34topo-dc-a-dmz" {
					t.Fatalf("DecodeTopologyInventory() clusters = %#v", clusters)
				}
			} else if err == nil {
				t.Fatal("DecodeTopologyInventory() error = nil, want rejection")
			}
		})
	}
}

func TestDecodeTopologyInventoryRejectsMalformedInput(t *testing.T) {
	t.Parallel()
	var typedNil *bytes.Reader
	tests := []struct {
		name   string
		reader io.Reader
	}{
		{name: "nil", reader: nil},
		{name: "typed nil", reader: typedNil},
		{name: "unknown field", reader: strings.NewReader(`{"unknown":true}`)},
		{name: "trailing document", reader: strings.NewReader(`{} {}`)},
		{name: "too large", reader: strings.NewReader(strings.Repeat("x", maximumInventoryBytes+1))},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := DecodeTopologyInventory(test.reader); err == nil {
				t.Fatal("DecodeTopologyInventory() error = nil, want malformed input rejection")
			}
		})
	}
}

func validTopologyInventory(directory string) topologyInventory {
	clusters := make([]inventoryCluster, 0, 4)
	for _, cluster := range testClusters(directory) {
		clusters = append(clusters, inventoryCluster{
			LogicalName:            cluster.LogicalName,
			ResourceName:           cluster.ResourceName,
			DC:                     cluster.DC,
			Zone:                   cluster.Zone,
			NetworkName:            cluster.ResourceName,
			ControlPlaneAddress:    cluster.ControlPlaneAddress,
			Kubeconfig:             cluster.Kubeconfig,
			Context:                cluster.Context,
			Namespace:              topologyNamespace,
			WorkloadIdentityFormat: "<pod>/<namespace>/<logical-cluster>",
		})
	}

	return topologyInventory{
		APIVersion: topologyInventoryAPIVersion, Task: topologyTaskKey,
		Instance: "mm34topo", DockerContext: "orbstack",
		Namespace: topologyNamespace, TunnelPort: topologyTunnelPort,
		Ownership: inventoryOwnership{
			DockerLabels: map[string]string{
				"com.marketmesh.task": topologyTaskKey, "com.marketmesh.topology": "mm34topo",
			},
			KubernetesLabels: map[string]string{
				"marketmesh.dev/owner-task":        topologyTaskKey,
				"marketmesh.dev/topology-instance": "mm34topo",
			},
		},
		Commands: inventoryCommands{Ready: "ready", Inspect: "inspect", Down: "down"},
		Clusters: clusters,
	}
}

func cloneTopologyInventory(value topologyInventory) topologyInventory {
	clone := value
	clone.Clusters = slices.Clone(value.Clusters)
	clone.Ownership.DockerLabels = map[string]string{}
	for key, entry := range value.Ownership.DockerLabels {
		clone.Ownership.DockerLabels[key] = entry
	}
	clone.Ownership.KubernetesLabels = map[string]string{}
	for key, entry := range value.Ownership.KubernetesLabels {
		clone.Ownership.KubernetesLabels[key] = entry
	}

	return clone
}
