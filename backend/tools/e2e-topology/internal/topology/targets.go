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
	sha256DigestPattern  = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
	machineIDPattern     = regexp.MustCompile(`^[0-9A-HJKMNP-TV-Z]{26}$`)
	machineMACPattern    = regexp.MustCompile(`^[0-9a-f]{2}(:[0-9a-f]{2}){5}$`)
	bootIDPattern        = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	ifaceNamePattern     = regexp.MustCompile(`^[a-z][a-z0-9]{1,14}$`)
)

// ExpectedTargetState is the only mutable machine state accepted by validation.
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
	ConsumerTask  string         `json:"consumer_task"`
	ConsumerRunID string         `json:"consumer_run_id"`
	ResolvedAt    string         `json:"resolved_at"`
	Selector      TargetSelector `json:"selector"`
	Targets       []FaultTarget  `json:"targets"`
	PreviousToken string         `json:"previous_token,omitempty"`
	Token         string         `json:"token"`
}

// FaultTarget identifies one OrbStack machine hosting one logical cluster.
type FaultTarget struct {
	LogicalCluster  string                    `json:"logical_cluster"`
	ResourceCluster string                    `json:"resource_cluster"`
	DC              string                    `json:"dc"`
	Zone            string                    `json:"zone"`
	Kubeconfig      string                    `json:"kubeconfig"`
	KubeContext     string                    `json:"kube_context"`
	Namespace       string                    `json:"namespace"`
	Machine         FaultTargetMachine        `json:"machine"`
	KubernetesNode  FaultTargetKubernetesNode `json:"kubernetes_node"`
}

// FaultTargetMachine contains the immutable OrbStack machine identity plus the
// boot generation that must change across any stop/start transition.
type FaultTargetMachine struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	IPv4      string `json:"ipv4"`
	MAC       string `json:"mac"`
	Interface string `json:"interface"`
	BootID    string `json:"boot_id"`
}

// FaultTargetKubernetesNode contains exact Kubernetes identity and ownership.
type FaultTargetKubernetesNode struct {
	Name   string            `json:"name"`
	UID    string            `json:"uid"`
	Labels map[string]string `json:"labels"`
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
	LogicalCluster string `json:"logical_cluster"`
	MachineID      string `json:"machine_id"`
	State          string `json:"state"`
	IPv4           string `json:"ipv4"`
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
	MachineID            string `json:"machine_id"`
	BootID               string `json:"boot_id"`
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

// machineInterface binds the primary in-guest interface to the machine IPv4.
type machineInterface struct {
	Name    string
	MAC     string
	Address string
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
		observed, validateErr := t.validateTargetRuntime(ctx, target, request.ExpectedState)
		if validateErr != nil {
			return TargetValidationReceipt{}, fmt.Errorf("validating target %s: %w", target.LogicalCluster, validateErr)
		}
		receipt.Targets = append(receipt.Targets, ValidatedFaultTarget{
			LogicalCluster: target.LogicalCluster,
			MachineID:      target.Machine.ID,
			State:          observed.State,
			IPv4:           observed.IPv4,
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

	refreshedTarget, err := t.rebindRunningTarget(ctx, target)
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
		MachineID:            target.Machine.ID,
		BootID:               refreshedTarget.Machine.BootID,
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

// rebindRunningTarget proves that the same machine came back with a new boot generation.
func (t *Topology) rebindRunningTarget(ctx context.Context, target FaultTarget) (FaultTarget, error) {
	machine, err := t.inspectMachine(ctx, target.Machine.Name)
	if err != nil {
		return FaultTarget{}, err
	}
	if machine.ID != target.Machine.ID {
		return FaultTarget{}, errors.New("topology: immutable machine identity changed during rebind")
	}
	if machine.State != string(ExpectedStateRunning) {
		return FaultTarget{}, errors.New("topology: rebound machine is not running")
	}
	if machine.IPv4 != target.Machine.IPv4 {
		return FaultTarget{}, errors.New("topology: rebound machine address changed")
	}
	iface, err := t.primaryMachineInterface(ctx, machine)
	if err != nil {
		return FaultTarget{}, err
	}
	if iface.MAC != target.Machine.MAC || iface.Name != target.Machine.Interface {
		return FaultTarget{}, errors.New("topology: rebound machine interface identity changed")
	}
	bootID, err := t.machineBootID(ctx, machine.Name)
	if err != nil {
		return FaultTarget{}, err
	}
	if bootID == target.Machine.BootID {
		return FaultTarget{}, errors.New("topology: no proved stopped-to-started generation transition")
	}

	cluster, err := t.config.Cluster(target.DC, target.Zone)
	if err != nil {
		return FaultTarget{}, err
	}
	node, err := t.resolveKubernetesNode(ctx, cluster)
	if err != nil {
		return FaultTarget{}, err
	}
	if node.UID != target.KubernetesNode.UID || !exactLabels(node.Labels, target.KubernetesNode.Labels) {
		return FaultTarget{}, errors.New("topology: kubernetes node identity changed during rebind")
	}

	// Netfilter state does not survive a VM stop/start: identity is already
	// proved, so rebuild the zone firewall of the rebound machine and wait for
	// the node before handing the refreshed snapshot to chaos consumers.
	// Peer rules keep pointing at the unchanged machine IPv4.
	machines, err := t.runningMachines(ctx)
	if err != nil {
		return FaultTarget{}, err
	}
	if err := t.ensureFirewallToolchain(ctx, cluster); err != nil {
		return FaultTarget{}, err
	}
	rules, err := zoneChainRules(cluster, machines)
	if err != nil {
		return FaultTarget{}, err
	}
	if err := t.configureZoneFirewall(
		ctx,
		cluster.Name,
		t.peerIPv4s(machines, cluster.LogicalName),
		zoneChainName(cluster),
		rules,
	); err != nil {
		return FaultTarget{}, fmt.Errorf("restoring zone firewall in %s: %w", cluster.Name, err)
	}
	if _, err := t.runKubectl(
		ctx,
		readyTimeout,
		cluster,
		"wait",
		"--for=condition=Ready",
		"node/"+cluster.NodeName,
		"--timeout=90s",
	); err != nil {
		return FaultTarget{}, fmt.Errorf("waiting for rebound node %s: %w", cluster.NodeName, err)
	}

	refreshed := target
	refreshed.Machine.BootID = bootID
	refreshed.KubernetesNode = node
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
	if _, err := time.Parse(time.RFC3339Nano, receipt.ValidatedAt); err != nil {
		return errors.New("topology: stopped receipt validated_at is invalid")
	}
	observed := receipt.Targets[0]
	if observed.LogicalCluster != target.LogicalCluster || observed.MachineID != target.Machine.ID ||
		observed.State != string(ExpectedStateStopped) {
		return errors.New("topology: stopped receipt target identity mismatch")
	}
	return nil
}

func (t *Topology) resolveTarget(ctx context.Context, cluster Cluster) (FaultTarget, error) {
	machine, err := t.requireRunningMachine(ctx, cluster)
	if err != nil {
		return FaultTarget{}, err
	}
	if !machineIDPattern.MatchString(machine.ID) {
		return FaultTarget{}, fmt.Errorf("topology: machine %s returned an invalid immutable id", cluster.Name)
	}
	iface, err := t.primaryMachineInterface(ctx, machine)
	if err != nil {
		return FaultTarget{}, err
	}
	bootID, err := t.machineBootID(ctx, machine.Name)
	if err != nil {
		return FaultTarget{}, err
	}
	node, err := t.resolveKubernetesNode(ctx, cluster)
	if err != nil {
		return FaultTarget{}, err
	}

	return FaultTarget{
		LogicalCluster:  cluster.LogicalName,
		ResourceCluster: cluster.Name,
		DC:              cluster.DC,
		Zone:            cluster.Zone,
		Kubeconfig:      cluster.Kubeconfig,
		KubeContext:     cluster.KubeContext,
		Namespace:       Namespace,
		Machine: FaultTargetMachine{
			ID:        machine.ID,
			Name:      machine.Name,
			IPv4:      machine.IPv4,
			MAC:       iface.MAC,
			Interface: iface.Name,
			BootID:    bootID,
		},
		KubernetesNode: node,
	}, nil
}

// primaryMachineInterface resolves the first non-loopback interface that owns the machine IPv4.
func (t *Topology) primaryMachineInterface(ctx context.Context, machine orbMachine) (machineInterface, error) {
	commandCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	result, err := t.runner.Run(commandCtx, Command{
		Program: "orbctl",
		Args:    []string{"run", "-m", machine.Name, "ip", "-j", "address", "show"},
	})
	if err != nil {
		return machineInterface{}, fmt.Errorf("inspecting interfaces in machine %s: %w", machine.Name, err)
	}
	interfaces := []interfaceInspection{}
	if err := json.Unmarshal([]byte(result.Stdout), &interfaces); err != nil || len(interfaces) == 0 {
		return machineInterface{}, errors.New("topology: invalid machine interface inspection")
	}

	matches := make([]machineInterface, 0, 1)
	for _, candidate := range interfaces {
		if candidate.Name == "lo" || !ifaceNamePattern.MatchString(candidate.Name) ||
			!machineMACPattern.MatchString(candidate.MAC) {
			continue
		}
		for _, address := range candidate.AddressInfo {
			if address.Family == "inet" && address.Local == machine.IPv4 {
				matches = append(matches, machineInterface{
					Name:    candidate.Name,
					MAC:     candidate.MAC,
					Address: machine.IPv4,
				})
			}
		}
	}
	if len(matches) != 1 {
		return machineInterface{}, fmt.Errorf("topology: machine %s interface resolution is ambiguous", machine.Name)
	}
	return matches[0], nil
}

// machineBootID reads the per-boot kernel UUID that proves a stop/start transition.
func (t *Topology) machineBootID(ctx context.Context, name string) (string, error) {
	commandCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	result, err := t.runner.Run(commandCtx, Command{
		Program: "orbctl",
		Args:    []string{"run", "-m", name, "cat", "/proc/sys/kernel/random/boot_id"},
	})
	if err != nil {
		return "", fmt.Errorf("reading boot id in machine %s: %w", name, err)
	}
	bootID := strings.TrimSpace(result.Stdout)
	if !bootIDPattern.MatchString(bootID) {
		return "", errors.New("topology: invalid machine boot id")
	}
	return bootID, nil
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
		snapshot.Environment != TargetEnvironment || snapshot.Instance != t.config.Instance {
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
	if snapshot.PreviousToken != "" && !sha256DigestPattern.MatchString(snapshot.PreviousToken) {
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
	if target.Machine.Name != cluster.Name || !machineIDPattern.MatchString(target.Machine.ID) ||
		!machineMACPattern.MatchString(target.Machine.MAC) || !ifaceNamePattern.MatchString(target.Machine.Interface) ||
		!bootIDPattern.MatchString(target.Machine.BootID) ||
		!validPrivateIPv4(target.Machine.IPv4) {
		return fmt.Errorf("topology: target %s has invalid machine identity", cluster.LogicalName)
	}
	expectedNodeLabels := targetKubernetesLabels(cluster, t.config.Instance)
	if target.KubernetesNode.Name != cluster.NodeName || target.KubernetesNode.UID == "" ||
		len(target.KubernetesNode.UID) > 128 || !exactLabels(target.KubernetesNode.Labels, expectedNodeLabels) {
		return fmt.Errorf("topology: target %s has invalid kubernetes identity", cluster.LogicalName)
	}
	return nil
}

func validPrivateIPv4(address string) bool {
	ip := net.ParseIP(address)
	return ip != nil && ip.To4() != nil && ip.IsPrivate()
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

// validateTargetRuntime re-checks the immutable machine identity. Stopped targets
// are validated host-side only: a stopped machine must not expose any live
// in-guest execution handle, so no `orbctl run` happens in that state.
func (t *Topology) validateTargetRuntime(
	ctx context.Context,
	target FaultTarget,
	expectedState ExpectedTargetState,
) (orbMachine, error) {
	machine, err := t.inspectMachine(ctx, target.Machine.Name)
	if err != nil {
		return orbMachine{}, err
	}
	if machine.ID != target.Machine.ID {
		return orbMachine{}, errors.New("topology: immutable machine identity changed")
	}

	switch expectedState {
	case ExpectedStateRunning:
		if machine.State != string(ExpectedStateRunning) {
			return orbMachine{}, errors.New("topology: machine is not running")
		}
		if machine.IPv4 != target.Machine.IPv4 {
			return orbMachine{}, errors.New("topology: machine address changed")
		}
		iface, ifaceErr := t.primaryMachineInterface(ctx, machine)
		if ifaceErr != nil {
			return orbMachine{}, ifaceErr
		}
		if iface.MAC != target.Machine.MAC || iface.Name != target.Machine.Interface {
			return orbMachine{}, errors.New("topology: machine interface identity changed")
		}
		bootID, bootErr := t.machineBootID(ctx, machine.Name)
		if bootErr != nil {
			return orbMachine{}, bootErr
		}
		if bootID != target.Machine.BootID {
			return orbMachine{}, errors.New("topology: machine rebooted without a proved rebind")
		}
		cluster, clusterErr := t.config.Cluster(target.DC, target.Zone)
		if clusterErr != nil {
			return orbMachine{}, clusterErr
		}
		node, nodeErr := t.resolveKubernetesNode(ctx, cluster)
		if nodeErr != nil {
			return orbMachine{}, nodeErr
		}
		if node.UID != target.KubernetesNode.UID || !exactLabels(node.Labels, target.KubernetesNode.Labels) {
			return orbMachine{}, errors.New("topology: kubernetes node identity changed")
		}
	case ExpectedStateStopped:
		if machine.State != string(ExpectedStateStopped) {
			return orbMachine{}, errors.New("topology: machine is not safely stopped")
		}
	default:
		return orbMachine{}, errors.New("topology: unsupported expected machine state")
	}
	return machine, nil
}
