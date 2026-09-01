package dcfailover

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	mm28InventoryAPIVersion = "marketmesh.dev/e2e-topology/v1"
	mm28TaskKey             = "MM-28"
	mm28Namespace           = "marketmesh-system"
	mm28TunnelPort          = 30443
	mm28OwnerLabel          = "com.marketmesh.task"
	mm28InstanceLabel       = "com.marketmesh.topology"
	mm28ClusterLabel        = "io.x-k8s.kind.cluster"
	mm28RunPrefix           = "mm35-"
	mm28CommandOutputLimit  = 4 << 20
)

var (
	mm28InstancePattern      = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,18}[a-z0-9])?$`)
	mm28DockerContextPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,62}$`)
)

// MM28TopologyConfig selects one exact disposable MM-28 topology instance.
type MM28TopologyConfig struct {
	RepositoryRoot string
	Instance       string
	DockerContext  string
	CommandTimeout time.Duration
}

// MM28Topology consumes MM-28 through its public CLI and versioned inventory.
// It never uses an ambient Docker or Kubernetes context.
type MM28Topology struct {
	config   MM28TopologyConfig
	commands topologyCommandRunner

	mu         sync.RWMutex
	authorized *Snapshot
	cleaned    bool
}

type topologyCommand struct {
	program string
	args    []string
	dir     string
}

type topologyCommandResult struct {
	stdout []byte
}

type topologyCommandRunner interface {
	Run(context.Context, topologyCommand) (topologyCommandResult, error)
}

type execTopologyCommandRunner struct{}

// NewMM28Topology creates the concrete topology adapter used by MM-35.
func NewMM28Topology(config MM28TopologyConfig) (*MM28Topology, error) {
	topology, err := newMM28Topology(config, execTopologyCommandRunner{})
	if err != nil {
		return nil, err
	}
	if err := validateMM28RepositoryRoot(config.RepositoryRoot); err != nil {
		return nil, err
	}

	return topology, nil
}

func newMM28Topology(
	config MM28TopologyConfig,
	commands topologyCommandRunner,
) (*MM28Topology, error) {
	if err := validateMM28TopologyConfig(config); err != nil {
		return nil, err
	}
	if commands == nil {
		return nil, errors.New("dcfailover: topology command runner is required")
	}

	return &MM28Topology{config: config, commands: commands}, nil
}

func validateMM28TopologyConfig(config MM28TopologyConfig) error {
	if !filepath.IsAbs(config.RepositoryRoot) ||
		filepath.Clean(config.RepositoryRoot) != config.RepositoryRoot ||
		config.RepositoryRoot == string(filepath.Separator) {
		return errors.New("dcfailover: topology repository root must be an absolute clean path")
	}
	if !strings.HasPrefix(config.Instance, mm28RunPrefix) ||
		!mm28InstancePattern.MatchString(config.Instance) {
		return errors.New("dcfailover: topology instance must be a unique 1-20 character mm35-* name")
	}
	if !mm28DockerContextPattern.MatchString(config.DockerContext) {
		return errors.New("dcfailover: topology docker context contains unsupported characters")
	}
	if err := validateTimeout("topology command", config.CommandTimeout); err != nil {
		return err
	}

	return nil
}

func validateMM28RepositoryRoot(root string) error {
	for _, relativePath := range []string{
		"Taskfile.yml",
		filepath.Join("tools", "e2e-topology", "go.mod"),
		filepath.Join("tools", "e2e-topology", "main.go"),
	} {
		info, err := os.Lstat(filepath.Join(root, relativePath))
		if err != nil || !info.Mode().IsRegular() {
			return errors.New("dcfailover: repository root does not contain the MM-28 topology tool")
		}
	}

	return nil
}

// Preflight validates the public inventory, cluster readiness, and live Docker
// ownership before authorizing any state-changing operation.
func (topology *MM28Topology) Preflight(ctx context.Context, runID string) (Snapshot, error) {
	if ctx == nil {
		return Snapshot{}, errors.New("dcfailover: topology preflight context must not be nil")
	}
	if runID != topology.config.Instance {
		return Snapshot{}, errors.New("dcfailover: topology run id does not match configured instance")
	}

	result, err := topology.runMM28(ctx, "inventory")
	if err != nil {
		return Snapshot{}, fmt.Errorf("dcfailover: reading MM-28 inventory: %w", err)
	}
	inventory, err := decodeMM28Inventory(result.stdout)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot, err := topology.snapshotFromInventory(inventory)
	if err != nil {
		return Snapshot{}, err
	}
	snapshot, err = validateSnapshot(snapshot, runID)
	if err != nil {
		return Snapshot{}, err
	}
	if err := topology.validateRuntimeSnapshot(ctx, snapshot, true); err != nil {
		return Snapshot{}, fmt.Errorf("dcfailover: validating MM-28 runtime ownership: %w", err)
	}
	if _, err := topology.runMM28(ctx, "ready"); err != nil {
		return Snapshot{}, fmt.Errorf("dcfailover: validating MM-28 readiness: %w", err)
	}

	topology.mu.Lock()
	authorized := cloneSnapshot(snapshot)
	topology.authorized = &authorized
	topology.cleaned = false
	topology.mu.Unlock()

	return cloneSnapshot(snapshot), nil
}

// StopDC stops only the two exact, currently running control-plane containers
// authorized for target. Every target resource is revalidated before the first
// container is stopped.
func (topology *MM28Topology) StopDC(
	ctx context.Context,
	target DCTarget,
	kind OutageKind,
) error {
	if ctx == nil {
		return errors.New("dcfailover: topology stop context must not be nil")
	}
	if kind != OutageManaged && kind != OutageSudden {
		return errors.New("dcfailover: topology outage kind is unsupported")
	}
	authorized, err := topology.authorizedTarget(target)
	if err != nil {
		return err
	}
	if err := topology.validateRuntimeClusters(ctx, authorized.Clusters, true); err != nil {
		return fmt.Errorf("dcfailover: validating exact dc before stop: %w", err)
	}

	for _, cluster := range authorized.Clusters {
		container := cluster.ContainerNames[0]
		if _, err := topology.runDocker(ctx, "container", "stop", "--time", "30", container); err != nil {
			return fmt.Errorf("dcfailover: stopping exact container %s: %w", container, err)
		}
	}
	if err := topology.validateRuntimeClusters(ctx, authorized.Clusters, false); err != nil {
		return fmt.Errorf("dcfailover: verifying stopped dc: %w", err)
	}

	return nil
}

// RestoreDC starts only stopped containers from the authorized exact target and
// verifies that both control planes are running again.
func (topology *MM28Topology) RestoreDC(ctx context.Context, target DCTarget) error {
	if ctx == nil {
		return errors.New("dcfailover: topology restore context must not be nil")
	}
	authorized, err := topology.authorizedTarget(target)
	if err != nil {
		return err
	}
	if err := topology.restoreClusters(ctx, authorized.Clusters); err != nil {
		return fmt.Errorf("dcfailover: restoring exact dc: %w", err)
	}

	return nil
}

// Inspect delegates bounded, non-secret diagnostics to the public MM-28 CLI.
func (topology *MM28Topology) Inspect(ctx context.Context, snapshot Snapshot) error {
	if ctx == nil {
		return errors.New("dcfailover: topology inspect context must not be nil")
	}
	if _, err := topology.matchAuthorizedSnapshot(snapshot); err != nil {
		return err
	}
	if _, err := topology.runMM28(ctx, "inspect"); err != nil {
		return fmt.Errorf("dcfailover: collecting MM-28 diagnostics: %w", err)
	}

	return nil
}

// Cleanup restores any stopped owned control planes, then delegates exact,
// fail-closed removal to MM-28. MM-28 captures diagnostics again before delete.
func (topology *MM28Topology) Cleanup(ctx context.Context, snapshot Snapshot) error {
	if ctx == nil {
		return errors.New("dcfailover: topology cleanup context must not be nil")
	}
	authorized, err := topology.matchAuthorizedSnapshot(snapshot)
	if err != nil {
		return err
	}

	topology.mu.RLock()
	cleaned := topology.cleaned
	topology.mu.RUnlock()
	if cleaned {
		return nil
	}

	restoreErr := topology.restoreClusters(ctx, authorized.Clusters)
	_, downErr := topology.runMM28(ctx, "down")
	if downErr == nil {
		topology.mu.Lock()
		topology.cleaned = true
		topology.mu.Unlock()
	}

	return errors.Join(
		wrapOptionalError("dcfailover: restoring exact resources before cleanup", restoreErr),
		wrapOptionalError("dcfailover: removing exact MM-28 resources", downErr),
	)
}

func (topology *MM28Topology) restoreClusters(ctx context.Context, clusters []Cluster) error {
	states := make([]bool, len(clusters))
	var validationErrors []error
	for index, cluster := range clusters {
		running, err := topology.validateRuntimeCluster(ctx, cluster)
		if err != nil {
			validationErrors = append(validationErrors, err)
			continue
		}
		states[index] = running
	}
	if err := errors.Join(validationErrors...); err != nil {
		return err
	}

	for index, cluster := range clusters {
		if states[index] {
			continue
		}
		container := cluster.ContainerNames[0]
		if _, err := topology.runDocker(ctx, "container", "start", container); err != nil {
			return fmt.Errorf("starting exact container %s: %w", container, err)
		}
	}
	if err := topology.validateRuntimeClusters(ctx, clusters, true); err != nil {
		return fmt.Errorf("verifying restored containers: %w", err)
	}

	return nil
}

func (topology *MM28Topology) authorizedTarget(target DCTarget) (DCTarget, error) {
	snapshot, err := topology.authorizedSnapshot()
	if err != nil {
		return DCTarget{}, err
	}
	authorized, err := targetForDC(snapshot, target.DC)
	if err != nil {
		return DCTarget{}, err
	}
	if !equalDCTarget(authorized, target) {
		return DCTarget{}, errors.New("dcfailover: topology target differs from authorized preflight")
	}

	return cloneTarget(authorized), nil
}

func (topology *MM28Topology) matchAuthorizedSnapshot(snapshot Snapshot) (Snapshot, error) {
	authorized, err := topology.authorizedSnapshot()
	if err != nil {
		return Snapshot{}, err
	}
	if !equalSnapshots(authorized, snapshot) {
		return Snapshot{}, errors.New("dcfailover: topology snapshot differs from authorized preflight")
	}

	return authorized, nil
}

func (topology *MM28Topology) authorizedSnapshot() (Snapshot, error) {
	topology.mu.RLock()
	defer topology.mu.RUnlock()
	if topology.authorized == nil {
		return Snapshot{}, errors.New("dcfailover: topology preflight has not authorized resources")
	}

	return cloneSnapshot(*topology.authorized), nil
}

func (topology *MM28Topology) validateRuntimeSnapshot(
	ctx context.Context,
	snapshot Snapshot,
	wantRunning bool,
) error {
	return topology.validateRuntimeClusters(ctx, snapshot.Clusters, wantRunning)
}

func (topology *MM28Topology) validateRuntimeClusters(
	ctx context.Context,
	clusters []Cluster,
	wantRunning bool,
) error {
	for _, cluster := range clusters {
		running, err := topology.validateRuntimeCluster(ctx, cluster)
		if err != nil {
			return err
		}
		if running != wantRunning {
			return fmt.Errorf("container %s has unexpected running state", cluster.ContainerNames[0])
		}
	}

	return nil
}

func (topology *MM28Topology) validateRuntimeCluster(
	ctx context.Context,
	cluster Cluster,
) (bool, error) {
	if len(cluster.ContainerNames) != 1 || len(cluster.NetworkNames) != 1 {
		return false, errors.New("authorized cluster must contain one exact container and network")
	}
	containerName := cluster.ContainerNames[0]
	networkName := cluster.NetworkNames[0]

	result, err := topology.runDocker(ctx, "container", "inspect", containerName)
	if err != nil {
		return false, fmt.Errorf("inspecting exact container %s: %w", containerName, err)
	}
	container, err := decodeDockerContainer(result.stdout)
	if err != nil {
		return false, fmt.Errorf("decoding exact container %s: %w", containerName, err)
	}
	if strings.TrimPrefix(container.Name, "/") != containerName ||
		container.Config.Labels[mm28ClusterLabel] != cluster.Name {
		return false, fmt.Errorf("refusing unowned container %s", containerName)
	}
	if _, attached := container.NetworkSettings.Networks[networkName]; !attached {
		return false, fmt.Errorf("container %s is not attached to exact network", containerName)
	}

	result, err = topology.runDocker(ctx, "network", "inspect", networkName)
	if err != nil {
		return false, fmt.Errorf("inspecting exact network %s: %w", networkName, err)
	}
	network, err := decodeDockerNetwork(result.stdout)
	if err != nil {
		return false, fmt.Errorf("decoding exact network %s: %w", networkName, err)
	}
	if network.Name != networkName ||
		network.Labels[mm28OwnerLabel] != mm28TaskKey ||
		network.Labels[mm28InstanceLabel] != topology.config.Instance {
		return false, fmt.Errorf("refusing unowned network %s", networkName)
	}
	if !networkContainsExactContainer(network, containerName) {
		return false, fmt.Errorf("network %s does not contain exact container", networkName)
	}

	return container.State.Running, nil
}

func (topology *MM28Topology) snapshotFromInventory(inventory mm28Inventory) (Snapshot, error) {
	if err := topology.validateInventoryHeader(inventory); err != nil {
		return Snapshot{}, err
	}
	expected := mm28ExpectedClusters(topology.config)
	if len(inventory.Clusters) != len(expected) {
		return Snapshot{}, errors.New("dcfailover: MM-28 inventory must contain exactly four clusters")
	}

	clusters := make([]Cluster, 0, len(expected))
	for index, expectedCluster := range expected {
		actual := inventory.Clusters[index]
		if err := validateMM28InventoryCluster(actual, expectedCluster); err != nil {
			return Snapshot{}, fmt.Errorf("dcfailover: validating MM-28 cluster %s: %w", expectedCluster.logicalName, err)
		}
		clusters = append(clusters, Cluster{
			DC:             expectedCluster.dc,
			Zone:           expectedCluster.zone,
			Name:           expectedCluster.resourceName,
			Kubeconfig:     expectedCluster.kubeconfig,
			KubeContext:    expectedCluster.kubeContext,
			OwnerRunID:     inventory.Ownership.DockerLabels[mm28InstanceLabel],
			ContainerNames: []string{expectedCluster.containerName},
			NetworkNames:   []string{expectedCluster.networkName},
		})
	}

	return Snapshot{
		RunID:       inventory.Instance,
		Environment: EnvironmentLocalE2E,
		Disposable:  true,
		Clusters:    clusters,
	}, nil
}

func (topology *MM28Topology) validateInventoryHeader(inventory mm28Inventory) error {
	if inventory.APIVersion != mm28InventoryAPIVersion ||
		inventory.Task != mm28TaskKey ||
		inventory.Instance != topology.config.Instance ||
		inventory.DockerContext != topology.config.DockerContext ||
		inventory.Namespace != mm28Namespace ||
		inventory.TunnelPort != mm28TunnelPort {
		return errors.New("dcfailover: MM-28 inventory header does not match the configured topology")
	}
	if inventory.Ownership.DockerLabels[mm28OwnerLabel] != mm28TaskKey ||
		inventory.Ownership.DockerLabels[mm28InstanceLabel] != topology.config.Instance ||
		inventory.Ownership.KubernetesLabels["marketmesh.dev/owner-task"] != mm28TaskKey ||
		inventory.Ownership.KubernetesLabels["marketmesh.dev/topology-instance"] != topology.config.Instance {
		return errors.New("dcfailover: MM-28 inventory ownership labels do not match")
	}

	prefix := fmt.Sprintf(
		"go run ./tools/e2e-topology --instance %s --docker-context %s",
		topology.config.Instance,
		topology.config.DockerContext,
	)
	if inventory.Commands.Ready != prefix+" ready" ||
		inventory.Commands.Inspect != prefix+" inspect" ||
		inventory.Commands.Down != prefix+" down" {
		return errors.New("dcfailover: MM-28 inventory lifecycle commands do not match")
	}

	return nil
}

func (topology *MM28Topology) runMM28(
	ctx context.Context,
	action string,
) (topologyCommandResult, error) {
	switch action {
	case "inventory", "ready", "inspect", "down":
	default:
		return topologyCommandResult{}, errors.New("unsupported MM-28 action")
	}

	return topology.runCommand(ctx, topologyCommand{
		program: "go",
		args: []string{
			"run",
			"./tools/e2e-topology",
			"--instance",
			topology.config.Instance,
			"--docker-context",
			topology.config.DockerContext,
			action,
		},
		dir: topology.config.RepositoryRoot,
	})
}

func (topology *MM28Topology) runDocker(
	ctx context.Context,
	args ...string,
) (topologyCommandResult, error) {
	commandArgs := []string{"--context", topology.config.DockerContext}
	commandArgs = append(commandArgs, args...)

	return topology.runCommand(ctx, topologyCommand{program: "docker", args: commandArgs})
}

func (topology *MM28Topology) runCommand(
	ctx context.Context,
	command topologyCommand,
) (topologyCommandResult, error) {
	commandCtx, cancel := context.WithTimeout(ctx, topology.config.CommandTimeout)
	defer cancel()

	return topology.commands.Run(commandCtx, command)
}

func (execTopologyCommandRunner) Run(
	ctx context.Context,
	command topologyCommand,
) (topologyCommandResult, error) {
	if command.program != "go" && command.program != "docker" {
		return topologyCommandResult{}, errors.New("dcfailover: unsupported command program")
	}
	stdout := &boundedCommandBuffer{remaining: mm28CommandOutputLimit}
	stderr := &boundedCommandBuffer{remaining: mm28CommandOutputLimit}
	// #nosec G204 -- the program and every argument come from the validated static MM-35 plan.
	process := exec.CommandContext(ctx, command.program, command.args...)
	process.Dir = command.dir
	process.Env = sanitizedTopologyEnvironment()
	process.Stdout = stdout
	process.Stderr = stderr
	err := process.Run()
	if errors.Is(stdout.err, errCommandOutputLimit) || errors.Is(stderr.err, errCommandOutputLimit) {
		return topologyCommandResult{}, errCommandOutputLimit
	}
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return topologyCommandResult{}, fmt.Errorf("running %s: %w", command.program, ctxErr)
		}
		return topologyCommandResult{}, fmt.Errorf("running %s: %w", command.program, err)
	}

	return topologyCommandResult{stdout: slices.Clone(stdout.buffer.Bytes())}, nil
}

var errCommandOutputLimit = errors.New("dcfailover: command output exceeded limit")

type boundedCommandBuffer struct {
	buffer    bytes.Buffer
	remaining int
	err       error
}

func (buffer *boundedCommandBuffer) Write(data []byte) (int, error) {
	if buffer.err != nil {
		return 0, buffer.err
	}
	if len(data) > buffer.remaining {
		if buffer.remaining > 0 {
			_, _ = buffer.buffer.Write(data[:buffer.remaining])
			buffer.remaining = 0
		}
		buffer.err = errCommandOutputLimit
		return 0, buffer.err
	}
	n, err := buffer.buffer.Write(data)
	buffer.remaining -= n
	if err != nil {
		buffer.err = err
	}

	return n, err
}

func sanitizedTopologyEnvironment() []string {
	blocked := map[string]struct{}{
		"DOCKER_CONTEXT":                   {},
		"KIND_EXPERIMENTAL_DOCKER_NETWORK": {},
		"KIND_EXPERIMENTAL_PROVIDER":       {},
		"KUBECONFIG":                       {},
	}
	environment := make([]string, 0, len(os.Environ()))
	for _, value := range os.Environ() {
		key, _, ok := strings.Cut(value, "=")
		if !ok {
			continue
		}
		if _, found := blocked[key]; found {
			continue
		}
		environment = append(environment, value)
	}

	return environment
}

type mm28Inventory struct {
	APIVersion    string               `json:"api_version"`
	Task          string               `json:"task"`
	Instance      string               `json:"instance"`
	DockerContext string               `json:"docker_context"`
	Namespace     string               `json:"namespace"`
	TunnelPort    int                  `json:"tunnel_port"`
	Ownership     mm28InventoryOwner   `json:"ownership"`
	Commands      mm28InventoryCommand `json:"commands"`
	Clusters      []mm28InventoryEntry `json:"clusters"`
}

type mm28InventoryOwner struct {
	DockerLabels     map[string]string `json:"docker_labels"`
	KubernetesLabels map[string]string `json:"kubernetes_labels"`
}

type mm28InventoryCommand struct {
	Ready   string `json:"ready"`
	Inspect string `json:"inspect"`
	Down    string `json:"down"`
}

type mm28InventoryEntry struct {
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

type expectedMM28Cluster struct {
	logicalName   string
	resourceName  string
	dc            DC
	zone          Zone
	networkName   string
	containerName string
	kubeconfig    string
	kubeContext   string
}

func mm28ExpectedClusters(config MM28TopologyConfig) []expectedMM28Cluster {
	specifications := []struct {
		logicalName string
		dc          DC
		zone        Zone
	}{
		{logicalName: "dc-a-dmz", dc: DCA, zone: ZoneDMZ},
		{logicalName: "dc-a-internal", dc: DCA, zone: ZoneInternal},
		{logicalName: "dc-b-dmz", dc: DCB, zone: ZoneDMZ},
		{logicalName: "dc-b-internal", dc: DCB, zone: ZoneInternal},
	}
	clusters := make([]expectedMM28Cluster, 0, len(specifications))
	for _, specification := range specifications {
		resourceName := config.Instance + "-" + specification.logicalName
		clusters = append(clusters, expectedMM28Cluster{
			logicalName:   specification.logicalName,
			resourceName:  resourceName,
			dc:            specification.dc,
			zone:          specification.zone,
			networkName:   resourceName,
			containerName: resourceName + "-control-plane",
			kubeconfig: filepath.Join(
				config.RepositoryRoot,
				".cache",
				"mm28-topology",
				config.Instance,
				"kubeconfigs",
				specification.logicalName+".yaml",
			),
			kubeContext: "kind-" + resourceName,
		})
	}

	return clusters
}

func validateMM28InventoryCluster(
	actual mm28InventoryEntry,
	expected expectedMM28Cluster,
) error {
	if actual.LogicalName != expected.logicalName ||
		actual.ResourceName != expected.resourceName ||
		actual.DC != string(expected.dc) ||
		actual.Zone != string(expected.zone) ||
		actual.NetworkName != expected.networkName ||
		actual.Kubeconfig != expected.kubeconfig ||
		actual.Context != expected.kubeContext ||
		actual.Namespace != mm28Namespace ||
		actual.WorkloadIdentityFormat != "<pod>/<namespace>/<logical-cluster>" {
		return errors.New("cluster identifiers do not match the exact expected plan")
	}
	if !filepath.IsAbs(actual.Kubeconfig) || net.ParseIP(actual.ControlPlaneAddress).To4() == nil {
		return errors.New("cluster kubeconfig or control-plane address is invalid")
	}

	return nil
}

type dockerContainerInspection struct {
	Name   string `json:"Name"`
	Config struct {
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	State struct {
		Running bool `json:"Running"`
	} `json:"State"`
	NetworkSettings struct {
		Networks map[string]json.RawMessage `json:"Networks"`
	} `json:"NetworkSettings"`
}

type dockerNetworkInspection struct {
	Name       string            `json:"Name"`
	Labels     map[string]string `json:"Labels"`
	Containers map[string]struct {
		Name string `json:"Name"`
	} `json:"Containers"`
}

func decodeMM28Inventory(data []byte) (mm28Inventory, error) {
	var inventory mm28Inventory
	if err := decodeSingleJSON(data, &inventory); err != nil {
		return mm28Inventory{}, fmt.Errorf("dcfailover: decoding MM-28 inventory: %w", err)
	}

	return inventory, nil
}

func decodeDockerContainer(data []byte) (dockerContainerInspection, error) {
	var containers []dockerContainerInspection
	if err := decodeSingleJSON(data, &containers); err != nil || len(containers) != 1 {
		return dockerContainerInspection{}, errors.New("invalid Docker container inspection")
	}
	if containers[0].Config.Labels == nil {
		containers[0].Config.Labels = map[string]string{}
	}
	if containers[0].NetworkSettings.Networks == nil {
		containers[0].NetworkSettings.Networks = map[string]json.RawMessage{}
	}

	return containers[0], nil
}

func decodeDockerNetwork(data []byte) (dockerNetworkInspection, error) {
	var networks []dockerNetworkInspection
	if err := decodeSingleJSON(data, &networks); err != nil || len(networks) != 1 {
		return dockerNetworkInspection{}, errors.New("invalid Docker network inspection")
	}
	if networks[0].Labels == nil {
		networks[0].Labels = map[string]string{}
	}
	if networks[0].Containers == nil {
		networks[0].Containers = map[string]struct {
			Name string `json:"Name"`
		}{}
	}

	return networks[0], nil
}

func decodeSingleJSON(data []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values are not allowed")
		}
		return err
	}

	return nil
}

func networkContainsExactContainer(network dockerNetworkInspection, containerName string) bool {
	for _, attachment := range network.Containers {
		if attachment.Name == containerName {
			return true
		}
	}

	return false
}

func equalSnapshots(left, right Snapshot) bool {
	return left.RunID == right.RunID &&
		left.Environment == right.Environment &&
		left.Disposable == right.Disposable &&
		slices.EqualFunc(left.Clusters, right.Clusters, equalClusters)
}

func equalDCTarget(left, right DCTarget) bool {
	return left.DC == right.DC && slices.EqualFunc(left.Clusters, right.Clusters, equalClusters)
}

func equalClusters(left, right Cluster) bool {
	return left.DC == right.DC &&
		left.Zone == right.Zone &&
		left.Name == right.Name &&
		left.Kubeconfig == right.Kubeconfig &&
		left.KubeContext == right.KubeContext &&
		left.OwnerRunID == right.OwnerRunID &&
		slices.Equal(left.ContainerNames, right.ContainerNames) &&
		slices.Equal(left.NetworkNames, right.NetworkNames)
}

func wrapOptionalError(message string, err error) error {
	if err == nil {
		return nil
	}

	return fmt.Errorf("%s: %w", message, err)
}

var (
	_ Topology  = (*MM28Topology)(nil)
	_ io.Writer = (*boundedCommandBuffer)(nil)
)
