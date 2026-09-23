package podchaos

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

const (
	topologyInventoryAPIVersion = "marketmesh.dev/e2e-topology/v1"
	topologyTargetAPIVersion    = "marketmesh.dev/e2e-topology/targets/v1"
	topologyOwnerTask           = "MM-28"
	topologyNamespace           = "marketmesh-system"
	topologyTunnelPort          = 30443
	maxTopologyInventoryBytes   = 64 * 1024
	workloadIdentityFormat      = "<pod>/<namespace>/<logical-cluster>"
)

var dockerContextPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,62}$`)

// TopologyInventory is the strict MM-28 handoff consumed by this scenario.
// Commands are decoded only to validate the complete schema; MM-32 never
// executes command strings from the document.
type TopologyInventory struct {
	APIVersion       string                     `json:"api_version"`
	TargetAPIVersion string                     `json:"target_api_version"`
	Task             string                     `json:"task"`
	Instance         string                     `json:"instance"`
	DockerContext    string                     `json:"docker_context"`
	Namespace        string                     `json:"namespace"`
	TunnelPort       int                        `json:"tunnel_port"`
	Ownership        TopologyInventoryOwnership `json:"ownership"`
	Commands         TopologyInventoryCommands  `json:"commands"`
	Clusters         []TopologyInventoryCluster `json:"clusters"`
}

// TopologyInventoryOwnership proves the disposable topology boundary.
type TopologyInventoryOwnership struct {
	DockerLabels     map[string]string `json:"docker_labels"`
	KubernetesLabels map[string]string `json:"kubernetes_labels"`
}

// TopologyInventoryCommands is informational and is never passed to a shell.
type TopologyInventoryCommands struct {
	Ready           string `json:"ready"`
	Inspect         string `json:"inspect"`
	Down            string `json:"down"`
	TargetsResolve  string `json:"targets_resolve"`
	TargetsValidate string `json:"targets_validate"`
	TargetsRebind   string `json:"targets_rebind"`
}

// TopologyInventoryCluster is one explicit physical cluster boundary.
type TopologyInventoryCluster struct {
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

// DecodeTopologyInventory accepts only the complete versioned MM-28 schema
// for the exact disposable MM-32 run. It performs no ambient discovery.
func DecodeTopologyInventory(reader io.Reader, runID string) (TopologyInventory, error) {
	if reader == nil || !isMM32RunID(runID) {
		return TopologyInventory{}, fmt.Errorf(
			"%w: topology inventory input is invalid",
			ErrInvalidConfiguration,
		)
	}
	limited := io.LimitReader(reader, maxTopologyInventoryBytes+1)
	data, err := io.ReadAll(limited)
	if err != nil {
		return TopologyInventory{}, errors.New("podchaos: reading topology inventory")
	}
	if len(data) == 0 || len(data) > maxTopologyInventoryBytes {
		return TopologyInventory{}, fmt.Errorf(
			"%w: topology inventory size is outside bounds",
			ErrInvalidConfiguration,
		)
	}
	if err := rejectDuplicateInventoryFields(data); err != nil {
		return TopologyInventory{}, err
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var inventory TopologyInventory
	if err := decoder.Decode(&inventory); err != nil {
		return TopologyInventory{}, fmt.Errorf(
			"%w: decoding topology inventory",
			ErrInvalidConfiguration,
		)
	}
	if err := requireInventoryEOF(decoder); err != nil {
		return TopologyInventory{}, err
	}
	if err := validateTopologyInventory(inventory, runID); err != nil {
		return TopologyInventory{}, err
	}
	return inventory, nil
}

// KubernetesTargets returns a defensive, deterministic conversion of the
// already validated inventory. The kubeconfig paths and contexts are consumed
// exactly as supplied; they are never derived from logical names.
func (inventory TopologyInventory) KubernetesTargets() []KubernetesTarget {
	clusters := slices.Clone(inventory.Clusters)
	slices.SortFunc(clusters, func(left, right TopologyInventoryCluster) int {
		return strings.Compare(left.LogicalName, right.LogicalName)
	})
	targets := make([]KubernetesTarget, 0, len(clusters))
	for _, cluster := range clusters {
		targets = append(targets, KubernetesTarget{
			DC:             DC(cluster.DC),
			Zone:           cluster.Zone,
			KubeconfigPath: cluster.Kubeconfig,
			ContextName:    cluster.Context,
		})
	}
	return targets
}

func validateTopologyInventory(inventory TopologyInventory, runID string) error {
	if inventory.APIVersion != topologyInventoryAPIVersion ||
		inventory.TargetAPIVersion != topologyTargetAPIVersion ||
		inventory.Task != topologyOwnerTask ||
		inventory.Instance != runID || len(inventory.Instance) > 20 ||
		!dockerContextPattern.MatchString(inventory.DockerContext) ||
		inventory.Namespace != topologyNamespace ||
		inventory.TunnelPort != topologyTunnelPort {
		return fmt.Errorf(
			"%w: topology inventory identity does not match the MM-32 run",
			ErrInvalidConfiguration,
		)
	}
	if !exactLabels(inventory.Ownership.DockerLabels, map[string]string{
		"com.marketmesh.task":     topologyOwnerTask,
		"com.marketmesh.topology": runID,
	}) || !exactLabels(inventory.Ownership.KubernetesLabels, map[string]string{
		"marketmesh.dev/owner-task":        topologyOwnerTask,
		"marketmesh.dev/topology-instance": runID,
	}) {
		return fmt.Errorf(
			"%w: topology inventory ownership is invalid",
			ErrInvalidConfiguration,
		)
	}
	commandPrefix := fmt.Sprintf(
		"go run ./tools/e2e-topology --instance %s --docker-context %s",
		inventory.Instance,
		inventory.DockerContext,
	)
	if inventory.Commands != (TopologyInventoryCommands{
		Ready:           commandPrefix + " ready",
		Inspect:         commandPrefix + " inspect",
		Down:            commandPrefix + " down",
		TargetsResolve:  commandPrefix + " targets resolve",
		TargetsValidate: commandPrefix + " targets validate --snapshot -",
		TargetsRebind:   commandPrefix + " targets rebind --transition -",
	}) {
		return fmt.Errorf(
			"%w: topology lifecycle commands are incomplete",
			ErrInvalidConfiguration,
		)
	}
	if len(inventory.Clusters) != 4 {
		return fmt.Errorf(
			"%w: topology inventory must contain exactly four clusters",
			ErrInvalidConfiguration,
		)
	}

	expected := map[string]struct {
		dc   string
		zone string
	}{
		"dc-a-dmz":      {dc: "dc-a", zone: ZoneDMZ},
		"dc-a-internal": {dc: "dc-a", zone: ZoneInternal},
		"dc-b-dmz":      {dc: "dc-b", zone: ZoneDMZ},
		"dc-b-internal": {dc: "dc-b", zone: ZoneInternal},
	}
	seenPhysical := make(map[string]struct{}, len(inventory.Clusters))
	seenAddresses := make(map[netip.Addr]struct{}, len(inventory.Clusters))
	for _, cluster := range inventory.Clusters {
		want, exists := expected[cluster.LogicalName]
		if !exists || cluster.DC != want.dc || cluster.Zone != want.zone {
			return fmt.Errorf(
				"%w: topology inventory has an unknown logical cluster",
				ErrInvalidConfiguration,
			)
		}
		delete(expected, cluster.LogicalName)
		if cluster.ResourceName != runID+"-"+cluster.LogicalName ||
			cluster.NetworkName != cluster.ResourceName ||
			cluster.Context != "kind-"+cluster.ResourceName ||
			cluster.Namespace != topologyNamespace ||
			cluster.WorkloadIdentityFormat != workloadIdentityFormat ||
			!isExactArgument(cluster.Context) ||
			!filepath.IsAbs(cluster.Kubeconfig) ||
			filepath.Clean(cluster.Kubeconfig) != cluster.Kubeconfig ||
			cluster.Kubeconfig == string(filepath.Separator) {
			return fmt.Errorf(
				"%w: topology cluster boundary is invalid",
				ErrInvalidConfiguration,
			)
		}
		address, err := netip.ParseAddr(cluster.ControlPlaneAddress)
		if err != nil || !address.Is4() || address.IsUnspecified() ||
			address.IsLoopback() || address.IsMulticast() || !address.IsPrivate() {
			return fmt.Errorf(
				"%w: topology control-plane address is invalid",
				ErrInvalidConfiguration,
			)
		}
		if _, exists := seenAddresses[address]; exists {
			return fmt.Errorf(
				"%w: topology control-plane address is duplicated",
				ErrInvalidConfiguration,
			)
		}
		seenAddresses[address] = struct{}{}
		physical := cluster.Kubeconfig + "\x00" + cluster.Context
		if _, exists := seenPhysical[physical]; exists {
			return fmt.Errorf(
				"%w: topology kubernetes target is duplicated",
				ErrInvalidConfiguration,
			)
		}
		seenPhysical[physical] = struct{}{}
	}
	if len(expected) != 0 {
		return fmt.Errorf(
			"%w: topology inventory is incomplete",
			ErrInvalidConfiguration,
		)
	}
	return nil
}

func exactLabels(actual, expected map[string]string) bool {
	if len(actual) != len(expected) {
		return false
	}
	for key, value := range expected {
		if actual[key] != value {
			return false
		}
	}
	return true
}

func rejectDuplicateInventoryFields(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := inspectInventoryJSONValue(decoder); err != nil {
		return fmt.Errorf(
			"%w: topology inventory contains ambiguous JSON",
			ErrInvalidConfiguration,
		)
	}
	return requireInventoryEOF(decoder)
}

func inspectInventoryJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]struct{})
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return err
			}
			key, ok := keyToken.(string)
			if !ok {
				return errors.New("object key is not a string")
			}
			if _, exists := seen[key]; exists {
				return errors.New("duplicate object key")
			}
			seen[key] = struct{}{}
			if err := inspectInventoryJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim('}') {
			return errors.New("unterminated object")
		}
	case '[':
		for decoder.More() {
			if err := inspectInventoryJSONValue(decoder); err != nil {
				return err
			}
		}
		end, err := decoder.Token()
		if err != nil || end != json.Delim(']') {
			return errors.New("unterminated array")
		}
	default:
		return errors.New("unexpected JSON delimiter")
	}
	return nil
}

func requireInventoryEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return fmt.Errorf(
			"%w: topology inventory has trailing data",
			ErrInvalidConfiguration,
		)
	}
	return nil
}
