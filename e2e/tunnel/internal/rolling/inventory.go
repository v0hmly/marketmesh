package rolling

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"regexp"
)

const (
	topologyInventoryAPIVersion = "marketmesh.dev/e2e-topology/v1"
	topologyTaskKey             = "MM-28"
	topologyNamespace           = "marketmesh-system"
	topologyTunnelPort          = 30443
	maximumInventoryBytes       = 1024 * 1024
)

var (
	topologyInstancePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,18}[a-z0-9])?$`)
	dockerContextPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,62}$`)
)

type topologyInventory struct {
	APIVersion    string             `json:"api_version"`
	Task          string             `json:"task"`
	Instance      string             `json:"instance"`
	DockerContext string             `json:"docker_context"`
	Namespace     string             `json:"namespace"`
	TunnelPort    int                `json:"tunnel_port"`
	Ownership     inventoryOwnership `json:"ownership"`
	Commands      inventoryCommands  `json:"commands"`
	Clusters      []inventoryCluster `json:"clusters"`
}

type inventoryOwnership struct {
	DockerLabels     map[string]string `json:"docker_labels"`
	KubernetesLabels map[string]string `json:"kubernetes_labels"`
}

type inventoryCommands struct {
	Ready   string `json:"ready"`
	Inspect string `json:"inspect"`
	Down    string `json:"down"`
}

type inventoryCluster struct {
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

// DecodeTopologyInventory consumes the public MM-28 inventory v1 contract.
// Lifecycle command strings are treated as metadata and are never executed.
func DecodeTopologyInventory(reader io.Reader) ([]Cluster, error) {
	if isNilDependency(reader) {
		return nil, errors.New("rolling: topology inventory reader is required")
	}
	encoded, err := io.ReadAll(io.LimitReader(reader, maximumInventoryBytes+1))
	if err != nil {
		return nil, errors.New("rolling: reading topology inventory")
	}
	if len(encoded) > maximumInventoryBytes {
		return nil, errors.New("rolling: topology inventory exceeds size limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.DisallowUnknownFields()
	var inventory topologyInventory
	if err := decoder.Decode(&inventory); err != nil {
		return nil, fmt.Errorf("rolling: decoding topology inventory: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, err
	}

	return validateTopologyInventory(inventory)
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("rolling: topology inventory contains trailing data")
	}

	return nil
}

func validateTopologyInventory(inventory topologyInventory) ([]Cluster, error) {
	if inventory.APIVersion != topologyInventoryAPIVersion || inventory.Task != topologyTaskKey {
		return nil, errors.New("rolling: unsupported topology inventory identity")
	}
	if !topologyInstancePattern.MatchString(inventory.Instance) {
		return nil, errors.New("rolling: topology instance is outside bounds")
	}
	if !dockerContextPattern.MatchString(inventory.DockerContext) {
		return nil, errors.New("rolling: topology docker context is outside bounds")
	}
	if inventory.Namespace != topologyNamespace || inventory.TunnelPort != topologyTunnelPort {
		return nil, errors.New("rolling: topology network contract does not match MM-28")
	}
	if inventory.Ownership.DockerLabels["com.marketmesh.task"] != topologyTaskKey ||
		inventory.Ownership.DockerLabels["com.marketmesh.topology"] != inventory.Instance ||
		inventory.Ownership.KubernetesLabels["marketmesh.dev/owner-task"] != topologyTaskKey ||
		inventory.Ownership.KubernetesLabels["marketmesh.dev/topology-instance"] != inventory.Instance {
		return nil, errors.New("rolling: topology ownership metadata does not match MM-28")
	}
	if len(inventory.Clusters) != 4 {
		return nil, errors.New("rolling: topology inventory must contain four clusters")
	}

	clusters := make([]Cluster, 0, len(inventory.Clusters))
	seen := make(map[string]struct{}, len(inventory.Clusters))
	for _, candidate := range inventory.Clusters {
		cluster, err := validateInventoryCluster(inventory.Instance, candidate)
		if err != nil {
			return nil, err
		}
		if _, found := seen[cluster.LogicalName]; found {
			return nil, errors.New("rolling: topology inventory contains a duplicate cluster")
		}
		seen[cluster.LogicalName] = struct{}{}
		clusters = append(clusters, cluster)
	}
	for _, logicalName := range []string{"dc-a-dmz", "dc-a-internal", "dc-b-dmz", "dc-b-internal"} {
		if _, found := seen[logicalName]; !found {
			return nil, fmt.Errorf("rolling: topology cluster %s is missing", logicalName)
		}
	}

	return clusters, nil
}

func validateInventoryCluster(instance string, candidate inventoryCluster) (Cluster, error) {
	expectedLogicalName := candidate.DC + "-" + candidate.Zone
	if (candidate.DC != "dc-a" && candidate.DC != "dc-b") ||
		(candidate.Zone != "dmz" && candidate.Zone != "internal") ||
		candidate.LogicalName != expectedLogicalName {
		return Cluster{}, errors.New("rolling: topology cluster identity is invalid")
	}
	expectedResourceName := instance + "-" + candidate.LogicalName
	if candidate.ResourceName != expectedResourceName || candidate.NetworkName != expectedResourceName ||
		candidate.Context != "kind-"+expectedResourceName {
		return Cluster{}, fmt.Errorf("rolling: topology resource identity is invalid for %s", candidate.LogicalName)
	}
	if net.ParseIP(candidate.ControlPlaneAddress).To4() == nil {
		return Cluster{}, fmt.Errorf("rolling: topology address is invalid for %s", candidate.LogicalName)
	}
	if !filepath.IsAbs(candidate.Kubeconfig) || candidate.Namespace != topologyNamespace ||
		candidate.WorkloadIdentityFormat != "<pod>/<namespace>/<logical-cluster>" {
		return Cluster{}, fmt.Errorf("rolling: topology handoff is invalid for %s", candidate.LogicalName)
	}

	return Cluster{
		LogicalName:      candidate.LogicalName,
		ResourceName:     candidate.ResourceName,
		TopologyInstance: instance,
		DC:               candidate.DC,
		Zone:             candidate.Zone,
		Kubeconfig:       candidate.Kubeconfig,
		Context:          candidate.Context,
	}, nil
}
