package networkchaos

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	maxDockerOutputBytes   = 1 << 20
	containerInspectFormat = "{{json .Id}} {{json .Name}} " +
		"{{json (index .Config.Labels \"" + TaskLabel + "\")}} " +
		"{{json (index .Config.Labels \"" + RunLabel + "\")}} " +
		"{{json (index .Config.Labels \"" + DisposableLabel + "\")}} " +
		"{{json .NetworkSettings.Networks}}"
	networkInspectFormat = "{{json .Id}} {{json .Name}} " +
		"{{json (index .Labels \"" + TaskLabel + "\")}} " +
		"{{json (index .Labels \"" + RunLabel + "\")}} " +
		"{{json (index .Labels \"" + DisposableLabel + "\")}} " +
		"{{json .IPAM.Config}} {{json .Containers}}"
)

var errDockerOutputLimit = errors.New("networkchaos: docker command output exceeded limit")

// DockerDriver применяет faults только через аргументы docker CLI без shell.
// Inspect разрешает immutable IDs и проверяет принадлежность interface сети;
// окончательную проверку labels и private prefixes выполняет Runner.
type DockerDriver struct {
	commands dockerCommandRunner
}

// NewDockerDriver создаёт adapter для локального docker CLI.
func NewDockerDriver() *DockerDriver {
	return &DockerDriver{
		commands: execDockerCommandRunner{maxOutputBytes: maxDockerOutputBytes},
	}
}

// Inspect возвращает минимальный snapshot container, primary network и peer
// networks. Полный container inspect не запрашивается, чтобы не читать Env или
// другие потенциально чувствительные поля.
func (driver *DockerDriver) Inspect(ctx context.Context, fault Fault) (Snapshot, error) {
	if ctx == nil {
		return Snapshot{}, errors.New("networkchaos: context must not be nil")
	}
	if err := validateFault(fault); err != nil {
		return Snapshot{}, fmt.Errorf("networkchaos: invalid docker fault: %w", err)
	}

	container, endpoints, err := driver.inspectContainer(ctx, fault.Container)
	if err != nil {
		return Snapshot{}, err
	}
	primary, err := driver.inspectNetwork(ctx, fault.Network)
	if err != nil {
		return Snapshot{}, err
	}
	if err := validateMembership(fault.Container, fault.Network, endpoints, primary); err != nil {
		return Snapshot{}, err
	}

	peers := make([]Network, 0, len(fault.PeerNetworks))
	for _, peerRef := range fault.PeerNetworks {
		peer, inspectErr := driver.inspectNetwork(ctx, peerRef)
		if inspectErr != nil {
			return Snapshot{}, inspectErr
		}
		peers = append(peers, peer.Network)
	}

	if err := driver.assertInterface(
		ctx,
		fault.Container,
		fault.Interface,
		primary.Prefixes,
	); err != nil {
		return Snapshot{}, err
	}

	return Snapshot{
		Container:    container,
		Network:      primary.Network,
		PeerNetworks: peers,
		Interface:    fault.Interface,
	}, nil
}

// Apply повторно проверяет scope snapshot и interface непосредственно перед
// mutation. Возвращаемый cleanup безопасно повторяет удаление уже снятого fault.
func (driver *DockerDriver) Apply(
	ctx context.Context,
	snapshot Snapshot,
	fault Fault,
) (RestoreFunc, error) {
	if ctx == nil {
		return nil, errors.New("networkchaos: context must not be nil")
	}
	if err := validateFault(fault); err != nil {
		return nil, fmt.Errorf("networkchaos: invalid docker fault: %w", err)
	}

	runID := snapshot.Container.Labels[RunLabel]
	if !runIDPattern.MatchString(runID) {
		return nil, errors.New("networkchaos: snapshot has invalid run label")
	}
	if err := validateSnapshot(runID, fault, snapshot); err != nil {
		return nil, err
	}
	if err := driver.assertInterface(
		ctx,
		fault.Container,
		fault.Interface,
		snapshot.Network.Prefixes,
	); err != nil {
		return nil, err
	}

	switch fault.Kind {
	case KindPartition:
		return driver.applyPartition(ctx, runID, snapshot, fault)
	case KindDegradation:
		return driver.applyDegradation(ctx, fault)
	default:
		return nil, fmt.Errorf("networkchaos: unsupported docker fault kind %q", fault.Kind)
	}
}

func (driver *DockerDriver) inspectContainer(
	ctx context.Context,
	ref ResourceRef,
) (Resource, map[string]dockerEndpoint, error) {
	result, err := driver.commands.Run(
		ctx,
		"container",
		"inspect",
		"--format",
		containerInspectFormat,
		ref.ID,
	)
	if err != nil {
		return Resource{}, nil, fmt.Errorf("networkchaos: inspecting container: %w", err)
	}

	resource, endpoints, err := decodeContainerInspect(result.Output)
	if err != nil {
		return Resource{}, nil, fmt.Errorf("networkchaos: decoding container inspect: %w", err)
	}

	return resource, endpoints, nil
}

func (driver *DockerDriver) inspectNetwork(
	ctx context.Context,
	ref ResourceRef,
) (inspectedNetwork, error) {
	result, err := driver.commands.Run(
		ctx,
		"network",
		"inspect",
		"--format",
		networkInspectFormat,
		ref.ID,
	)
	if err != nil {
		return inspectedNetwork{}, fmt.Errorf("networkchaos: inspecting network: %w", err)
	}

	network, err := decodeNetworkInspect(result.Output)
	if err != nil {
		return inspectedNetwork{}, fmt.Errorf("networkchaos: decoding network inspect: %w", err)
	}

	return network, nil
}

func (driver *DockerDriver) assertInterface(
	ctx context.Context,
	container ResourceRef,
	interfaceName string,
	prefixes []netip.Prefix,
) error {
	result, err := driver.commands.Run(
		ctx,
		"exec",
		container.ID,
		"ip",
		"-j",
		"address",
		"show",
		"dev",
		interfaceName,
	)
	if err != nil {
		return fmt.Errorf("networkchaos: inspecting container interface: %w", err)
	}

	var interfaces []dockerInterface
	if err := decodeSingleJSON(result.Output, &interfaces); err != nil {
		return fmt.Errorf("networkchaos: decoding container interface: %w", err)
	}
	if len(interfaces) != 1 || interfaces[0].Name != interfaceName {
		return fmt.Errorf(
			"networkchaos: docker interface %q did not resolve exactly once",
			interfaceName,
		)
	}
	for _, address := range interfaces[0].Addresses {
		parsed, parseErr := netip.ParseAddr(address.Local)
		if parseErr != nil {
			continue
		}
		if slices.ContainsFunc(prefixes, func(prefix netip.Prefix) bool {
			return prefix.Contains(parsed)
		}) {
			return nil
		}
	}

	return fmt.Errorf(
		"networkchaos: interface %q has no address in the inspected primary network",
		interfaceName,
	)
}

func (driver *DockerDriver) applyDegradation(
	ctx context.Context,
	fault Fault,
) (RestoreFunc, error) {
	if err := driver.requireDefaultQdisc(ctx, fault.Container, fault.Interface); err != nil {
		return nil, err
	}

	restore := func(restoreCtx context.Context) error {
		return driver.restoreDegradation(restoreCtx, fault.Container, fault.Interface)
	}
	arguments := []string{
		"exec",
		fault.Container.ID,
		"tc",
		"qdisc",
		"replace",
		"dev",
		fault.Interface,
		"root",
		"netem",
	}
	if fault.Delay > 0 {
		arguments = append(arguments, "delay", formatMicroseconds(fault.Delay))
		if fault.Jitter > 0 {
			arguments = append(arguments, formatMicroseconds(fault.Jitter))
		}
	}
	if fault.LossPercent > 0 {
		arguments = append(
			arguments,
			"loss",
			"random",
			strconv.FormatFloat(fault.LossPercent, 'f', 3, 64)+"%",
		)
	}
	if fault.BandwidthKbit > 0 {
		arguments = append(
			arguments,
			"rate",
			strconv.FormatUint(uint64(fault.BandwidthKbit), 10)+"kbit",
		)
	}

	_, err := driver.commands.Run(ctx, arguments...)
	if err != nil {
		return restore, fmt.Errorf("networkchaos: applying netem: %w", err)
	}

	return restore, nil
}

func (driver *DockerDriver) requireDefaultQdisc(
	ctx context.Context,
	container ResourceRef,
	interfaceName string,
) error {
	states, err := driver.inspectQdisc(ctx, container, interfaceName)
	if err != nil {
		return err
	}
	for _, state := range states {
		if state.Kind != "noqueue" {
			return fmt.Errorf(
				"networkchaos: interface %q has foreign qdisc %q",
				interfaceName,
				state.Kind,
			)
		}
	}

	return nil
}

func (driver *DockerDriver) restoreDegradation(
	ctx context.Context,
	container ResourceRef,
	interfaceName string,
) error {
	states, err := driver.inspectQdisc(ctx, container, interfaceName)
	if err != nil {
		return err
	}
	hasNetem := false
	for _, state := range states {
		switch state.Kind {
		case "noqueue":
		case "netem":
			if hasNetem {
				return fmt.Errorf("networkchaos: interface %q has multiple netem qdiscs", interfaceName)
			}
			hasNetem = true
		default:
			return fmt.Errorf(
				"networkchaos: refusing to remove foreign qdisc %q from interface %q",
				state.Kind,
				interfaceName,
			)
		}
	}
	if !hasNetem {
		return nil
	}

	_, err = driver.commands.Run(
		ctx,
		"exec",
		container.ID,
		"tc",
		"qdisc",
		"del",
		"dev",
		interfaceName,
		"root",
	)
	if err != nil {
		return fmt.Errorf("networkchaos: removing netem: %w", err)
	}

	return nil
}

func (driver *DockerDriver) inspectQdisc(
	ctx context.Context,
	container ResourceRef,
	interfaceName string,
) ([]dockerQdisc, error) {
	result, err := driver.commands.Run(
		ctx,
		"exec",
		container.ID,
		"tc",
		"-j",
		"qdisc",
		"show",
		"dev",
		interfaceName,
	)
	if err != nil {
		return nil, fmt.Errorf("networkchaos: inspecting qdisc: %w", err)
	}

	states := []dockerQdisc{}
	if err := decodeSingleJSON(result.Output, &states); err != nil {
		return nil, fmt.Errorf("networkchaos: decoding qdisc: %w", err)
	}

	return states, nil
}

func (driver *DockerDriver) applyPartition(
	ctx context.Context,
	runID string,
	snapshot Snapshot,
	fault Fault,
) (RestoreFunc, error) {
	rules := partitionRules(runID, fault, snapshot.PeerNetworks)
	for _, rule := range rules {
		result, err := driver.commands.Run(ctx, rule.arguments(fault.Container.ID, "-C")...)
		if err == nil {
			return nil, fmt.Errorf("networkchaos: partition rule %q already exists", rule.description())
		}
		if result.ExitCode != 1 {
			return nil, fmt.Errorf("networkchaos: checking partition rule: %w", err)
		}
	}

	applied := make([]firewallRule, 0, len(rules))
	restore := func(restoreCtx context.Context) error {
		return driver.restorePartition(restoreCtx, fault.Container, applied)
	}
	for _, rule := range rules {
		_, err := driver.commands.Run(ctx, rule.arguments(fault.Container.ID, "-I")...)
		if err != nil {
			if len(applied) == 0 {
				return nil, fmt.Errorf("networkchaos: applying partition rule: %w", err)
			}
			return restore, fmt.Errorf("networkchaos: applying partition rule: %w", err)
		}
		applied = append(applied, rule)
	}

	return restore, nil
}

func (driver *DockerDriver) restorePartition(
	ctx context.Context,
	container ResourceRef,
	rules []firewallRule,
) error {
	restoreErrors := make([]error, 0)
	for _, rule := range slices.Backward(rules) {
		result, err := driver.commands.Run(ctx, rule.arguments(container.ID, "-C")...)
		if err != nil {
			if result.ExitCode == 1 {
				continue
			}
			restoreErrors = append(
				restoreErrors,
				fmt.Errorf("checking partition rule %q: %w", rule.description(), err),
			)
			continue
		}

		_, err = driver.commands.Run(ctx, rule.arguments(container.ID, "-D")...)
		if err != nil {
			restoreErrors = append(
				restoreErrors,
				fmt.Errorf("removing partition rule %q: %w", rule.description(), err),
			)
		}
	}

	return errors.Join(restoreErrors...)
}

func partitionRules(runID string, fault Fault, peers []Network) []firewallRule {
	comment := "mm36:" + runID + ":" + fault.Name
	rules := make([]firewallRule, 0)
	for _, peer := range peers {
		for _, prefix := range peer.Prefixes {
			binary := "iptables"
			if prefix.Addr().Is6() {
				binary = "ip6tables"
			}
			rules = append(
				rules,
				firewallRule{
					Binary:    binary,
					Chain:     "OUTPUT",
					Direction: "-d",
					Prefix:    prefix.String(),
					Comment:   comment,
				},
				firewallRule{
					Binary:    binary,
					Chain:     "INPUT",
					Direction: "-s",
					Prefix:    prefix.String(),
					Comment:   comment,
				},
			)
		}
	}

	return rules
}

type firewallRule struct {
	Binary    string
	Chain     string
	Direction string
	Prefix    string
	Comment   string
}

func (rule firewallRule) arguments(containerID string, operation string) []string {
	arguments := []string{
		"exec",
		containerID,
		rule.Binary,
		"-w",
		"5",
		operation,
		rule.Chain,
	}
	if operation == "-I" {
		arguments = append(arguments, "1")
	}
	arguments = append(
		arguments,
		rule.Direction,
		rule.Prefix,
		"-m",
		"comment",
		"--comment",
		rule.Comment,
		"-j",
		"DROP",
	)

	return arguments
}

func (rule firewallRule) description() string {
	return rule.Binary + ":" + rule.Chain + ":" + rule.Direction + ":" + rule.Prefix
}

func validateMembership(
	containerRef ResourceRef,
	networkRef ResourceRef,
	endpoints map[string]dockerEndpoint,
	network inspectedNetwork,
) error {
	endpoint, found := endpoints[networkRef.Name]
	if !found || endpoint.NetworkID != networkRef.ID {
		return errors.New("networkchaos: container is not attached to the exact primary network")
	}
	if _, found := network.containers[containerRef.ID]; !found {
		return errors.New("networkchaos: primary network does not contain the exact container")
	}

	return nil
}

func decodeContainerInspect(output []byte) (Resource, map[string]dockerEndpoint, error) {
	decoder := json.NewDecoder(bytes.NewReader(output))
	var id string
	var name string
	var taskLabel *string
	var runLabel *string
	var disposableLabel *string
	endpoints := map[string]dockerEndpoint{}
	if err := decoder.Decode(&id); err != nil {
		return Resource{}, nil, err
	}
	if err := decoder.Decode(&name); err != nil {
		return Resource{}, nil, err
	}
	if err := decoder.Decode(&taskLabel); err != nil {
		return Resource{}, nil, err
	}
	if err := decoder.Decode(&runLabel); err != nil {
		return Resource{}, nil, err
	}
	if err := decoder.Decode(&disposableLabel); err != nil {
		return Resource{}, nil, err
	}
	if err := decoder.Decode(&endpoints); err != nil {
		return Resource{}, nil, err
	}
	if err := requireJSONEOF(decoder); err != nil {
		return Resource{}, nil, err
	}
	if endpoints == nil {
		endpoints = map[string]dockerEndpoint{}
	}

	return Resource{
		ID:   id,
		Name: strings.TrimPrefix(name, "/"),
		Labels: map[string]string{
			TaskLabel:       stringValue(taskLabel),
			RunLabel:        stringValue(runLabel),
			DisposableLabel: stringValue(disposableLabel),
		},
	}, endpoints, nil
}

func decodeNetworkInspect(output []byte) (inspectedNetwork, error) {
	decoder := json.NewDecoder(bytes.NewReader(output))
	var id string
	var name string
	var taskLabel *string
	var runLabel *string
	var disposableLabel *string
	ipam := []dockerIPAMConfig{}
	containers := map[string]json.RawMessage{}
	if err := decoder.Decode(&id); err != nil {
		return inspectedNetwork{}, err
	}
	if err := decoder.Decode(&name); err != nil {
		return inspectedNetwork{}, err
	}
	if err := decoder.Decode(&taskLabel); err != nil {
		return inspectedNetwork{}, err
	}
	if err := decoder.Decode(&runLabel); err != nil {
		return inspectedNetwork{}, err
	}
	if err := decoder.Decode(&disposableLabel); err != nil {
		return inspectedNetwork{}, err
	}
	if err := decoder.Decode(&ipam); err != nil {
		return inspectedNetwork{}, err
	}
	if err := decoder.Decode(&containers); err != nil {
		return inspectedNetwork{}, err
	}
	if err := requireJSONEOF(decoder); err != nil {
		return inspectedNetwork{}, err
	}
	if containers == nil {
		containers = map[string]json.RawMessage{}
	}

	prefixes := make([]netip.Prefix, 0, len(ipam))
	for _, config := range ipam {
		prefix, err := netip.ParsePrefix(config.Subnet)
		if err != nil {
			return inspectedNetwork{}, fmt.Errorf("invalid Docker subnet: %w", err)
		}
		prefixes = append(prefixes, prefix)
	}

	return inspectedNetwork{
		Network: Network{
			Resource: Resource{
				ID:   id,
				Name: name,
				Labels: map[string]string{
					TaskLabel:       stringValue(taskLabel),
					RunLabel:        stringValue(runLabel),
					DisposableLabel: stringValue(disposableLabel),
				},
			},
			Prefixes: prefixes,
		},
		containers: containers,
	}, nil
}

func stringValue(value *string) string {
	if value == nil {
		return ""
	}

	return *value
}

func decodeSingleJSON(output []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(output))
	if err := decoder.Decode(destination); err != nil {
		return err
	}

	return requireJSONEOF(decoder)
}

func requireJSONEOF(decoder *json.Decoder) error {
	var extra any
	err := decoder.Decode(&extra)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err != nil {
		return err
	}

	return errors.New("unexpected trailing JSON value")
}

func formatMicroseconds(duration time.Duration) string {
	return strconv.FormatInt(duration.Microseconds(), 10) + "us"
}

type dockerEndpoint struct {
	NetworkID string `json:"NetworkID"`
}

type inspectedNetwork struct {
	Network
	containers map[string]json.RawMessage
}

type dockerIPAMConfig struct {
	Subnet string `json:"Subnet"`
}

type dockerInterface struct {
	Name      string                   `json:"ifname"`
	Addresses []dockerInterfaceAddress `json:"addr_info"`
}

type dockerInterfaceAddress struct {
	Local string `json:"local"`
}

type dockerQdisc struct {
	Kind string `json:"kind"`
}

type dockerCommandResult struct {
	Output   []byte
	ExitCode int
}

type dockerCommandRunner interface {
	Run(ctx context.Context, arguments ...string) (dockerCommandResult, error)
}

type execDockerCommandRunner struct {
	maxOutputBytes int
}

func (runner execDockerCommandRunner) Run(
	ctx context.Context,
	arguments ...string,
) (dockerCommandResult, error) {
	if ctx == nil {
		return dockerCommandResult{ExitCode: -1}, errors.New("networkchaos: context must not be nil")
	}
	if len(arguments) == 0 {
		return dockerCommandResult{ExitCode: -1}, errors.New("networkchaos: docker arguments are empty")
	}

	stdout := newBoundedCommandBuffer(runner.maxOutputBytes)
	stderr := newBoundedCommandBuffer(runner.maxOutputBytes)
	// #nosec G204 -- binary is fixed; every argument is either a package
	// constant or validated structured fault data, and no shell is involved.
	command := exec.CommandContext(ctx, "docker", arguments...)
	command.Stdout = stdout
	command.Stderr = stderr
	err := command.Run()
	result := dockerCommandResult{
		Output:   stdout.Bytes(),
		ExitCode: 0,
	}
	if stdout.Exceeded() || stderr.Exceeded() {
		return result, errDockerOutputLimit
	}
	if err == nil {
		return result, nil
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return dockerCommandResult{Output: result.Output, ExitCode: -1}, ctxErr
	}

	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		result.ExitCode = exitErr.ExitCode()
	} else {
		result.ExitCode = -1
	}

	return result, fmt.Errorf("networkchaos: docker command failed: %w", err)
}

type boundedCommandBuffer struct {
	mu       sync.Mutex
	content  []byte
	limit    int
	exceeded bool
}

func newBoundedCommandBuffer(limit int) *boundedCommandBuffer {
	return &boundedCommandBuffer{
		content: make([]byte, 0, min(limit, 4096)),
		limit:   limit,
	}
}

func (buffer *boundedCommandBuffer) Write(content []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()

	remaining := max(buffer.limit-len(buffer.content), 0)
	stored := min(remaining, len(content))
	buffer.content = append(buffer.content, content[:stored]...)
	if stored < len(content) {
		buffer.exceeded = true
	}

	return len(content), nil
}

func (buffer *boundedCommandBuffer) Bytes() []byte {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return append([]byte{}, buffer.content...)
}

func (buffer *boundedCommandBuffer) Exceeded() bool {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.exceeded
}
