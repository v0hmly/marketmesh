package topology

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"slices"
	"strings"
	"time"
)

const (
	// TargetAPIVersion identifies the immutable fault-target snapshot schema.
	TargetAPIVersion = "marketmesh.dev/e2e-topology/targets/v1"
	// TargetValidationAPIVersion identifies a successful revalidation receipt.
	TargetValidationAPIVersion = "marketmesh.dev/e2e-topology/target-validation/v1"
	// TargetRebindAPIVersion identifies a safe stopped-to-running binding transition.
	TargetRebindAPIVersion = "marketmesh.dev/e2e-topology/target-rebind/v1"
	// TargetEnvironment prevents snapshots from being mistaken for non-local resources.
	TargetEnvironment = "local-e2e-disposable"

	ExpectedStateRunning ExpectedTargetState = "running"
	ExpectedStateStopped ExpectedTargetState = "stopped"
)

var (
	consumerTaskPattern  = regexp.MustCompile(`^MM-[0-9]{1,6}$`)
	consumerRunIDPattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{1,61}[a-z0-9])$`)
	dockerIDPattern      = regexp.MustCompile(`^[a-f0-9]{64}$`)
	imageIDPattern       = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
	interfaceNamePattern = regexp.MustCompile(`^eth[0-9]+$`)
	netNSPattern         = regexp.MustCompile(`^net:\[[0-9]+\]$`)
)

// ExpectedTargetState is the only mutable container state accepted by validation.
type ExpectedTargetState string

// TargetSelector scopes one immutable snapshot without inferring resource names.
type TargetSelector struct {
	DC   string `json:"dc,omitempty"`
	Zone string `json:"zone,omitempty"`
}

// TargetResolveRequest binds a snapshot to one consumer run.
type TargetResolveRequest struct {
	ConsumerTask  string
	ConsumerRunID string
	Selector      TargetSelector
}

// TargetValidateRequest selects exact snapshot targets and the required current state.
type TargetValidateRequest struct {
	ExpectedState ExpectedTargetState
	LogicalNames  []string
}

// TargetSnapshot is a read-only, versioned handoff for fault consumers.
type TargetSnapshot struct {
	APIVersion    string         `json:"api_version"`
	Task          string         `json:"task"`
	Environment   string         `json:"environment"`
	Instance      string         `json:"instance"`
	DockerContext string         `json:"docker_context"`
	ConsumerTask  string         `json:"consumer_task"`
	ConsumerRunID string         `json:"consumer_run_id"`
	ResolvedAt    string         `json:"resolved_at"`
	Selector      TargetSelector `json:"selector"`
	Targets       []FaultTarget  `json:"targets"`
	PreviousToken string         `json:"previous_token,omitempty"`
	Token         string         `json:"token"`
}

// FaultTarget identifies one kind node and all topology-owned network attachments.
type FaultTarget struct {
	LogicalCluster  string                    `json:"logical_cluster"`
	ResourceCluster string                    `json:"resource_cluster"`
	DC              string                    `json:"dc"`
	Zone            string                    `json:"zone"`
	Kubeconfig      string                    `json:"kubeconfig"`
	KubeContext     string                    `json:"kube_context"`
	Namespace       string                    `json:"namespace"`
	Container       FaultTargetContainer      `json:"container"`
	KubernetesNode  FaultTargetKubernetesNode `json:"kubernetes_node"`
	SandboxID       string                    `json:"sandbox_id"`
	NetNS           string                    `json:"netns"`
	Networks        []FaultTargetNetwork      `json:"networks"`
}

// FaultTargetContainer contains immutable Docker container identity.
type FaultTargetContainer struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	ImageID        string            `json:"image_id"`
	ImageReference string            `json:"image_reference"`
	StartedAt      string            `json:"started_at"`
	Labels         map[string]string `json:"labels"`
}

// FaultTargetKubernetesNode contains exact Kubernetes identity and ownership.
type FaultTargetKubernetesNode struct {
	Name   string            `json:"name"`
	UID    string            `json:"uid"`
	Labels map[string]string `json:"labels"`
}

// FaultTargetNetwork binds one Docker network, endpoint, and in-netns interface.
type FaultTargetNetwork struct {
	LogicalNetwork string               `json:"logical_network"`
	Primary        bool                 `json:"primary"`
	ID             string               `json:"id"`
	Name           string               `json:"name"`
	Driver         string               `json:"driver"`
	Scope          string               `json:"scope"`
	Subnet         string               `json:"subnet"`
	Labels         map[string]string    `json:"labels"`
	Endpoint       FaultTargetEndpoint  `json:"endpoint"`
	Interface      FaultTargetInterface `json:"interface"`
}

// FaultTargetEndpoint is cross-checked between container and network inspection.
type FaultTargetEndpoint struct {
	ID        string `json:"id"`
	NetworkID string `json:"network_id"`
	Address   string `json:"address"`
	Gateway   string `json:"gateway"`
	MAC       string `json:"mac"`
}

// FaultTargetInterface identifies the exact interface inside the container netns.
type FaultTargetInterface struct {
	Name    string `json:"name"`
	Index   int    `json:"index"`
	Address string `json:"address"`
	MAC     string `json:"mac"`
}

// TargetValidationReceipt proves that exact snapshot targets passed a fresh inspection.
type TargetValidationReceipt struct {
	APIVersion    string                 `json:"api_version"`
	SnapshotToken string                 `json:"snapshot_token"`
	ExpectedState ExpectedTargetState    `json:"expected_state"`
	Validated     bool                   `json:"validated"`
	ValidatedAt   string                 `json:"validated_at"`
	Targets       []ValidatedFaultTarget `json:"targets"`
	ReceiptDigest string                 `json:"receipt_digest"`
}

// ValidatedFaultTarget reports only the immutable ID and observed state.
type ValidatedFaultTarget struct {
	LogicalCluster string   `json:"logical_cluster"`
	ContainerID    string   `json:"container_id"`
	State          string   `json:"state"`
	StartedAt      string   `json:"started_at"`
	FinishedAt     string   `json:"finished_at"`
	NetworkIDs     []string `json:"network_ids"`
}

// TargetRebindInput supplies the original binding and exact stopped receipt.
type TargetRebindInput struct {
	Snapshot       TargetSnapshot          `json:"snapshot"`
	StoppedReceipt TargetValidationReceipt `json:"stopped_receipt"`
}

// TargetRebindResult contains a refreshed snapshot and its deterministic transition proof.
type TargetRebindResult struct {
	APIVersion string                 `json:"api_version"`
	Snapshot   TargetSnapshot         `json:"snapshot"`
	Transition TargetRebindTransition `json:"transition"`
}

// TargetRebindTransition links exactly one old token to one refreshed target binding.
type TargetRebindTransition struct {
	FromToken            string `json:"from_token"`
	ToToken              string `json:"to_token"`
	LogicalCluster       string `json:"logical_cluster"`
	ContainerID          string `json:"container_id"`
	StartedAt            string `json:"started_at"`
	StoppedReceiptDigest string `json:"stopped_receipt_digest"`
	TransitionDigest     string `json:"transition_digest"`
}

type interfaceInspection struct {
	Index       int                    `json:"ifindex"`
	Name        string                 `json:"ifname"`
	MAC         string                 `json:"address"`
	AddressInfo []interfaceAddressInfo `json:"addr_info"`
}

type interfaceAddressInfo struct {
	Family    string `json:"family"`
	Local     string `json:"local"`
	PrefixLen int    `json:"prefixlen"`
}

type observedTargetState struct {
	status     string
	startedAt  string
	finishedAt string
}

type targetNetworkResolveInput struct {
	cluster        Cluster
	networkCluster Cluster
	container      dockerContainer
	interfaces     []interfaceInspection
}

// ResolveTargets publishes exact runtime identities only after complete live validation.
func (t *Topology) ResolveTargets(
	ctx context.Context,
	request TargetResolveRequest,
) (TargetSnapshot, error) {
	if err := validateConsumer(request.ConsumerTask, request.ConsumerRunID); err != nil {
		return TargetSnapshot{}, err
	}
	clusters, err := t.selectedClusters(request.Selector)
	if err != nil {
		return TargetSnapshot{}, err
	}

	snapshot := TargetSnapshot{
		APIVersion:    TargetAPIVersion,
		Task:          TaskKey,
		Environment:   TargetEnvironment,
		Instance:      t.config.Instance,
		DockerContext: t.config.DockerContext,
		ConsumerTask:  request.ConsumerTask,
		ConsumerRunID: request.ConsumerRunID,
		ResolvedAt:    t.now().UTC().Format(time.RFC3339Nano),
		Selector:      request.Selector,
		Targets:       make([]FaultTarget, 0, len(clusters)),
	}
	for _, cluster := range clusters {
		target, resolveErr := t.resolveTarget(ctx, cluster)
		if resolveErr != nil {
			return TargetSnapshot{}, fmt.Errorf("resolving target %s: %w", cluster.LogicalName, resolveErr)
		}
		snapshot.Targets = append(snapshot.Targets, target)
	}

	token, err := targetSnapshotToken(snapshot)
	if err != nil {
		return TargetSnapshot{}, err
	}
	snapshot.Token = token
	return snapshot, nil
}

// ValidateTargets re-inspects immutable IDs from a snapshot and never resolves replacements by name.
func (t *Topology) ValidateTargets(
	ctx context.Context,
	snapshot TargetSnapshot,
	request TargetValidateRequest,
) (TargetValidationReceipt, error) {
	if err := t.validateSnapshot(snapshot); err != nil {
		return TargetValidationReceipt{}, err
	}
	if request.ExpectedState != ExpectedStateRunning && request.ExpectedState != ExpectedStateStopped {
		return TargetValidationReceipt{}, errors.New("topology: expected state must be running or stopped")
	}
	selected, err := selectSnapshotTargets(snapshot.Targets, request.LogicalNames)
	if err != nil {
		return TargetValidationReceipt{}, err
	}

	receipt := TargetValidationReceipt{
		APIVersion:    TargetValidationAPIVersion,
		SnapshotToken: snapshot.Token,
		ExpectedState: request.ExpectedState,
		Validated:     true,
		ValidatedAt:   t.now().UTC().Format(time.RFC3339Nano),
		Targets:       make([]ValidatedFaultTarget, 0, len(selected)),
	}
	for _, target := range selected {
		state, validateErr := t.validateTargetRuntime(ctx, target, request.ExpectedState)
		if validateErr != nil {
			return TargetValidationReceipt{}, fmt.Errorf("validating target %s: %w", target.LogicalCluster, validateErr)
		}
		networkIDs := make([]string, 0, len(target.Networks))
		for _, network := range target.Networks {
			networkIDs = append(networkIDs, network.ID)
		}
		receipt.Targets = append(receipt.Targets, ValidatedFaultTarget{
			LogicalCluster: target.LogicalCluster,
			ContainerID:    target.Container.ID,
			State:          state.status,
			StartedAt:      state.startedAt,
			FinishedAt:     state.finishedAt,
			NetworkIDs:     networkIDs,
		})
	}
	digest, err := targetValidationReceiptDigest(receipt)
	if err != nil {
		return TargetValidationReceipt{}, err
	}
	receipt.ReceiptDigest = digest
	return receipt, nil
}

// RebindTarget accepts only a proved stop/start transition of the same immutable target.
func (t *Topology) RebindTarget(
	ctx context.Context,
	input TargetRebindInput,
	logicalName string,
) (TargetRebindResult, error) {
	if err := t.validateSnapshot(input.Snapshot); err != nil {
		return TargetRebindResult{}, err
	}
	targets, err := selectSnapshotTargets(input.Snapshot.Targets, []string{logicalName})
	if err != nil {
		return TargetRebindResult{}, err
	}
	target := targets[0]
	if err := validateStoppedReceipt(input.StoppedReceipt, input.Snapshot, target); err != nil {
		return TargetRebindResult{}, err
	}

	refreshedTarget, err := t.rebindRunningTarget(ctx, target, input.StoppedReceipt.Targets[0])
	if err != nil {
		return TargetRebindResult{}, fmt.Errorf("rebinding target %s: %w", logicalName, err)
	}
	refreshedSnapshot := input.Snapshot
	refreshedSnapshot.Targets = slices.Clone(input.Snapshot.Targets)
	for index := range refreshedSnapshot.Targets {
		if refreshedSnapshot.Targets[index].LogicalCluster == logicalName {
			refreshedSnapshot.Targets[index] = refreshedTarget
		}
	}
	refreshedSnapshot.PreviousToken = input.Snapshot.Token
	refreshedSnapshot.Token = ""
	token, err := targetSnapshotToken(refreshedSnapshot)
	if err != nil {
		return TargetRebindResult{}, err
	}
	refreshedSnapshot.Token = token

	transition := TargetRebindTransition{
		FromToken:            input.Snapshot.Token,
		ToToken:              refreshedSnapshot.Token,
		LogicalCluster:       logicalName,
		ContainerID:          target.Container.ID,
		StartedAt:            refreshedTarget.Container.StartedAt,
		StoppedReceiptDigest: input.StoppedReceipt.ReceiptDigest,
	}
	digest, err := targetRebindTransitionDigest(transition)
	if err != nil {
		return TargetRebindResult{}, err
	}
	transition.TransitionDigest = digest
	return TargetRebindResult{
		APIVersion: TargetRebindAPIVersion,
		Snapshot:   refreshedSnapshot,
		Transition: transition,
	}, nil
}

func (t *Topology) rebindRunningTarget(
	ctx context.Context,
	target FaultTarget,
	stopped ValidatedFaultTarget,
) (FaultTarget, error) {
	cluster, err := t.config.Cluster(target.DC, target.Zone)
	if err != nil {
		return FaultTarget{}, err
	}
	container, err := t.inspectContainer(ctx, target.Container.ID)
	if err != nil {
		return FaultTarget{}, err
	}
	if container.ID != target.Container.ID || strings.TrimPrefix(container.Name, "/") != target.Container.Name ||
		container.Image != target.Container.ImageID || container.Config.Image != target.Container.ImageReference ||
		container.Config.Labels[clusterLabelKey] != target.Container.Labels[clusterLabelKey] {
		return FaultTarget{}, errors.New("topology: immutable container identity changed during rebind")
	}
	if err := validateObservedState(container, ExpectedStateRunning); err != nil {
		return FaultTarget{}, err
	}
	startedAt, err := time.Parse(time.RFC3339Nano, container.State.StartedAt)
	if err != nil {
		return FaultTarget{}, errors.New("topology: rebound container started_at is invalid")
	}
	finishedAt, err := time.Parse(time.RFC3339Nano, stopped.FinishedAt)
	if err != nil || !startedAt.After(finishedAt) || container.State.StartedAt == target.Container.StartedAt ||
		container.State.FinishedAt != stopped.FinishedAt {
		return FaultTarget{}, errors.New("topology: no proved stopped-to-started generation transition")
	}
	if !dockerIDPattern.MatchString(container.NetworkSettings.SandboxID) {
		return FaultTarget{}, errors.New("topology: rebound docker sandbox identity is invalid")
	}
	if len(container.NetworkSettings.Networks) != len(target.Networks) {
		return FaultTarget{}, errors.New("topology: rebound container has unexpected network attachments")
	}

	node, err := t.resolveKubernetesNode(ctx, cluster)
	if err != nil {
		return FaultTarget{}, err
	}
	if node.UID != target.KubernetesNode.UID || !exactLabels(node.Labels, target.KubernetesNode.Labels) {
		return FaultTarget{}, errors.New("topology: kubernetes node identity changed during rebind")
	}
	netNS, err := t.inspectNetNS(ctx, container.ID)
	if err != nil {
		return FaultTarget{}, err
	}
	interfaces, err := t.inspectInterfaces(ctx, container.ID)
	if err != nil {
		return FaultTarget{}, err
	}
	refreshedNetworks := make([]FaultTargetNetwork, 0, len(target.Networks))
	for _, oldNetwork := range target.Networks {
		networkCluster, networkErr := findNetworkCluster(t.config.Clusters(), oldNetwork.LogicalNetwork)
		if networkErr != nil {
			return FaultTarget{}, networkErr
		}
		network, networkErr := t.inspectNetwork(ctx, oldNetwork.ID)
		if networkErr != nil {
			return FaultTarget{}, networkErr
		}
		if network.ID != oldNetwork.ID || network.Name != oldNetwork.Name {
			return FaultTarget{}, errors.New("topology: immutable network identity changed during rebind")
		}
		if networkErr := validateTargetNetworkBase(t.config.Instance, networkCluster, network); networkErr != nil {
			return FaultTarget{}, networkErr
		}
		attachment, ok := container.NetworkSettings.Networks[oldNetwork.Name]
		if !ok {
			return FaultTarget{}, errors.New("topology: rebound network attachment is missing")
		}
		endpoint, networkErr := validateTargetEndpoint(container, network, attachment)
		if networkErr != nil {
			return FaultTarget{}, networkErr
		}
		if endpoint.Address != oldNetwork.Endpoint.Address || endpoint.Gateway != oldNetwork.Endpoint.Gateway {
			return FaultTarget{}, errors.New("topology: rebound network address changed")
		}
		interfaceIdentity, networkErr := findTargetInterface(interfaces, endpoint)
		if networkErr != nil {
			return FaultTarget{}, networkErr
		}
		refreshedNetwork := oldNetwork
		refreshedNetwork.Endpoint = endpoint
		refreshedNetwork.Interface = interfaceIdentity
		refreshedNetworks = append(refreshedNetworks, refreshedNetwork)
	}

	refreshed := target
	refreshed.Container.StartedAt = container.State.StartedAt
	refreshed.KubernetesNode = node
	refreshed.SandboxID = container.NetworkSettings.SandboxID
	refreshed.NetNS = netNS
	refreshed.Networks = refreshedNetworks
	return refreshed, nil
}

func validateStoppedReceipt(
	receipt TargetValidationReceipt,
	snapshot TargetSnapshot,
	target FaultTarget,
) error {
	if receipt.APIVersion != TargetValidationAPIVersion || !receipt.Validated ||
		receipt.ExpectedState != ExpectedStateStopped || receipt.SnapshotToken != snapshot.Token ||
		len(receipt.Targets) != 1 {
		return errors.New("topology: stopped receipt metadata mismatch")
	}
	expectedDigest, err := targetValidationReceiptDigest(receipt)
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare([]byte(receipt.ReceiptDigest), []byte(expectedDigest)) != 1 {
		return errors.New("topology: stopped receipt digest mismatch")
	}
	validatedAt, err := time.Parse(time.RFC3339Nano, receipt.ValidatedAt)
	if err != nil {
		return errors.New("topology: stopped receipt validated_at is invalid")
	}
	observed := receipt.Targets[0]
	expectedNetworkIDs := make([]string, 0, len(target.Networks))
	for _, network := range target.Networks {
		expectedNetworkIDs = append(expectedNetworkIDs, network.ID)
	}
	finishedAt, err := time.Parse(time.RFC3339Nano, observed.FinishedAt)
	if observed.LogicalCluster != target.LogicalCluster || observed.ContainerID != target.Container.ID ||
		observed.State != "exited" || observed.StartedAt != target.Container.StartedAt ||
		err != nil || validatedAt.Before(finishedAt) || !slices.Equal(observed.NetworkIDs, expectedNetworkIDs) {
		return errors.New("topology: stopped receipt target identity mismatch")
	}
	return nil
}

func (t *Topology) resolveTarget(ctx context.Context, cluster Cluster) (FaultTarget, error) {
	container, err := t.inspectContainer(ctx, cluster.NodeName)
	if err != nil {
		return FaultTarget{}, err
	}
	if err := validateRunningContainer(cluster, container); err != nil {
		return FaultTarget{}, err
	}
	expectedNetworks, err := t.expectedNetworkClusters(cluster)
	if err != nil {
		return FaultTarget{}, err
	}
	if len(container.NetworkSettings.Networks) != len(expectedNetworks) {
		return FaultTarget{}, fmt.Errorf("topology: container %s has unexpected network attachments", cluster.NodeName)
	}

	node, err := t.resolveKubernetesNode(ctx, cluster)
	if err != nil {
		return FaultTarget{}, err
	}
	netNS, err := t.inspectNetNS(ctx, container.ID)
	if err != nil {
		return FaultTarget{}, err
	}
	interfaces, err := t.inspectInterfaces(ctx, container.ID)
	if err != nil {
		return FaultTarget{}, err
	}

	target := FaultTarget{
		LogicalCluster:  cluster.LogicalName,
		ResourceCluster: cluster.Name,
		DC:              cluster.DC,
		Zone:            cluster.Zone,
		Kubeconfig:      cluster.Kubeconfig,
		KubeContext:     cluster.KubeContext,
		Namespace:       Namespace,
		Container: FaultTargetContainer{
			ID:             container.ID,
			Name:           cluster.NodeName,
			ImageID:        container.Image,
			ImageReference: container.Config.Image,
			StartedAt:      container.State.StartedAt,
			Labels: map[string]string{
				clusterLabelKey: cluster.Name,
			},
		},
		KubernetesNode: node,
		SandboxID:      container.NetworkSettings.SandboxID,
		NetNS:          netNS,
		Networks:       make([]FaultTargetNetwork, 0, len(expectedNetworks)),
	}
	for _, networkCluster := range expectedNetworks {
		network, networkErr := t.resolveTargetNetwork(ctx, targetNetworkResolveInput{
			cluster:        cluster,
			networkCluster: networkCluster,
			container:      container,
			interfaces:     interfaces,
		})
		if networkErr != nil {
			return FaultTarget{}, networkErr
		}
		target.Networks = append(target.Networks, network)
	}
	slices.SortFunc(target.Networks, func(left, right FaultTargetNetwork) int {
		return strings.Compare(left.LogicalNetwork, right.LogicalNetwork)
	})
	return target, nil
}

func (t *Topology) resolveTargetNetwork(
	ctx context.Context,
	input targetNetworkResolveInput,
) (FaultTargetNetwork, error) {
	network, err := t.inspectNetwork(ctx, input.networkCluster.NetworkName)
	if err != nil {
		return FaultTargetNetwork{}, err
	}
	if err := validateTargetNetworkBase(t.config.Instance, input.networkCluster, network); err != nil {
		return FaultTargetNetwork{}, err
	}
	attachment, ok := input.container.NetworkSettings.Networks[input.networkCluster.NetworkName]
	if !ok {
		return FaultTargetNetwork{}, fmt.Errorf(
			"topology: expected attachment %s is missing",
			input.networkCluster.NetworkName,
		)
	}
	endpoint, err := validateTargetEndpoint(input.container, network, attachment)
	if err != nil {
		return FaultTargetNetwork{}, err
	}
	if !addressWithinSubnet(endpoint.Address, input.networkCluster.DockerSubnet) ||
		!ipWithinSubnet(endpoint.Gateway, input.networkCluster.DockerSubnet) {
		return FaultTargetNetwork{}, errors.New("topology: endpoint address or gateway does not match the network subnet")
	}
	interfaceIdentity, err := findTargetInterface(input.interfaces, endpoint)
	if err != nil {
		return FaultTargetNetwork{}, err
	}
	return FaultTargetNetwork{
		LogicalNetwork: input.networkCluster.LogicalName,
		Primary:        input.networkCluster.LogicalName == input.cluster.LogicalName,
		ID:             network.ID,
		Name:           network.Name,
		Driver:         network.Driver,
		Scope:          network.Scope,
		Subnet:         input.networkCluster.DockerSubnet,
		Labels: map[string]string{
			ownerLabelKey:    TaskKey,
			instanceLabelKey: t.config.Instance,
		},
		Endpoint:  endpoint,
		Interface: interfaceIdentity,
	}, nil
}

func validateRunningContainer(cluster Cluster, container dockerContainer) error {
	if !dockerIDPattern.MatchString(container.ID) {
		return fmt.Errorf("topology: container %s returned an invalid immutable id", cluster.NodeName)
	}
	if strings.TrimPrefix(container.Name, "/") != cluster.NodeName {
		return fmt.Errorf("topology: container name mismatch for %s", cluster.NodeName)
	}
	if container.Config.Labels[clusterLabelKey] != cluster.Name {
		return fmt.Errorf("topology: refusing unowned container %s", cluster.NodeName)
	}
	if container.Config.Image != NodeImage || !imageIDPattern.MatchString(container.Image) {
		return fmt.Errorf("topology: container %s has an invalid image identity", cluster.NodeName)
	}
	if !container.State.Running || container.State.Paused || container.State.Restarting || container.State.Dead {
		return fmt.Errorf("topology: container %s is not stably running", cluster.NodeName)
	}
	if !dockerIDPattern.MatchString(container.NetworkSettings.SandboxID) || !validRuntimeTime(container.State.StartedAt) {
		return fmt.Errorf("topology: container %s has an invalid running generation", cluster.NodeName)
	}
	return nil
}

func validRuntimeTime(value string) bool {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && parsed.Year() >= 2000
}

func validateTargetNetworkBase(instance string, cluster Cluster, network dockerNetwork) error {
	if !dockerIDPattern.MatchString(network.ID) || network.Name != cluster.NetworkName {
		return fmt.Errorf("topology: network %s has an invalid immutable identity", cluster.NetworkName)
	}
	if network.Driver != "bridge" || network.Scope != "local" {
		return fmt.Errorf("topology: network %s has unexpected driver or scope", cluster.NetworkName)
	}
	if network.Labels[ownerLabelKey] != TaskKey || network.Labels[instanceLabelKey] != instance {
		return fmt.Errorf("topology: refusing unowned network %s", cluster.NetworkName)
	}
	if len(network.IPAM.Config) != 1 || network.IPAM.Config[0].Subnet != cluster.DockerSubnet {
		return fmt.Errorf("topology: network %s has unexpected subnet", cluster.NetworkName)
	}
	return nil
}

func validateTargetEndpoint(
	container dockerContainer,
	network dockerNetwork,
	attachment struct {
		NetworkID   string `json:"NetworkID"`
		EndpointID  string `json:"EndpointID"`
		Gateway     string `json:"Gateway"`
		IPAddress   string `json:"IPAddress"`
		IPPrefixLen int    `json:"IPPrefixLen"`
		MacAddress  string `json:"MacAddress"`
	},
) (FaultTargetEndpoint, error) {
	if attachment.NetworkID != network.ID || !dockerIDPattern.MatchString(attachment.EndpointID) {
		return FaultTargetEndpoint{}, errors.New("topology: endpoint has an invalid immutable identity")
	}
	if net.ParseIP(attachment.IPAddress).To4() == nil || attachment.IPPrefixLen < 1 || attachment.IPPrefixLen > 32 {
		return FaultTargetEndpoint{}, errors.New("topology: endpoint has an invalid private ipv4 address")
	}
	address := fmt.Sprintf("%s/%d", attachment.IPAddress, attachment.IPPrefixLen)
	if !validPrivateNetworkAddress(address) || net.ParseIP(attachment.Gateway).To4() == nil {
		return FaultTargetEndpoint{}, errors.New("topology: endpoint is outside the expected private network")
	}
	membership, ok := network.Containers[container.ID]
	if !ok || membership.Name != strings.TrimPrefix(container.Name, "/") {
		return FaultTargetEndpoint{}, errors.New("topology: network membership does not match the container")
	}
	if membership.EndpointID != attachment.EndpointID || membership.MacAddress != attachment.MacAddress ||
		membership.IPv4Address != address {
		return FaultTargetEndpoint{}, errors.New("topology: endpoint inspection is inconsistent")
	}
	return FaultTargetEndpoint{
		ID:        attachment.EndpointID,
		NetworkID: attachment.NetworkID,
		Address:   address,
		Gateway:   attachment.Gateway,
		MAC:       attachment.MacAddress,
	}, nil
}

func validPrivateNetworkAddress(address string) bool {
	ip, _, err := net.ParseCIDR(address)
	return err == nil && ip.IsPrivate()
}

func addressWithinSubnet(address, subnet string) bool {
	ip, _, err := net.ParseCIDR(address)
	if err != nil {
		return false
	}
	return ipWithinSubnet(ip.String(), subnet)
}

func ipWithinSubnet(address, subnet string) bool {
	ip := net.ParseIP(address)
	_, network, err := net.ParseCIDR(subnet)
	return err == nil && ip != nil && network.Contains(ip)
}

func findTargetInterface(
	interfaces []interfaceInspection,
	endpoint FaultTargetEndpoint,
) (FaultTargetInterface, error) {
	expectedIP, expectedNetwork, err := net.ParseCIDR(endpoint.Address)
	if err != nil {
		return FaultTargetInterface{}, errors.New("topology: endpoint address is invalid")
	}
	ones, _ := expectedNetwork.Mask.Size()
	matches := make([]FaultTargetInterface, 0, 1)
	for _, candidate := range interfaces {
		if candidate.Index <= 0 || !interfaceNamePattern.MatchString(candidate.Name) || candidate.MAC != endpoint.MAC {
			continue
		}
		for _, address := range candidate.AddressInfo {
			if address.Family == "inet" && address.Local == expectedIP.String() && address.PrefixLen == ones {
				matches = append(matches, FaultTargetInterface{
					Name:    candidate.Name,
					Index:   candidate.Index,
					Address: endpoint.Address,
					MAC:     candidate.MAC,
				})
			}
		}
	}
	if len(matches) != 1 {
		return FaultTargetInterface{}, errors.New("topology: endpoint interface resolution is ambiguous")
	}
	return matches[0], nil
}

func (t *Topology) inspectInterfaces(ctx context.Context, containerID string) ([]interfaceInspection, error) {
	commandCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	result, err := t.runner.Run(commandCtx, t.dockerCommand(
		"exec",
		containerID,
		"ip",
		"-j",
		"-details",
		"address",
		"show",
	))
	if err != nil {
		return nil, fmt.Errorf("inspecting interfaces for container %s: %w", containerID, err)
	}
	interfaces := []interfaceInspection{}
	if err := json.Unmarshal([]byte(result.Stdout), &interfaces); err != nil || len(interfaces) == 0 {
		return nil, errors.New("topology: invalid interface inspection")
	}
	return interfaces, nil
}

func (t *Topology) inspectNetNS(ctx context.Context, containerID string) (string, error) {
	commandCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	result, err := t.runner.Run(commandCtx, t.dockerCommand(
		"exec",
		containerID,
		"readlink",
		"/proc/self/ns/net",
	))
	if err != nil {
		return "", fmt.Errorf("inspecting netns for container %s: %w", containerID, err)
	}
	identity := strings.TrimSpace(result.Stdout)
	if !netNSPattern.MatchString(identity) {
		return "", errors.New("topology: invalid network namespace identity")
	}
	return identity, nil
}

func (t *Topology) resolveKubernetesNode(
	ctx context.Context,
	cluster Cluster,
) (FaultTargetKubernetesNode, error) {
	result, err := t.runKubectl(
		ctx,
		commandTimeout,
		cluster,
		"get",
		"node",
		cluster.NodeName,
		"-o",
		"json",
	)
	if err != nil {
		return FaultTargetKubernetesNode{}, fmt.Errorf("reading kubernetes node %s: %w", cluster.NodeName, err)
	}
	object := kubernetesObject{}
	if err := json.Unmarshal([]byte(result.Stdout), &object); err != nil {
		return FaultTargetKubernetesNode{}, fmt.Errorf("decoding kubernetes node %s: %w", cluster.NodeName, err)
	}
	expectedLabels := targetKubernetesLabels(cluster, t.config.Instance)
	labels := make([]struct{ key, value string }, 0, len(expectedLabels))
	for key, value := range expectedLabels {
		labels = append(labels, struct{ key, value string }{key: key, value: value})
	}
	if err := validateKubernetesIdentity(object, cluster.NodeName, labels); err != nil {
		return FaultTargetKubernetesNode{}, err
	}
	if object.Metadata.UID == "" || len(object.Metadata.UID) > 128 {
		return FaultTargetKubernetesNode{}, errors.New("topology: kubernetes node uid is invalid")
	}
	return FaultTargetKubernetesNode{
		Name:   object.Metadata.Name,
		UID:    object.Metadata.UID,
		Labels: expectedLabels,
	}, nil
}

func targetKubernetesLabels(cluster Cluster, instance string) map[string]string {
	return map[string]string{
		"marketmesh.dev/cluster":           cluster.LogicalName,
		"marketmesh.dev/dc":                cluster.DC,
		"marketmesh.dev/owner-task":        TaskKey,
		"marketmesh.dev/topology-instance": instance,
		"marketmesh.dev/zone":              cluster.Zone,
	}
}

func (t *Topology) selectedClusters(selector TargetSelector) ([]Cluster, error) {
	if selector.DC == "" && selector.Zone != "" {
		return nil, errors.New("topology: target zone requires an exact dc")
	}
	if selector.DC != "" && selector.DC != "dc-a" && selector.DC != "dc-b" {
		return nil, fmt.Errorf("topology: unknown target dc %q", selector.DC)
	}
	if selector.Zone != "" && selector.Zone != "dmz" && selector.Zone != "internal" {
		return nil, fmt.Errorf("topology: unknown target zone %q", selector.Zone)
	}
	clusters := []Cluster{}
	for _, cluster := range t.config.Clusters() {
		if selector.DC != "" && cluster.DC != selector.DC {
			continue
		}
		if selector.Zone != "" && cluster.Zone != selector.Zone {
			continue
		}
		clusters = append(clusters, cluster)
	}
	if len(clusters) == 0 {
		return nil, errors.New("topology: target selector matched no clusters")
	}
	return clusters, nil
}

func (t *Topology) expectedNetworkClusters(cluster Cluster) ([]Cluster, error) {
	networks := []Cluster{cluster}
	if cluster.Zone == "internal" {
		dmz, err := t.config.Cluster(cluster.DC, "dmz")
		if err != nil {
			return nil, err
		}
		networks = append(networks, dmz)
	}
	return networks, nil
}

func validateConsumer(task, runID string) error {
	if !consumerTaskPattern.MatchString(task) {
		return errors.New("topology: consumer task must be an MM task key")
	}
	if !consumerRunIDPattern.MatchString(runID) {
		return errors.New("topology: consumer run id must be 3-63 lowercase letters, digits, or hyphens")
	}
	prefix := strings.ToLower(strings.ReplaceAll(task, "-", "")) + "-"
	if !strings.HasPrefix(runID, prefix) {
		return errors.New("topology: consumer run id is not bound to the consumer task")
	}
	return nil
}

func targetSnapshotToken(snapshot TargetSnapshot) (string, error) {
	snapshot.Token = ""
	return canonicalSHA256(snapshot, "target snapshot token")
}

func targetValidationReceiptDigest(receipt TargetValidationReceipt) (string, error) {
	receipt.ReceiptDigest = ""
	return canonicalSHA256(receipt, "target validation receipt")
}

func targetRebindTransitionDigest(transition TargetRebindTransition) (string, error) {
	transition.TransitionDigest = ""
	return canonicalSHA256(transition, "target rebind transition")
}

func canonicalSHA256(value any, description string) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("encoding %s: %w", description, err)
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func (t *Topology) validateSnapshot(snapshot TargetSnapshot) error {
	if snapshot.APIVersion != TargetAPIVersion || snapshot.Task != TaskKey ||
		snapshot.Environment != TargetEnvironment || snapshot.Instance != t.config.Instance ||
		snapshot.DockerContext != t.config.DockerContext {
		return errors.New("topology: target snapshot metadata mismatch")
	}
	if err := validateConsumer(snapshot.ConsumerTask, snapshot.ConsumerRunID); err != nil {
		return err
	}
	if _, err := time.Parse(time.RFC3339Nano, snapshot.ResolvedAt); err != nil {
		return errors.New("topology: target snapshot resolved_at is invalid")
	}
	expectedClusters, err := t.selectedClusters(snapshot.Selector)
	if err != nil {
		return err
	}
	if len(snapshot.Targets) != len(expectedClusters) {
		return errors.New("topology: target snapshot cardinality mismatch")
	}
	if snapshot.PreviousToken != "" && !imageIDPattern.MatchString(snapshot.PreviousToken) {
		return errors.New("topology: target snapshot previous token is invalid")
	}
	for index, cluster := range expectedClusters {
		if err := t.validateTargetShape(snapshot.Targets[index], cluster); err != nil {
			return err
		}
	}
	expectedToken, err := targetSnapshotToken(snapshot)
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare([]byte(snapshot.Token), []byte(expectedToken)) != 1 {
		return errors.New("topology: target snapshot token mismatch")
	}
	return nil
}

func (t *Topology) validateTargetShape(target FaultTarget, cluster Cluster) error {
	if target.LogicalCluster != cluster.LogicalName || target.ResourceCluster != cluster.Name ||
		target.DC != cluster.DC || target.Zone != cluster.Zone || target.Kubeconfig != cluster.Kubeconfig ||
		target.KubeContext != cluster.KubeContext || target.Namespace != Namespace {
		return fmt.Errorf("topology: target %s does not match configured cluster identity", cluster.LogicalName)
	}
	if target.Container.Name != cluster.NodeName || target.Container.ImageReference != NodeImage ||
		!dockerIDPattern.MatchString(target.Container.ID) || !imageIDPattern.MatchString(target.Container.ImageID) ||
		!validRuntimeTime(target.Container.StartedAt) ||
		!exactLabels(target.Container.Labels, map[string]string{clusterLabelKey: cluster.Name}) {
		return fmt.Errorf("topology: target %s has invalid container identity", cluster.LogicalName)
	}
	expectedNodeLabels := targetKubernetesLabels(cluster, t.config.Instance)
	if target.KubernetesNode.Name != cluster.NodeName || target.KubernetesNode.UID == "" ||
		len(target.KubernetesNode.UID) > 128 || !exactLabels(target.KubernetesNode.Labels, expectedNodeLabels) {
		return fmt.Errorf("topology: target %s has invalid kubernetes identity", cluster.LogicalName)
	}
	if !dockerIDPattern.MatchString(target.SandboxID) || !netNSPattern.MatchString(target.NetNS) {
		return fmt.Errorf("topology: target %s has invalid netns identity", cluster.LogicalName)
	}
	expectedNetworks, err := t.expectedNetworkClusters(cluster)
	if err != nil {
		return err
	}
	if len(target.Networks) != len(expectedNetworks) {
		return fmt.Errorf("topology: target %s has invalid network cardinality", cluster.LogicalName)
	}
	slices.SortFunc(expectedNetworks, func(left, right Cluster) int {
		return strings.Compare(left.LogicalName, right.LogicalName)
	})
	for index, networkCluster := range expectedNetworks {
		if err := validateTargetNetworkShape(
			target.Networks[index],
			networkCluster,
			networkCluster.LogicalName == cluster.LogicalName,
			t.config.Instance,
		); err != nil {
			return err
		}
	}
	return nil
}

func validateTargetNetworkShape(
	target FaultTargetNetwork,
	cluster Cluster,
	isPrimary bool,
	instance string,
) error {
	labels := map[string]string{ownerLabelKey: TaskKey, instanceLabelKey: instance}
	if target.LogicalNetwork != cluster.LogicalName || target.Primary != isPrimary ||
		target.Name != cluster.NetworkName || target.Driver != "bridge" || target.Scope != "local" ||
		target.Subnet != cluster.DockerSubnet || !dockerIDPattern.MatchString(target.ID) ||
		!exactLabels(target.Labels, labels) {
		return fmt.Errorf("topology: network target %s has invalid identity", cluster.LogicalName)
	}
	if target.Endpoint.NetworkID != target.ID || !dockerIDPattern.MatchString(target.Endpoint.ID) ||
		!validPrivateNetworkAddress(target.Endpoint.Address) || net.ParseIP(target.Endpoint.Gateway).To4() == nil ||
		target.Endpoint.MAC == "" || !addressWithinSubnet(target.Endpoint.Address, target.Subnet) ||
		!ipWithinSubnet(target.Endpoint.Gateway, target.Subnet) {
		return fmt.Errorf("topology: network target %s has invalid endpoint identity", cluster.LogicalName)
	}
	if !interfaceNamePattern.MatchString(target.Interface.Name) || target.Interface.Index <= 0 ||
		target.Interface.Address != target.Endpoint.Address || target.Interface.MAC != target.Endpoint.MAC {
		return fmt.Errorf("topology: network target %s has invalid interface identity", cluster.LogicalName)
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

func selectSnapshotTargets(targets []FaultTarget, names []string) ([]FaultTarget, error) {
	if len(names) == 0 {
		return slices.Clone(targets), nil
	}
	requested := make(map[string]struct{}, len(names))
	for _, name := range names {
		if _, exists := requested[name]; exists {
			return nil, fmt.Errorf("topology: duplicate target %q", name)
		}
		requested[name] = struct{}{}
	}
	selected := make([]FaultTarget, 0, len(requested))
	for _, target := range targets {
		if _, ok := requested[target.LogicalCluster]; ok {
			selected = append(selected, target)
			delete(requested, target.LogicalCluster)
		}
	}
	if len(requested) != 0 {
		return nil, errors.New("topology: requested target is absent from the snapshot")
	}
	return selected, nil
}

func (t *Topology) validateTargetRuntime(
	ctx context.Context,
	target FaultTarget,
	expectedState ExpectedTargetState,
) (observedTargetState, error) {
	cluster, err := t.config.Cluster(target.DC, target.Zone)
	if err != nil {
		return observedTargetState{}, err
	}
	container, err := t.inspectContainer(ctx, target.Container.ID)
	if err != nil {
		return observedTargetState{}, err
	}
	if container.ID != target.Container.ID || strings.TrimPrefix(container.Name, "/") != target.Container.Name ||
		container.Image != target.Container.ImageID || container.Config.Image != target.Container.ImageReference ||
		container.Config.Labels[clusterLabelKey] != target.Container.Labels[clusterLabelKey] ||
		container.State.StartedAt != target.Container.StartedAt {
		return observedTargetState{}, errors.New("topology: immutable container identity or generation changed")
	}
	if err := validateObservedState(container, expectedState); err != nil {
		return observedTargetState{}, err
	}
	for _, targetNetwork := range target.Networks {
		networkCluster, networkErr := findNetworkCluster(t.config.Clusters(), targetNetwork.LogicalNetwork)
		if networkErr != nil {
			return observedTargetState{}, networkErr
		}
		network, networkErr := t.inspectNetwork(ctx, targetNetwork.ID)
		if networkErr != nil {
			return observedTargetState{}, networkErr
		}
		if network.ID != targetNetwork.ID || network.Name != targetNetwork.Name {
			return observedTargetState{}, errors.New("topology: immutable network identity changed")
		}
		if networkErr := validateTargetNetworkBase(t.config.Instance, networkCluster, network); networkErr != nil {
			return observedTargetState{}, networkErr
		}
		if expectedState == ExpectedStateRunning {
			if networkErr := validateRunningAttachment(container, network, targetNetwork); networkErr != nil {
				return observedTargetState{}, networkErr
			}
		} else if networkErr := validateStoppedAttachment(container, network, targetNetwork); networkErr != nil {
			return observedTargetState{}, networkErr
		}
	}
	if expectedState == ExpectedStateRunning {
		if len(container.NetworkSettings.Networks) != len(target.Networks) {
			return observedTargetState{}, errors.New("topology: running container network attachments changed")
		}
		if container.NetworkSettings.SandboxID != target.SandboxID {
			return observedTargetState{}, errors.New("topology: docker sandbox identity changed")
		}
		netNS, netNSErr := t.inspectNetNS(ctx, target.Container.ID)
		if netNSErr != nil {
			return observedTargetState{}, netNSErr
		}
		if netNS != target.NetNS {
			return observedTargetState{}, errors.New("topology: network namespace identity changed")
		}
		interfaces, interfaceErr := t.inspectInterfaces(ctx, target.Container.ID)
		if interfaceErr != nil {
			return observedTargetState{}, interfaceErr
		}
		for _, targetNetwork := range target.Networks {
			interfaceIdentity, findErr := findTargetInterface(interfaces, targetNetwork.Endpoint)
			if findErr != nil {
				return observedTargetState{}, findErr
			}
			if interfaceIdentity != targetNetwork.Interface {
				return observedTargetState{}, errors.New("topology: network interface identity changed")
			}
		}
		node, nodeErr := t.resolveKubernetesNode(ctx, cluster)
		if nodeErr != nil {
			return observedTargetState{}, nodeErr
		}
		if node.UID != target.KubernetesNode.UID || !exactLabels(node.Labels, target.KubernetesNode.Labels) {
			return observedTargetState{}, errors.New("topology: kubernetes node identity changed")
		}
	} else if container.NetworkSettings.SandboxID != "" {
		return observedTargetState{}, errors.New("topology: stopped container retains a live sandbox")
	}
	return observedTargetState{
		status:     container.State.Status,
		startedAt:  container.State.StartedAt,
		finishedAt: container.State.FinishedAt,
	}, nil
}

func validateObservedState(container dockerContainer, expectedState ExpectedTargetState) error {
	switch expectedState {
	case ExpectedStateRunning:
		if !container.State.Running || container.State.Status != "running" || container.State.Paused ||
			container.State.Restarting || container.State.Dead {
			return errors.New("topology: container is not stably running")
		}
		if !validRuntimeTime(container.State.StartedAt) {
			return errors.New("topology: running container started_at is invalid")
		}
	case ExpectedStateStopped:
		if container.State.Running || container.State.Status != "exited" || container.State.Paused ||
			container.State.Restarting || container.State.Dead {
			return errors.New("topology: container is not safely stopped")
		}
		if !validRuntimeTime(container.State.StartedAt) || !validRuntimeTime(container.State.FinishedAt) {
			return errors.New("topology: stopped container transition times are invalid")
		}
	default:
		return errors.New("topology: unsupported expected container state")
	}
	return nil
}

func validateRunningAttachment(
	container dockerContainer,
	network dockerNetwork,
	target FaultTargetNetwork,
) error {
	attachment, ok := container.NetworkSettings.Networks[target.Name]
	if !ok {
		return errors.New("topology: running network attachment is missing")
	}
	endpoint, err := validateTargetEndpoint(container, network, attachment)
	if err != nil {
		return err
	}
	if endpoint != target.Endpoint {
		return errors.New("topology: endpoint identity changed")
	}
	return nil
}

func validateStoppedAttachment(
	container dockerContainer,
	network dockerNetwork,
	target FaultTargetNetwork,
) error {
	attachment, ok := container.NetworkSettings.Networks[target.Name]
	if !ok || attachment.NetworkID != target.ID {
		return errors.New("topology: stopped container network identity changed")
	}
	hasLiveEndpoint := attachment.EndpointID != "" || attachment.IPAddress != "" ||
		attachment.MacAddress != "" || attachment.IPPrefixLen != 0
	if hasLiveEndpoint {
		return errors.New("topology: stopped container retains a live endpoint")
	}
	if _, exists := network.Containers[container.ID]; exists {
		return errors.New("topology: stopped container remains a live network member")
	}
	return nil
}

func findNetworkCluster(clusters []Cluster, logicalName string) (Cluster, error) {
	for _, cluster := range clusters {
		if cluster.LogicalName == logicalName {
			return cluster, nil
		}
	}
	return Cluster{}, fmt.Errorf("topology: unknown logical network %q", logicalName)
}
