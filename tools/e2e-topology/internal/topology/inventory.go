package topology

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
)

// InventoryAPIVersion identifies the stable consumer-facing inventory schema.
const InventoryAPIVersion = "marketmesh.dev/e2e-topology/v1"

// InventoryDocument is the complete machine-readable topology handoff.
type InventoryDocument struct {
	APIVersion    string             `json:"api_version"`
	Task          string             `json:"task"`
	Instance      string             `json:"instance"`
	DockerContext string             `json:"docker_context"`
	Namespace     string             `json:"namespace"`
	TunnelPort    int                `json:"tunnel_port"`
	Ownership     InventoryOwnership `json:"ownership"`
	Commands      InventoryCommands  `json:"commands"`
	Clusters      []InventoryCluster `json:"clusters"`
}

// InventoryOwnership lists labels that consumers can use to verify resource ownership.
type InventoryOwnership struct {
	DockerLabels     map[string]string `json:"docker_labels"`
	KubernetesLabels map[string]string `json:"kubernetes_labels"`
}

// InventoryCommands contains bounded lifecycle commands for consumers.
type InventoryCommands struct {
	Ready   string `json:"ready"`
	Inspect string `json:"inspect"`
	Down    string `json:"down"`
}

// InventoryCluster describes one logical cluster without granting implicit access.
type InventoryCluster struct {
	LogicalName            string `json:"logical_name"`
	ResourceName           string `json:"resource_name"`
	DC                     string `json:"dc"`
	Zone                   string `json:"zone"`
	NetworkName            string `json:"network_name"`
	ControlPlaneAddress    string `json:"control_plane_address"`
	Kubeconfig             string `json:"kubeconfig"`
	Context                string `json:"context"`
	Namespace              string `json:"namespace"`
	WorkloadIdentityFormat string `json:"workload_identity_format"`
}

// Inventory returns the topology handoff after validating every running node container.
func (t *Topology) Inventory(ctx context.Context) (InventoryDocument, error) {
	addresses := make(map[string]string, len(t.config.Clusters()))
	for _, cluster := range t.config.Clusters() {
		container, err := t.validateContainer(ctx, cluster)
		if err != nil {
			return InventoryDocument{}, fmt.Errorf("inventory: %w", err)
		}
		attachment, ok := container.NetworkSettings.Networks[cluster.NetworkName]
		if !ok || net.ParseIP(attachment.IPAddress).To4() == nil {
			return InventoryDocument{}, fmt.Errorf(
				"topology: control-plane address is missing for %s",
				cluster.Name,
			)
		}
		addresses[cluster.LogicalName] = attachment.IPAddress
	}
	return t.inventoryDocument(addresses)
}

func (t *Topology) inventoryDocument(addresses map[string]string) (InventoryDocument, error) {
	commandPrefix := fmt.Sprintf(
		"go run ./tools/e2e-topology --instance %s --docker-context %s",
		t.config.Instance,
		t.config.DockerContext,
	)
	document := InventoryDocument{
		APIVersion:    InventoryAPIVersion,
		Task:          TaskKey,
		Instance:      t.config.Instance,
		DockerContext: t.config.DockerContext,
		Namespace:     Namespace,
		TunnelPort:    AllowedProbePort,
		Ownership: InventoryOwnership{
			DockerLabels: map[string]string{
				ownerLabelKey:    TaskKey,
				instanceLabelKey: t.config.Instance,
			},
			KubernetesLabels: map[string]string{
				"marketmesh.dev/owner-task":        TaskKey,
				"marketmesh.dev/topology-instance": t.config.Instance,
			},
		},
		Commands: InventoryCommands{
			Ready:   commandPrefix + " ready",
			Inspect: commandPrefix + " inspect",
			Down:    commandPrefix + " down",
		},
		Clusters: []InventoryCluster{},
	}

	for _, cluster := range t.config.Clusters() {
		if !filepath.IsAbs(cluster.Kubeconfig) {
			return InventoryDocument{}, fmt.Errorf("topology: kubeconfig for %s is not absolute", cluster.Name)
		}
		address := addresses[cluster.LogicalName]
		if net.ParseIP(address).To4() == nil {
			return InventoryDocument{}, fmt.Errorf("topology: invalid control-plane address for %s", cluster.Name)
		}
		document.Clusters = append(document.Clusters, InventoryCluster{
			LogicalName:            cluster.LogicalName,
			ResourceName:           cluster.Name,
			DC:                     cluster.DC,
			Zone:                   cluster.Zone,
			NetworkName:            cluster.NetworkName,
			ControlPlaneAddress:    address,
			Kubeconfig:             cluster.Kubeconfig,
			Context:                cluster.KubeContext,
			Namespace:              Namespace,
			WorkloadIdentityFormat: "<pod>/<namespace>/<logical-cluster>",
		})
	}
	return document, nil
}

func (t *Topology) writeInventory(ctx context.Context) (string, error) {
	document, err := t.Inventory(ctx)
	if err != nil {
		return "", err
	}
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encoding topology inventory: %w", err)
	}
	path := filepath.Join(t.config.StateDir, "inventory.json")
	if err := writePrivateFile(path, append(encoded, '\n')); err != nil {
		return "", fmt.Errorf("writing topology inventory: %w", err)
	}
	return path, nil
}
