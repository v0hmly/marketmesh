package networkchaos

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

const (
	topologyTargetAPIVersion     = "marketmesh.dev/e2e-topology/targets/v1"
	topologyValidationAPIVersion = "marketmesh.dev/e2e-topology/target-validation/v1"
	topologyTaskKey              = "MM-28"
	topologyEnvironment          = "local-e2e-disposable"
	topologyExpectedStateRunning = "running"
	topologyKindClusterLabel     = "io.x-k8s.kind.cluster"
	topologyOwnerLabel           = "com.marketmesh.task"
	topologyInstanceLabel        = "com.marketmesh.topology"
	topologyNamespace            = "marketmesh-system"
	topologyNodeImage            = "kindest/node:v1.37.0@sha256:a1ed56cfb0e7b93589bdf97c8cd566405a265939e3620fc4f5de89adff580ae5"
	maxTopologyOutputBytes       = 2 << 20
)

var (
	topologyInstancePattern = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,18}[a-z0-9])?$`)
	dockerContextPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,62}$`)
	topologyDockerIDPattern = regexp.MustCompile(`^[a-f0-9]{64}$`)
	dockerImageIDPattern    = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
	topologyDigestPattern   = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
	topologyNetNSPattern    = regexp.MustCompile(`^net:\[[0-9]+\]$`)
)

// TopologyCLIConfig binds the read-only MM-38 CLI to one MM-36 consumer run.
type TopologyCLIConfig struct {
	Executable    string
	Instance      string
	DockerContext string
	DockerHost    string
	RunID         string
}

// TopologyTargetSnapshot is the public MM-38 handoff. It is intentionally a
// data-only mirror of the versioned CLI schema; resolution logic remains in
// e2e-topology.
type TopologyTargetSnapshot struct {
	APIVersion    string                 `json:"api_version"`
	Task          string                 `json:"task"`
	Environment   string                 `json:"environment"`
	Instance      string                 `json:"instance"`
	DockerContext string                 `json:"docker_context"`
	ConsumerTask  string                 `json:"consumer_task"`
	ConsumerRunID string                 `json:"consumer_run_id"`
	ResolvedAt    string                 `json:"resolved_at"`
	Selector      TopologyTargetSelector `json:"selector"`
	Targets       []TopologyFaultTarget  `json:"targets"`
	PreviousToken string                 `json:"previous_token,omitempty"`
	Token         string                 `json:"token"`
}

// TopologyTargetSelector mirrors the optional MM-38 target selector.
type TopologyTargetSelector struct {
	DC   string `json:"dc,omitempty"`
	Zone string `json:"zone,omitempty"`
}

// TopologyFaultTarget identifies one exact kind node and its attachments.
type TopologyFaultTarget struct {
	LogicalCluster  string                       `json:"logical_cluster"`
	ResourceCluster string                       `json:"resource_cluster"`
	DC              string                       `json:"dc"`
	Zone            string                       `json:"zone"`
	Kubeconfig      string                       `json:"kubeconfig"`
	KubeContext     string                       `json:"kube_context"`
	Namespace       string                       `json:"namespace"`
	Container       TopologyFaultTargetContainer `json:"container"`
	KubernetesNode  TopologyKubernetesNode       `json:"kubernetes_node"`
	SandboxID       string                       `json:"sandbox_id"`
	NetNS           string                       `json:"netns"`
	Networks        []TopologyFaultTargetNetwork `json:"networks"`
}

// TopologyFaultTargetContainer contains immutable Docker identity.
type TopologyFaultTargetContainer struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	ImageID        string            `json:"image_id"`
	ImageReference string            `json:"image_reference"`
	StartedAt      string            `json:"started_at"`
	Labels         map[string]string `json:"labels"`
}

// TopologyKubernetesNode contains the exact Kubernetes node identity.
type TopologyKubernetesNode struct {
	Name   string            `json:"name"`
	UID    string            `json:"uid"`
	Labels map[string]string `json:"labels"`
}

// TopologyFaultTargetNetwork binds one Docker network and in-netns interface.
type TopologyFaultTargetNetwork struct {
	LogicalNetwork string                  `json:"logical_network"`
	Primary        bool                    `json:"primary"`
	ID             string                  `json:"id"`
	Name           string                  `json:"name"`
	Driver         string                  `json:"driver"`
	Scope          string                  `json:"scope"`
	Subnet         string                  `json:"subnet"`
	Labels         map[string]string       `json:"labels"`
	Endpoint       TopologyTargetEndpoint  `json:"endpoint"`
	Interface      TopologyTargetInterface `json:"interface"`
}

// TopologyTargetEndpoint is the cross-inspected Docker endpoint identity.
type TopologyTargetEndpoint struct {
	ID        string `json:"id"`
	NetworkID string `json:"network_id"`
	Address   string `json:"address"`
	Gateway   string `json:"gateway"`
	MAC       string `json:"mac"`
}

// TopologyTargetInterface identifies the exact container interface.
type TopologyTargetInterface struct {
	Name    string `json:"name"`
	Index   int    `json:"index"`
	Address string `json:"address"`
	MAC     string `json:"mac"`
}

// TopologyValidationReceipt proves a fresh validation of the original token.
type TopologyValidationReceipt struct {
	APIVersion    string                    `json:"api_version"`
	SnapshotToken string                    `json:"snapshot_token"`
	ExpectedState string                    `json:"expected_state"`
	Validated     bool                      `json:"validated"`
	ValidatedAt   string                    `json:"validated_at"`
	Targets       []TopologyValidatedTarget `json:"targets"`
	ReceiptDigest string                    `json:"receipt_digest"`
}

// TopologyValidatedTarget is the bounded target summary in a receipt.
type TopologyValidatedTarget struct {
	LogicalCluster string   `json:"logical_cluster"`
	ContainerID    string   `json:"container_id"`
	State          string   `json:"state"`
	StartedAt      string   `json:"started_at"`
	FinishedAt     string   `json:"finished_at"`
	NetworkIDs     []string `json:"network_ids"`
}

// TopologyFaultSpec selects public target identities and structured fault
// parameters without accepting Docker names, IDs, interfaces, or CIDRs.
type TopologyFaultSpec struct {
	Name           string
	Kind           Kind
	LogicalCluster string
	LogicalNetwork string
	PeerNetworks   []string
	Delay          time.Duration
	Jitter         time.Duration
	LossPercent    float64
	BandwidthKbit  uint32
	CapacityLoss   uint
}

type topologyCommandRunner interface {
	Run(ctx context.Context, stdin []byte, arguments ...string) ([]byte, error)
}

type topologyRunningValidator interface {
	ValidateRunning(
		ctx context.Context,
		snapshot TopologyTargetSnapshot,
		logicalNames []string,
	) (TopologyValidationReceipt, error)
}

type topologyDockerContextValidator interface {
	Validate(ctx context.Context, dockerContext string, dockerHost string) error
}

// TopologyTargetClient executes the public MM-38 resolve/validate CLI with
// fixed argv, bounded IO, and no shell.
type TopologyTargetClient struct {
	config         TopologyCLIConfig
	commands       topologyCommandRunner
	dockerContexts topologyDockerContextValidator
}

// NewTopologyTargetClient validates the exact local executable and consumer
// binding before any subprocess can run.
func NewTopologyTargetClient(config TopologyCLIConfig) (*TopologyTargetClient, error) {
	if err := validateTopologyCLIConfig(config); err != nil {
		return nil, err
	}

	return newTopologyTargetClient(
		config,
		execTopologyCommandRunner{
			executable:     config.Executable,
			maxOutputBytes: maxTopologyOutputBytes,
		},
		execTopologyDockerContextValidator{
			commands: execDockerCommandRunner{maxOutputBytes: maxDockerOutputBytes},
		},
	), nil
}

func newTopologyTargetClient(
	config TopologyCLIConfig,
	commands topologyCommandRunner,
	dockerContexts topologyDockerContextValidator,
) *TopologyTargetClient {
	return &TopologyTargetClient{
		config:         config,
		commands:       commands,
		dockerContexts: dockerContexts,
	}
}

// Resolve obtains all four targets once for the exact MM-36 run.
func (client *TopologyTargetClient) Resolve(ctx context.Context) (TopologyTargetSnapshot, error) {
	if err := requireBoundedTopologyContext(ctx); err != nil {
		return TopologyTargetSnapshot{}, err
	}
	if err := client.validateDockerContext(ctx); err != nil {
		return TopologyTargetSnapshot{}, err
	}
	output, err := client.commands.Run(
		ctx,
		nil,
		"--instance",
		client.config.Instance,
		"--docker-context",
		client.config.DockerContext,
		"targets",
		"resolve",
		"--consumer-task",
		TaskKey,
		"--consumer-run-id",
		client.config.RunID,
	)
	contextErr := client.validateDockerContext(ctx)
	if err != nil || contextErr != nil {
		return TopologyTargetSnapshot{}, errors.Join(
			wrapTopologyError("resolving topology targets", err),
			contextErr,
		)
	}

	snapshot := TopologyTargetSnapshot{}
	if err := decodeTopologyDocument(output, &snapshot); err != nil {
		return TopologyTargetSnapshot{}, fmt.Errorf("networkchaos: decoding topology targets: %w", err)
	}
	if err := validateTopologySnapshot(client.config, snapshot); err != nil {
		return TopologyTargetSnapshot{}, err
	}

	return cloneTopologySnapshot(snapshot), nil
}

// ValidateRunning re-inspects only immutable IDs from the supplied original
// snapshot and verifies the returned receipt before it is trusted.
func (client *TopologyTargetClient) ValidateRunning(
	ctx context.Context,
	snapshot TopologyTargetSnapshot,
	logicalNames []string,
) (TopologyValidationReceipt, error) {
	if err := requireBoundedTopologyContext(ctx); err != nil {
		return TopologyValidationReceipt{}, err
	}
	if err := client.validateDockerContext(ctx); err != nil {
		return TopologyValidationReceipt{}, err
	}
	if err := validateTopologySnapshot(client.config, snapshot); err != nil {
		return TopologyValidationReceipt{}, err
	}
	selected, err := selectTopologyTargets(snapshot, logicalNames)
	if err != nil {
		return TopologyValidationReceipt{}, err
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return TopologyValidationReceipt{}, fmt.Errorf("networkchaos: encoding topology target snapshot: %w", err)
	}
	if len(encoded) > maxTopologyOutputBytes {
		return TopologyValidationReceipt{}, errors.New("networkchaos: topology target snapshot exceeds input limit")
	}

	arguments := []string{
		"--instance",
		client.config.Instance,
		"--docker-context",
		client.config.DockerContext,
		"targets",
		"validate",
		"--snapshot",
		"-",
		"--expected-state",
		topologyExpectedStateRunning,
	}
	for _, target := range selected {
		arguments = append(arguments, "--target", target.LogicalCluster)
	}
	output, err := client.commands.Run(ctx, encoded, arguments...)
	contextErr := client.validateDockerContext(ctx)
	if err != nil || contextErr != nil {
		return TopologyValidationReceipt{}, errors.Join(
			wrapTopologyError("validating topology targets", err),
			contextErr,
		)
	}

	receipt := TopologyValidationReceipt{}
	if err := decodeTopologyDocument(output, &receipt); err != nil {
		return TopologyValidationReceipt{}, fmt.Errorf("networkchaos: decoding topology validation receipt: %w", err)
	}
	if err := validateTopologyReceipt(snapshot, selected, receipt); err != nil {
		return TopologyValidationReceipt{}, err
	}

	return cloneTopologyReceipt(receipt), nil
}

func (client *TopologyTargetClient) validateDockerContext(ctx context.Context) error {
	if client.dockerContexts == nil {
		return errors.New("networkchaos: Docker context validator must not be nil")
	}
	if err := client.dockerContexts.Validate(
		ctx,
		client.config.DockerContext,
		client.config.DockerHost,
	); err != nil {
		return fmt.Errorf("networkchaos: validating pinned Docker context: %w", err)
	}
	return nil
}

func wrapTopologyError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("networkchaos: %s: %w", operation, err)
}

// TopologyDriver applies the existing structured Docker mutations only after
// the public MM-38 validator proves the exact running binding. It never resolves
// a replacement by name.
type TopologyDriver struct {
	snapshot  TopologyTargetSnapshot
	validator topologyRunningValidator
	docker    *DockerDriver
}

// NewTopologyDriver binds one immutable snapshot to a Docker context.
func NewTopologyDriver(
	client *TopologyTargetClient,
	snapshot TopologyTargetSnapshot,
) (*TopologyDriver, error) {
	if client == nil {
		return nil, errors.New("networkchaos: topology target client must not be nil")
	}
	if err := validateTopologySnapshot(client.config, snapshot); err != nil {
		return nil, err
	}

	return newTopologyDriver(
		client,
		snapshot,
		execDockerCommandRunner{
			maxOutputBytes: maxDockerOutputBytes,
			dockerHost:     client.config.DockerHost,
		},
	), nil
}

func newTopologyDriver(
	validator topologyRunningValidator,
	snapshot TopologyTargetSnapshot,
	commands dockerCommandRunner,
) *TopologyDriver {
	return &TopologyDriver{
		snapshot:  cloneTopologySnapshot(snapshot),
		validator: validator,
		docker:    &DockerDriver{commands: commands},
	}
}

// Fault derives every destructive selector from the immutable MM-38 snapshot.
func (driver *TopologyDriver) Fault(spec TopologyFaultSpec) (Fault, error) {
	target, err := topologyTargetByName(driver.snapshot, spec.LogicalCluster)
	if err != nil {
		return Fault{}, err
	}
	network, err := topologyNetworkOnTarget(target, spec.LogicalNetwork)
	if err != nil {
		return Fault{}, err
	}
	peers := make([]ResourceRef, 0, len(spec.PeerNetworks))
	seenPeers := make(map[string]struct{}, len(spec.PeerNetworks))
	for _, logicalName := range spec.PeerNetworks {
		if _, found := seenPeers[logicalName]; found {
			return Fault{}, fmt.Errorf("networkchaos: topology peer network %q is duplicated", logicalName)
		}
		seenPeers[logicalName] = struct{}{}
		peer, peerErr := topologyNetworkByName(driver.snapshot, logicalName)
		if peerErr != nil {
			return Fault{}, peerErr
		}
		peers = append(peers, topologyNetworkRef(peer))
	}

	fault := Fault{
		Name:          spec.Name,
		Kind:          spec.Kind,
		Container:     topologyContainerRef(target),
		Network:       topologyNetworkRef(network),
		PeerNetworks:  peers,
		Interface:     network.Interface.Name,
		Delay:         spec.Delay,
		Jitter:        spec.Jitter,
		LossPercent:   spec.LossPercent,
		BandwidthKbit: spec.BandwidthKbit,
		CapacityLoss:  spec.CapacityLoss,
	}
	if err := validateFault(fault); err != nil {
		return Fault{}, fmt.Errorf("networkchaos: invalid topology fault: %w", err)
	}

	return fault, nil
}

// Inspect validates the original target token and returns only data already
// present in that snapshot.
func (driver *TopologyDriver) Inspect(ctx context.Context, fault Fault) (Snapshot, error) {
	if ctx == nil {
		return Snapshot{}, errors.New("networkchaos: context must not be nil")
	}
	snapshot, logicalNames, err := driver.snapshotForFault(fault)
	if err != nil {
		return Snapshot{}, err
	}
	if _, err := driver.validator.ValidateRunning(ctx, driver.snapshot, logicalNames); err != nil {
		return Snapshot{}, fmt.Errorf("networkchaos: validating topology target before inspect: %w", err)
	}

	return cloneSnapshot(snapshot), nil
}

// Apply validates again immediately before mutation and wraps cleanup with the
// same original-token validation.
func (driver *TopologyDriver) Apply(
	ctx context.Context,
	snapshot Snapshot,
	fault Fault,
) (RestoreFunc, error) {
	if ctx == nil {
		return nil, errors.New("networkchaos: context must not be nil")
	}
	expected, logicalNames, err := driver.snapshotForFault(fault)
	if err != nil {
		return nil, err
	}
	if err := driver.validateSnapshotScope(driver.snapshot.ConsumerRunID, fault, snapshot); err != nil {
		return nil, err
	}
	if !topologySnapshotsEqual(expected, snapshot) {
		return nil, errors.New("networkchaos: topology snapshot differs from original target binding")
	}
	if _, err := driver.validator.ValidateRunning(ctx, driver.snapshot, logicalNames); err != nil {
		return nil, fmt.Errorf("networkchaos: validating topology target before mutation: %w", err)
	}

	var restore RestoreFunc
	switch fault.Kind {
	case KindPartition:
		restore, err = driver.docker.applyPartition(
			ctx,
			driver.snapshot.ConsumerRunID,
			snapshot,
			fault,
		)
	case KindDegradation:
		restore, err = driver.docker.applyDegradation(ctx, fault)
	default:
		return nil, fmt.Errorf("networkchaos: unsupported topology fault kind %q", fault.Kind)
	}
	if restore == nil {
		return nil, err
	}

	validatedRestore := func(restoreCtx context.Context) error {
		if restoreCtx == nil {
			return errors.New("networkchaos: cleanup context must not be nil")
		}
		if _, validateErr := driver.validator.ValidateRunning(
			restoreCtx,
			driver.snapshot,
			logicalNames,
		); validateErr != nil {
			return fmt.Errorf("networkchaos: validating topology target before cleanup: %w", validateErr)
		}
		return restore(restoreCtx)
	}

	return validatedRestore, err
}

func (driver *TopologyDriver) validateSnapshotScope(
	runID string,
	fault Fault,
	snapshot Snapshot,
) error {
	if runID != driver.snapshot.ConsumerRunID {
		return errors.New("networkchaos: topology run differs from original consumer binding")
	}
	expected, _, err := driver.snapshotForFault(fault)
	if err != nil {
		return err
	}
	if !topologySnapshotsEqual(expected, snapshot) {
		return errors.New("networkchaos: topology snapshot differs from original target binding")
	}
	return nil
}

func (driver *TopologyDriver) snapshotForFault(
	fault Fault,
) (Snapshot, []string, error) {
	if err := validateFault(fault); err != nil {
		return Snapshot{}, nil, fmt.Errorf("networkchaos: invalid topology fault: %w", err)
	}
	target, err := topologyTargetByContainer(driver.snapshot, fault.Container)
	if err != nil {
		return Snapshot{}, nil, err
	}
	network, err := topologyNetworkOnTargetRef(target, fault.Network)
	if err != nil {
		return Snapshot{}, nil, err
	}
	if fault.Interface != network.Interface.Name {
		return Snapshot{}, nil, errors.New("networkchaos: fault interface differs from topology target")
	}

	peerNetworks := make([]Network, 0, len(fault.PeerNetworks))
	logicalNames := []string{target.LogicalCluster}
	for _, peerRef := range fault.PeerNetworks {
		peer, owner, peerErr := topologyNetworkByRef(driver.snapshot, peerRef)
		if peerErr != nil {
			return Snapshot{}, nil, peerErr
		}
		peerNetwork, convertErr := topologyNetworkSnapshot(peer)
		if convertErr != nil {
			return Snapshot{}, nil, convertErr
		}
		peerNetworks = append(peerNetworks, peerNetwork)
		logicalNames = append(logicalNames, owner)
	}
	logicalNames = uniqueStrings(logicalNames)

	primary, err := topologyNetworkSnapshot(network)
	if err != nil {
		return Snapshot{}, nil, err
	}
	return Snapshot{
		Container: Resource{
			ID:     target.Container.ID,
			Name:   target.Container.Name,
			Labels: maps.Clone(target.Container.Labels),
		},
		Network:      primary,
		PeerNetworks: peerNetworks,
		Interface:    network.Interface.Name,
	}, logicalNames, nil
}

func validateTopologyCLIConfig(config TopologyCLIConfig) error {
	if !filepath.IsAbs(config.Executable) || filepath.Clean(config.Executable) != config.Executable {
		return errors.New("networkchaos: topology executable must be an exact absolute path")
	}
	info, err := os.Lstat(config.Executable)
	if err != nil {
		return fmt.Errorf("networkchaos: inspecting topology executable: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return errors.New("networkchaos: topology executable must be a regular executable file")
	}
	if !topologyInstancePattern.MatchString(config.Instance) {
		return errors.New("networkchaos: topology instance is invalid")
	}
	if !dockerContextPattern.MatchString(config.DockerContext) {
		return errors.New("networkchaos: topology Docker context is invalid")
	}
	if err := validateTopologyDockerHost(config.DockerHost); err != nil {
		return err
	}
	if !runIDPattern.MatchString(config.RunID) {
		return errors.New("networkchaos: topology consumer run id is invalid")
	}

	return nil
}

func validateTopologyDockerHost(host string) error {
	parsed, err := url.Parse(host)
	if err != nil || parsed.Scheme != "unix" || parsed.Host != "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" || parsed.RawPath != "" ||
		!filepath.IsAbs(parsed.Path) || filepath.Clean(parsed.Path) != parsed.Path || parsed.Path == "/" ||
		strings.ContainsAny(parsed.Path, "\x00\r\n") {
		return errors.New("networkchaos: topology Docker host must be an exact local Unix endpoint")
	}
	return nil
}

func requireBoundedTopologyContext(ctx context.Context) error {
	if ctx == nil {
		return errors.New("networkchaos: topology context must not be nil")
	}
	if _, ok := ctx.Deadline(); !ok {
		return errors.New("networkchaos: topology context must have a deadline")
	}
	return nil
}

func validateTopologySnapshot(
	config TopologyCLIConfig,
	snapshot TopologyTargetSnapshot,
) error {
	metadataMatches := snapshot.APIVersion == topologyTargetAPIVersion &&
		snapshot.Task == topologyTaskKey &&
		snapshot.Environment == topologyEnvironment &&
		snapshot.Instance == config.Instance &&
		snapshot.DockerContext == config.DockerContext &&
		snapshot.ConsumerTask == TaskKey &&
		snapshot.ConsumerRunID == config.RunID
	if !metadataMatches {
		return errors.New("networkchaos: topology target snapshot metadata mismatch")
	}
	if snapshot.Selector != (TopologyTargetSelector{}) || snapshot.PreviousToken != "" {
		return errors.New("networkchaos: MM-36 requires one initial unfiltered topology snapshot")
	}
	if !validTopologyRuntimeTime(snapshot.ResolvedAt) {
		return errors.New("networkchaos: topology target snapshot resolved_at is invalid")
	}
	expectedNames := []string{"dc-a-dmz", "dc-a-internal", "dc-b-dmz", "dc-b-internal"}
	if len(snapshot.Targets) != len(expectedNames) {
		return errors.New("networkchaos: topology target snapshot must contain exactly four targets")
	}
	for index, target := range snapshot.Targets {
		if err := validateTopologyTarget(snapshot.Instance, expectedNames[index], target); err != nil {
			return err
		}
	}
	if !topologyDigestPattern.MatchString(snapshot.Token) {
		return errors.New("networkchaos: topology target snapshot token is invalid")
	}
	expectedToken, err := topologySnapshotDigest(snapshot)
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare([]byte(snapshot.Token), []byte(expectedToken)) != 1 {
		return errors.New("networkchaos: topology target snapshot token mismatch")
	}

	return nil
}

func validateTopologyTarget(
	instance string,
	expectedName string,
	target TopologyFaultTarget,
) error {
	parts := strings.Split(expectedName, "-")
	expectedDC := strings.Join(parts[:2], "-")
	expectedZone := parts[2]
	expectedResource := instance + "-" + expectedName
	identityMatches := target.LogicalCluster == expectedName &&
		target.ResourceCluster == expectedResource &&
		target.DC == expectedDC &&
		target.Zone == expectedZone &&
		target.Container.Name == expectedResource+"-control-plane" &&
		target.KubeContext == "kind-"+expectedResource &&
		target.Namespace == topologyNamespace
	if !identityMatches || !validateTopologyKubeconfig(snapshotKubeconfigInput{
		instance:    instance,
		logicalName: expectedName,
		path:        target.Kubeconfig,
	}) {
		return fmt.Errorf("networkchaos: topology target %q identity is invalid", expectedName)
	}
	if !topologyDockerIDPattern.MatchString(target.Container.ID) ||
		!dockerImageIDPattern.MatchString(target.Container.ImageID) ||
		target.Container.ImageReference != topologyNodeImage ||
		!maps.Equal(
			target.Container.Labels,
			map[string]string{topologyKindClusterLabel: expectedResource},
		) {
		return fmt.Errorf("networkchaos: topology target %q container is invalid", expectedName)
	}
	if !validTopologyRuntimeTime(target.Container.StartedAt) {
		return fmt.Errorf("networkchaos: topology target %q started_at is invalid", expectedName)
	}
	expectedNodeLabels := map[string]string{
		"marketmesh.dev/cluster":           expectedName,
		"marketmesh.dev/dc":                expectedDC,
		"marketmesh.dev/owner-task":        topologyTaskKey,
		"marketmesh.dev/topology-instance": instance,
		"marketmesh.dev/zone":              expectedZone,
	}
	if target.KubernetesNode.Name != target.Container.Name ||
		target.KubernetesNode.UID == "" || len(target.KubernetesNode.UID) > 128 ||
		!maps.Equal(target.KubernetesNode.Labels, expectedNodeLabels) {
		return fmt.Errorf("networkchaos: topology target %q Kubernetes identity is invalid", expectedName)
	}
	if !topologyDockerIDPattern.MatchString(target.SandboxID) ||
		!topologyNetNSPattern.MatchString(target.NetNS) {
		return fmt.Errorf("networkchaos: topology target %q live binding is incomplete", expectedName)
	}

	expectedNetworks := []string{expectedName}
	if expectedZone == "internal" {
		expectedNetworks = []string{expectedDC + "-dmz", expectedName}
	}
	if len(target.Networks) != len(expectedNetworks) {
		return fmt.Errorf("networkchaos: topology target %q network cardinality is invalid", expectedName)
	}
	for index, network := range target.Networks {
		if err := validateTopologyNetwork(
			instance,
			expectedNetworks[index],
			expectedNetworks[index] == expectedName,
			network,
		); err != nil {
			return err
		}
	}

	return nil
}

type snapshotKubeconfigInput struct {
	instance    string
	logicalName string
	path        string
}

func validateTopologyKubeconfig(input snapshotKubeconfigInput) bool {
	if !filepath.IsAbs(input.path) || filepath.Clean(input.path) != input.path ||
		filepath.Base(input.path) != input.logicalName+".yaml" {
		return false
	}
	kubeconfigsDir := filepath.Dir(input.path)
	instanceDir := filepath.Dir(kubeconfigsDir)
	return filepath.Base(kubeconfigsDir) == "kubeconfigs" &&
		filepath.Base(instanceDir) == input.instance &&
		filepath.Base(filepath.Dir(instanceDir)) == "mm28-topology" &&
		filepath.Base(filepath.Dir(filepath.Dir(instanceDir))) == ".cache"
}

func validTopologyRuntimeTime(value string) bool {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	return err == nil && parsed.Year() >= 2000
}

func validateTopologyNetwork(
	instance string,
	expectedName string,
	expectedPrimary bool,
	network TopologyFaultTargetNetwork,
) error {
	expectedLabels := map[string]string{
		topologyOwnerLabel:    topologyTaskKey,
		topologyInstanceLabel: instance,
	}
	identityMatches := network.LogicalNetwork == expectedName &&
		network.Primary == expectedPrimary &&
		network.Name == instance+"-"+expectedName &&
		network.Driver == "bridge" && network.Scope == "local" &&
		topologyDockerIDPattern.MatchString(network.ID) &&
		maps.Equal(network.Labels, expectedLabels)
	if !identityMatches {
		return fmt.Errorf("networkchaos: topology network %q identity is invalid", expectedName)
	}
	expectedSubnets := map[string]string{
		"dc-a-dmz":      "172.28.10.0/24",
		"dc-a-internal": "172.28.11.0/24",
		"dc-b-dmz":      "172.28.20.0/24",
		"dc-b-internal": "172.28.21.0/24",
	}
	subnet, err := netip.ParsePrefix(network.Subnet)
	if err != nil || network.Subnet != expectedSubnets[expectedName] || !isPrivatePrefix(subnet) {
		return fmt.Errorf("networkchaos: topology network %q subnet is invalid", expectedName)
	}
	address, err := netip.ParsePrefix(network.Endpoint.Address)
	if err != nil || !subnet.Contains(address.Addr()) {
		return fmt.Errorf("networkchaos: topology network %q endpoint address is invalid", expectedName)
	}
	gateway, err := netip.ParseAddr(network.Endpoint.Gateway)
	if err != nil || !subnet.Contains(gateway) {
		return fmt.Errorf("networkchaos: topology network %q gateway is invalid", expectedName)
	}
	endpointMatches := topologyDockerIDPattern.MatchString(network.Endpoint.ID) &&
		network.Endpoint.NetworkID == network.ID &&
		network.Endpoint.MAC != "" &&
		network.Interface.Name != "" &&
		network.Interface.Index > 0 &&
		network.Interface.Address == network.Endpoint.Address &&
		network.Interface.MAC == network.Endpoint.MAC
	if !endpointMatches || !interfacePattern.MatchString(network.Interface.Name) {
		return fmt.Errorf("networkchaos: topology network %q live binding is invalid", expectedName)
	}

	return nil
}

func validateTopologyReceipt(
	snapshot TopologyTargetSnapshot,
	selected []TopologyFaultTarget,
	receipt TopologyValidationReceipt,
) error {
	metadataMatches := receipt.APIVersion == topologyValidationAPIVersion &&
		receipt.SnapshotToken == snapshot.Token &&
		receipt.ExpectedState == topologyExpectedStateRunning &&
		receipt.Validated
	if !metadataMatches {
		return errors.New("networkchaos: topology validation receipt metadata mismatch")
	}
	if !validTopologyRuntimeTime(receipt.ValidatedAt) {
		return errors.New("networkchaos: topology validation receipt timestamp is invalid")
	}
	if len(receipt.Targets) != len(selected) {
		return errors.New("networkchaos: topology validation receipt target count mismatch")
	}
	for index, target := range selected {
		validated := receipt.Targets[index]
		networkIDs := make([]string, 0, len(target.Networks))
		for _, network := range target.Networks {
			networkIDs = append(networkIDs, network.ID)
		}
		identityMatches := validated.LogicalCluster == target.LogicalCluster &&
			validated.ContainerID == target.Container.ID &&
			validated.State == topologyExpectedStateRunning &&
			validated.StartedAt == target.Container.StartedAt &&
			validTopologyReceiptFinishedAt(validated.FinishedAt) &&
			slices.Equal(validated.NetworkIDs, networkIDs)
		if !identityMatches {
			return fmt.Errorf(
				"networkchaos: topology validation receipt target %q mismatch",
				target.LogicalCluster,
			)
		}
	}
	if !topologyDigestPattern.MatchString(receipt.ReceiptDigest) {
		return errors.New("networkchaos: topology validation receipt digest is invalid")
	}
	expectedDigest, err := topologyReceiptDigest(receipt)
	if err != nil {
		return err
	}
	if subtle.ConstantTimeCompare([]byte(receipt.ReceiptDigest), []byte(expectedDigest)) != 1 {
		return errors.New("networkchaos: topology validation receipt digest mismatch")
	}

	return nil
}

func validTopologyReceiptFinishedAt(value string) bool {
	_, err := time.Parse(time.RFC3339Nano, value)
	return err == nil
}

func topologySnapshotDigest(snapshot TopologyTargetSnapshot) (string, error) {
	snapshot.Token = ""
	return topologyCanonicalDigest(snapshot, "target snapshot")
}

func topologyReceiptDigest(receipt TopologyValidationReceipt) (string, error) {
	receipt.ReceiptDigest = ""
	return topologyCanonicalDigest(receipt, "target validation receipt")
}

func topologyCanonicalDigest(value any, description string) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", fmt.Errorf("networkchaos: encoding topology %s: %w", description, err)
	}
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func selectTopologyTargets(
	snapshot TopologyTargetSnapshot,
	logicalNames []string,
) ([]TopologyFaultTarget, error) {
	if len(logicalNames) == 0 || len(logicalNames) > len(snapshot.Targets) {
		return nil, errors.New("networkchaos: topology validation requires a bounded non-empty target subset")
	}
	requested := make(map[string]struct{}, len(logicalNames))
	for _, name := range logicalNames {
		if _, found := requested[name]; found {
			return nil, fmt.Errorf("networkchaos: topology validation target %q is duplicated", name)
		}
		requested[name] = struct{}{}
	}
	selected := make([]TopologyFaultTarget, 0, len(requested))
	for _, target := range snapshot.Targets {
		if _, found := requested[target.LogicalCluster]; found {
			selected = append(selected, target)
			delete(requested, target.LogicalCluster)
		}
	}
	if len(requested) != 0 {
		return nil, errors.New("networkchaos: topology validation target is absent from snapshot")
	}
	return selected, nil
}

func topologyTargetByName(
	snapshot TopologyTargetSnapshot,
	logicalName string,
) (TopologyFaultTarget, error) {
	for _, target := range snapshot.Targets {
		if target.LogicalCluster == logicalName {
			return target, nil
		}
	}
	return TopologyFaultTarget{}, fmt.Errorf("networkchaos: topology target %q is absent", logicalName)
}

func topologyTargetByContainer(
	snapshot TopologyTargetSnapshot,
	ref ResourceRef,
) (TopologyFaultTarget, error) {
	for _, target := range snapshot.Targets {
		if target.Container.ID == ref.ID && target.Container.Name == ref.Name {
			return target, nil
		}
	}
	return TopologyFaultTarget{}, errors.New("networkchaos: container is absent from topology target snapshot")
}

func topologyNetworkOnTarget(
	target TopologyFaultTarget,
	logicalName string,
) (TopologyFaultTargetNetwork, error) {
	for _, network := range target.Networks {
		if network.LogicalNetwork == logicalName {
			return network, nil
		}
	}
	return TopologyFaultTargetNetwork{}, fmt.Errorf(
		"networkchaos: topology target %q has no network %q",
		target.LogicalCluster,
		logicalName,
	)
}

func topologyNetworkOnTargetRef(
	target TopologyFaultTarget,
	ref ResourceRef,
) (TopologyFaultTargetNetwork, error) {
	for _, network := range target.Networks {
		if topologyNetworkMatchesRef(network, ref) {
			return network, nil
		}
	}
	return TopologyFaultTargetNetwork{}, errors.New("networkchaos: primary network is absent from topology target")
}

func topologyNetworkByName(
	snapshot TopologyTargetSnapshot,
	logicalName string,
) (TopologyFaultTargetNetwork, error) {
	var selected TopologyFaultTargetNetwork
	found := false
	for _, target := range snapshot.Targets {
		for _, network := range target.Networks {
			if network.LogicalNetwork != logicalName {
				continue
			}
			if found && !topologyNetworkIdentityEqual(selected, network) {
				return TopologyFaultTargetNetwork{}, fmt.Errorf(
					"networkchaos: topology network %q has inconsistent identities",
					logicalName,
				)
			}
			selected = network
			found = true
		}
	}
	if !found {
		return TopologyFaultTargetNetwork{}, fmt.Errorf(
			"networkchaos: topology network %q is absent",
			logicalName,
		)
	}
	return selected, nil
}

func topologyNetworkByRef(
	snapshot TopologyTargetSnapshot,
	ref ResourceRef,
) (TopologyFaultTargetNetwork, string, error) {
	for _, target := range snapshot.Targets {
		for _, network := range target.Networks {
			if topologyNetworkMatchesRef(network, ref) {
				return network, target.LogicalCluster, nil
			}
		}
	}
	return TopologyFaultTargetNetwork{}, "", errors.New(
		"networkchaos: peer network is absent from topology target snapshot",
	)
}

func topologyNetworkMatchesRef(network TopologyFaultTargetNetwork, ref ResourceRef) bool {
	return network.ID == ref.ID && network.Name == ref.Name
}

func topologyNetworkIdentityEqual(left, right TopologyFaultTargetNetwork) bool {
	return left.ID == right.ID && left.Name == right.Name && left.Subnet == right.Subnet &&
		maps.Equal(left.Labels, right.Labels)
}

func topologyContainerRef(target TopologyFaultTarget) ResourceRef {
	return ResourceRef{
		ID:   target.Container.ID,
		Name: target.Container.Name,
	}
}

func topologyNetworkRef(network TopologyFaultTargetNetwork) ResourceRef {
	return ResourceRef{
		ID:   network.ID,
		Name: network.Name,
	}
}

func topologyNetworkSnapshot(network TopologyFaultTargetNetwork) (Network, error) {
	prefix, err := netip.ParsePrefix(network.Subnet)
	if err != nil {
		return Network{}, fmt.Errorf("networkchaos: parsing topology network subnet: %w", err)
	}
	return Network{
		Resource: Resource{
			ID:     network.ID,
			Name:   network.Name,
			Labels: maps.Clone(network.Labels),
		},
		Prefixes: []netip.Prefix{prefix},
	}, nil
}

func topologySnapshotsEqual(left, right Snapshot) bool {
	if left.Container.ID != right.Container.ID || left.Container.Name != right.Container.Name ||
		!maps.Equal(left.Container.Labels, right.Container.Labels) ||
		left.Interface != right.Interface ||
		!topologyNetworksEqual(left.Network, right.Network) ||
		len(left.PeerNetworks) != len(right.PeerNetworks) {
		return false
	}
	for index := range left.PeerNetworks {
		if !topologyNetworksEqual(left.PeerNetworks[index], right.PeerNetworks[index]) {
			return false
		}
	}
	return true
}

func topologyNetworksEqual(left, right Network) bool {
	return left.Resource.ID == right.Resource.ID &&
		left.Resource.Name == right.Resource.Name &&
		maps.Equal(left.Resource.Labels, right.Resource.Labels) &&
		slices.Equal(left.Prefixes, right.Prefixes)
}

func uniqueStrings(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if _, found := seen[value]; found {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func cloneTopologySnapshot(snapshot TopologyTargetSnapshot) TopologyTargetSnapshot {
	snapshot.Targets = slices.Clone(snapshot.Targets)
	for targetIndex := range snapshot.Targets {
		target := &snapshot.Targets[targetIndex]
		target.Container.Labels = maps.Clone(target.Container.Labels)
		target.KubernetesNode.Labels = maps.Clone(target.KubernetesNode.Labels)
		target.Networks = slices.Clone(target.Networks)
		for networkIndex := range target.Networks {
			target.Networks[networkIndex].Labels = maps.Clone(target.Networks[networkIndex].Labels)
		}
	}
	return snapshot
}

func cloneTopologyReceipt(receipt TopologyValidationReceipt) TopologyValidationReceipt {
	receipt.Targets = slices.Clone(receipt.Targets)
	for index := range receipt.Targets {
		receipt.Targets[index].NetworkIDs = slices.Clone(receipt.Targets[index].NetworkIDs)
	}
	return receipt
}

func decodeTopologyDocument(data []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	return requireJSONEOF(decoder)
}

type execTopologyCommandRunner struct {
	executable     string
	maxOutputBytes int
}

type execTopologyDockerContextValidator struct {
	commands dockerCommandRunner
}

func (validator execTopologyDockerContextValidator) Validate(
	ctx context.Context,
	dockerContext string,
	dockerHost string,
) error {
	result, err := validator.commands.Run(
		ctx,
		"context",
		"inspect",
		dockerContext,
		"--format",
		"{{json .Endpoints.docker.Host}}",
	)
	if err != nil {
		return err
	}
	observedHost := ""
	if decodeErr := decodeTopologyDocument(result.Output, &observedHost); decodeErr != nil {
		return errors.New("networkchaos: Docker context returned an invalid endpoint")
	}
	if observedHost != dockerHost {
		return errors.New("networkchaos: Docker context endpoint differs from the pinned host")
	}
	return nil
}

func (runner execTopologyCommandRunner) Run(
	ctx context.Context,
	stdin []byte,
	arguments ...string,
) ([]byte, error) {
	if err := requireBoundedTopologyContext(ctx); err != nil {
		return nil, err
	}
	if len(arguments) == 0 {
		return nil, errors.New("networkchaos: topology command arguments are empty")
	}
	if len(stdin) > runner.maxOutputBytes {
		return nil, errors.New("networkchaos: topology command input exceeded limit")
	}

	stdout := newBoundedCommandBuffer(runner.maxOutputBytes)
	stderr := newBoundedCommandBuffer(runner.maxOutputBytes)
	// #nosec G204 -- executable is an exact validated absolute regular file;
	// arguments are fixed flags and allowlisted config values, with no shell.
	command := exec.CommandContext(ctx, runner.executable, arguments...)
	command.Stdin = bytes.NewReader(stdin)
	command.Stdout = stdout
	command.Stderr = stderr
	err := command.Run()
	if stdout.Exceeded() || stderr.Exceeded() {
		return nil, errors.New("networkchaos: topology command output exceeded limit")
	}
	if err == nil {
		return stdout.Bytes(), nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return nil, fmt.Errorf("networkchaos: topology command failed with exit code %d", exitErr.ExitCode())
	}
	return nil, fmt.Errorf("networkchaos: starting topology command: %w", err)
}
