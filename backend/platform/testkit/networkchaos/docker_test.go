package networkchaos

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

type dockerCommandResponse struct {
	result dockerCommandResult
	err    error
}

type fakeDockerCommands struct {
	t         *testing.T
	responses []dockerCommandResponse
	calls     [][]string
}

func (commands *fakeDockerCommands) Run(
	_ context.Context,
	arguments ...string,
) (dockerCommandResult, error) {
	commands.t.Helper()
	commands.calls = append(commands.calls, append([]string{}, arguments...))
	if len(commands.responses) == 0 {
		commands.t.Fatalf("unexpected docker command: %v", arguments)
	}

	response := commands.responses[0]
	commands.responses = commands.responses[1:]
	return response.result, response.err
}

func TestDockerDriverInspectUsesExactMinimalMetadata(t *testing.T) {
	t.Parallel()

	snapshot := validSnapshot()
	commands := &fakeDockerCommands{
		t: t,
		responses: []dockerCommandResponse{
			{result: dockerResult(containerInspectOutput(t, snapshot))},
			{result: dockerResult(networkInspectOutput(t, snapshot.Network, snapshot.Container.ID))},
			{result: dockerResult(networkInspectOutput(t, snapshot.PeerNetworks[0], ""))},
			{result: dockerResult(interfaceOutput(t, snapshot.Interface, "10.36.1.2"))},
		},
	}
	driver := &DockerDriver{commands: commands}

	got, err := driver.Inspect(t.Context(), validPartitionFault("partition", 1))
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if got.Container.ID != snapshot.Container.ID || got.Network.Resource.ID != snapshot.Network.Resource.ID {
		t.Fatalf("Inspect() snapshot = %+v, want exact resources", got)
	}
	if len(got.PeerNetworks) != 1 || got.PeerNetworks[0].Resource.ID != snapshot.PeerNetworks[0].Resource.ID {
		t.Fatalf("Inspect() peer networks = %+v", got.PeerNetworks)
	}

	wantContainerInspect := []string{
		"container",
		"inspect",
		"--format",
		containerInspectFormat,
		testContainerRef.ID,
	}
	if !slices.Equal(commands.calls[0], wantContainerInspect) {
		t.Fatalf("container inspect = %v, want %v", commands.calls[0], wantContainerInspect)
	}
	if strings.Contains(containerInspectFormat, ".Env") {
		t.Fatalf("container inspect format reads environment: %q", containerInspectFormat)
	}
	wantInterfaceInspect := []string{
		"exec",
		testContainerRef.ID,
		"ip",
		"-j",
		"address",
		"show",
		"dev",
		"eth1",
	}
	if !slices.Equal(commands.calls[3], wantInterfaceInspect) {
		t.Fatalf("interface inspect = %v, want %v", commands.calls[3], wantInterfaceInspect)
	}
}

func TestDockerDriverAppliesAndIdempotentlyRestoresNetem(t *testing.T) {
	t.Parallel()

	fault := validDegradationFault()
	snapshot := validSnapshot()
	snapshot.PeerNetworks = nil
	commands := &fakeDockerCommands{
		t: t,
		responses: []dockerCommandResponse{
			{result: dockerResult(interfaceOutput(t, snapshot.Interface, "10.36.1.2"))},
			{result: dockerResult(jsonOutput(t, []dockerQdisc{{Kind: "noqueue"}}))},
			{result: dockerResult(nil)},
			{result: dockerResult(jsonOutput(t, []dockerQdisc{{Kind: "netem"}}))},
			{result: dockerResult(nil)},
			{result: dockerResult(jsonOutput(t, []dockerQdisc{{Kind: "noqueue"}}))},
		},
	}
	driver := &DockerDriver{commands: commands}

	restore, err := driver.Apply(t.Context(), snapshot, fault)
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if restore == nil {
		t.Fatal("Apply() returned nil restore")
	}
	if err := restore(t.Context()); err != nil {
		t.Fatalf("first restore error = %v", err)
	}
	if err := restore(t.Context()); err != nil {
		t.Fatalf("second restore error = %v", err)
	}

	wantApply := []string{
		"exec",
		testContainerRef.ID,
		"tc",
		"qdisc",
		"replace",
		"dev",
		"eth1",
		"root",
		"netem",
		"delay",
		"100000us",
		"20000us",
		"loss",
		"random",
		"2.500%",
		"rate",
		"1024kbit",
	}
	if !slices.Equal(commands.calls[2], wantApply) {
		t.Fatalf("netem apply = %v, want %v", commands.calls[2], wantApply)
	}
	wantDelete := []string{
		"exec",
		testContainerRef.ID,
		"tc",
		"qdisc",
		"del",
		"dev",
		"eth1",
		"root",
	}
	if !slices.Equal(commands.calls[4], wantDelete) {
		t.Fatalf("netem delete = %v, want %v", commands.calls[4], wantDelete)
	}
}

func TestDockerDriverReturnsCleanupAfterPartialPartition(t *testing.T) {
	t.Parallel()

	snapshot := validSnapshot()
	absent := dockerCommandResponse{
		result: dockerCommandResult{ExitCode: 1},
		err:    errors.New("rule absent"),
	}
	commands := &fakeDockerCommands{
		t: t,
		responses: []dockerCommandResponse{
			{result: dockerResult(interfaceOutput(t, snapshot.Interface, "10.36.1.2"))},
			absent,
			absent,
			{result: dockerResult(nil)},
			{result: dockerCommandResult{ExitCode: 2}, err: errors.New("insert failed")},
			{result: dockerResult(nil)},
			{result: dockerResult(nil)},
		},
	}
	driver := &DockerDriver{commands: commands}

	restore, err := driver.Apply(
		t.Context(),
		snapshot,
		validPartitionFault("partition", 1),
	)
	if err == nil || !strings.Contains(err.Error(), "applying partition rule") {
		t.Fatalf("Apply() error = %v, want partial partition failure", err)
	}
	if restore == nil {
		t.Fatal("Apply() returned nil cleanup after partial mutation")
	}
	if err := restore(t.Context()); err != nil {
		t.Fatalf("restore error = %v", err)
	}

	if got := partitionOperation(commands.calls[3]); got != "-I:OUTPUT" {
		t.Fatalf("first insert operation = %q", got)
	}
	if got := partitionOperation(commands.calls[4]); got != "-I:INPUT" {
		t.Fatalf("failed insert operation = %q", got)
	}
	if got := partitionOperation(commands.calls[5]); got != "-C:OUTPUT" {
		t.Fatalf("restore check operation = %q", got)
	}
	if got := partitionOperation(commands.calls[6]); got != "-D:OUTPUT" {
		t.Fatalf("restore delete operation = %q", got)
	}
	if !slices.Contains(commands.calls[6], "mm36:"+testRunID+":partition") {
		t.Fatalf("restore does not use exact run-scoped comment: %v", commands.calls[6])
	}
}

func TestDockerDriverRefusesForeignQdisc(t *testing.T) {
	t.Parallel()

	fault := validDegradationFault()
	snapshot := validSnapshot()
	snapshot.PeerNetworks = nil
	commands := &fakeDockerCommands{
		t: t,
		responses: []dockerCommandResponse{
			{result: dockerResult(interfaceOutput(t, snapshot.Interface, "10.36.1.2"))},
			{result: dockerResult(jsonOutput(t, []dockerQdisc{{Kind: "fq_codel"}}))},
		},
	}
	driver := &DockerDriver{commands: commands}

	restore, err := driver.Apply(t.Context(), snapshot, fault)
	if err == nil || !strings.Contains(err.Error(), "foreign qdisc") {
		t.Fatalf("Apply() error = %v, want foreign qdisc rejection", err)
	}
	if restore != nil {
		t.Fatal("Apply() returned cleanup without mutation")
	}
	if len(commands.calls) != 2 {
		t.Fatalf("docker calls = %v, want no mutation", commands.calls)
	}
}

func TestBoundedCommandBufferDiscardsExcess(t *testing.T) {
	t.Parallel()

	buffer := newBoundedCommandBuffer(4)
	content := []byte("unsafe-output")
	written, err := buffer.Write(content)
	if err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	if written != len(content) {
		t.Fatalf("Write() = %d, want %d", written, len(content))
	}
	if got := string(buffer.Bytes()); got != "unsa" {
		t.Fatalf("Bytes() = %q, want bounded prefix", got)
	}
	if !buffer.Exceeded() {
		t.Fatal("Exceeded() = false, want true")
	}
}

func validDegradationFault() Fault {
	return Fault{
		Name:          "latency-jitter-loss-bandwidth",
		Kind:          KindDegradation,
		Container:     testContainerRef,
		Network:       testNetworkRef,
		Interface:     "eth1",
		Delay:         100 * time.Millisecond,
		Jitter:        20 * time.Millisecond,
		LossPercent:   2.5,
		BandwidthKbit: 1024,
	}
}

func containerInspectOutput(t *testing.T, snapshot Snapshot) []byte {
	t.Helper()

	return jsonValues(
		t,
		snapshot.Container.ID,
		"/"+snapshot.Container.Name,
		snapshot.Container.Labels[TaskLabel],
		snapshot.Container.Labels[RunLabel],
		snapshot.Container.Labels[DisposableLabel],
		map[string]dockerEndpoint{
			snapshot.Network.Resource.Name: {NetworkID: snapshot.Network.Resource.ID},
		},
	)
}

func networkInspectOutput(t *testing.T, network Network, containerID string) []byte {
	t.Helper()

	containers := map[string]json.RawMessage{}
	if containerID != "" {
		containers[containerID] = json.RawMessage(`{}`)
	}
	ipam := make([]dockerIPAMConfig, 0, len(network.Prefixes))
	for _, prefix := range network.Prefixes {
		ipam = append(ipam, dockerIPAMConfig{Subnet: prefix.String()})
	}

	return jsonValues(
		t,
		network.Resource.ID,
		network.Resource.Name,
		network.Resource.Labels[TaskLabel],
		network.Resource.Labels[RunLabel],
		network.Resource.Labels[DisposableLabel],
		ipam,
		containers,
	)
}

func interfaceOutput(t *testing.T, interfaceName string, address string) []byte {
	t.Helper()

	return jsonOutput(t, []dockerInterface{
		{
			Name: interfaceName,
			Addresses: []dockerInterfaceAddress{
				{Local: address},
			},
		},
	})
}

func jsonValues(t *testing.T, values ...any) []byte {
	t.Helper()

	var output bytes.Buffer
	encoder := json.NewEncoder(&output)
	for _, value := range values {
		if err := encoder.Encode(value); err != nil {
			t.Fatalf("encoding docker fixture: %v", err)
		}
	}

	return output.Bytes()
}

func jsonOutput(t *testing.T, value any) []byte {
	t.Helper()

	output, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("encoding docker fixture: %v", err)
	}

	return output
}

func dockerResult(output []byte) dockerCommandResult {
	return dockerCommandResult{Output: output, ExitCode: 0}
}

func partitionOperation(arguments []string) string {
	operationIndex := slices.IndexFunc(arguments, func(argument string) bool {
		return argument == "-C" || argument == "-I" || argument == "-D"
	})
	if operationIndex < 0 || operationIndex+1 >= len(arguments) {
		return ""
	}

	return arguments[operationIndex] + ":" + arguments[operationIndex+1]
}
