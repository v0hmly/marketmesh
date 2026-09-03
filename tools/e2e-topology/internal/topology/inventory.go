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
	APIVersion       string             `json:"api_version"`
	TargetAPIVersion string             `json:"target_api_version"`
	Task             string             `json:"task"`
	Instance         string             `json:"instance"`
	Runtime          string             `json:"runtime"`
	Namespace        string             `json:"namespace"`
	TunnelPort       int                `json:"tunnel_port"`
	Ownership        InventoryOwnership `json:"ownership"`
	Commands         InventoryCommands  `json:"commands"`
	Clusters         []InventoryCluster `json:"clusters"`
}

// InventoryOwnership lists labels that consumers can use to verify resource ownership.
type InventoryOwnership struct {
	KubernetesLabels map[string]string `json:"kubernetes_labels"`
}

// InventoryCommands contains bounded lifecycle commands for consumers.
type InventoryCommands struct {
	Ready           string `json:"ready"`
	Inspect         string `json:"inspect"`
	Down            string `json:"down"`
	TargetsResolve  string `json:"targets_resolve"`
	TargetsValidate string `json:"targets_validate"`
	TargetsRebind   string `json:"targets_rebind"`
}

// InventoryCluster describes one logical cluster without granting implicit access.
type InventoryCluster struct {
	LogicalName            string `json:"logical_name"`
	ResourceName           string `json:"resource_name"`
	DC                     string `json:"dc"`
	Zone                   string `json:"zone"`
	ControlPlaneAddress    string `json:"control_plane_address"`
	Kubeconfig             string `json:"kubeconfig"`
	Context                string `json:"context"`
	Namespace              string `json:"namespace"`
	WorkloadIdentityFormat string `json:"workload_identity_format"`
}

// Inventory returns the topology handoff after validating every running machine.
func (t *Topology) Inventory(ctx context.Context) (InventoryDocument, error) {
	addresses := make(map[string]string, len(t.config.Clusters()))
	for _, cluster := range t.config.Clusters() {
		machine, err := t.requireRunningMachine(ctx, cluster)
		if err != nil {
			return InventoryDocument{}, fmt.Errorf("inventory: %w", err)
		}
		addresses[cluster.LogicalName] = machine.IPv4
	}
	return t.inventoryDocument(addresses)
}

func (t *Topology) inventoryDocument(addresses map[string]string) (InventoryDocument, error) {
	commandPrefix := fmt.Sprintf("go run ./tools/e2e-topology --instance %s", t.config.Instance)
	document := InventoryDocument{
		APIVersion:       InventoryAPIVersion,
		TargetAPIVersion: TargetAPIVersion,
		Task:             TaskKey,
		Instance:         t.config.Instance,
		Runtime:          RuntimeName,
		Namespace:        Namespace,
		TunnelPort:       AllowedProbePort,
		Ownership: InventoryOwnership{
			KubernetesLabels: map[string]string{
				"marketmesh.dev/owner-task":        TaskKey,
				"marketmesh.dev/topology-instance": t.config.Instance,
			},
		},
		Commands: InventoryCommands{
			Ready:           commandPrefix + " ready",
			Inspect:         commandPrefix + " inspect",
			Down:            commandPrefix + " down",
			TargetsResolve:  commandPrefix + " targets resolve",
			TargetsValidate: commandPrefix + " targets validate --snapshot -",
			TargetsRebind:   commandPrefix + " targets rebind --transition -",
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
