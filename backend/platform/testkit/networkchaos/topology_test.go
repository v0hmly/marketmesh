package networkchaos

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	testTopologyInstance = "mm38check"
	testDockerContext    = "orbstack"
	testDockerHost       = "unix:///private/tmp/mm36-docker.sock"
)

type topologyCommandResponse struct {
	output []byte
	err    error
}

type topologyCommandCall struct {
	stdin     []byte
	arguments []string
}

type fakeTopologyCommands struct {
	t         *testing.T
	responses []topologyCommandResponse
	calls     []topologyCommandCall
}

func (commands *fakeTopologyCommands) Run(
	_ context.Context,
	stdin []byte,
	arguments ...string,
) ([]byte, error) {
	commands.t.Helper()
	index := len(commands.calls)
	commands.calls = append(commands.calls, topologyCommandCall{
		stdin:     slices.Clone(stdin),
		arguments: slices.Clone(arguments),
	})
	if index >= len(commands.responses) {
		commands.t.Fatalf("unexpected topology command: %v", arguments)
	}
	return slices.Clone(commands.responses[index].output), commands.responses[index].err
}

type fakeTopologyValidator struct {
	mu     sync.Mutex
	calls  [][]string
	failAt int
}

type topologyDockerContextCall struct {
	name string
	host string
}

type fakeTopologyDockerContexts struct {
	calls  []topologyDockerContextCall
	failAt int
}

func (validator *fakeTopologyDockerContexts) Validate(
	_ context.Context,
	dockerContext string,
	dockerHost string,
) error {
	validator.calls = append(validator.calls, topologyDockerContextCall{
		name: dockerContext,
		host: dockerHost,
	})
	if validator.failAt == len(validator.calls) {
		return errors.New("Docker context endpoint changed")
	}
	return nil
}

func (validator *fakeTopologyValidator) ValidateRunning(
	_ context.Context,
	_ TopologyTargetSnapshot,
	logicalNames []string,
) (TopologyValidationReceipt, error) {
	validator.mu.Lock()
	defer validator.mu.Unlock()
	validator.calls = append(validator.calls, slices.Clone(logicalNames))
	if validator.failAt == len(validator.calls) {
		return TopologyValidationReceipt{}, errors.New("stale topology binding")
	}
	return TopologyValidationReceipt{Validated: true}, nil
}

func (validator *fakeTopologyValidator) recordedCalls() [][]string {
	validator.mu.Lock()
	defer validator.mu.Unlock()
	calls := make([][]string, len(validator.calls))
	for index, call := range validator.calls {
		calls[index] = slices.Clone(call)
	}
	return calls
}

func TestTopologyTargetClientUsesExactResolveAndValidateContract(t *testing.T) {
	t.Parallel()

	snapshot := validTopologySnapshot(t)
	receipt := validTopologyReceipt(t, snapshot, []string{"dc-a-internal", "dc-b-dmz"})
	commands := &fakeTopologyCommands{
		t: t,
		responses: []topologyCommandResponse{
			{output: topologyJSON(t, snapshot)},
			{output: topologyJSON(t, receipt)},
		},
	}
	dockerContexts := &fakeTopologyDockerContexts{}
	client := newTopologyTargetClient(testTopologyConfig(), commands, dockerContexts)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()

	resolved, err := client.Resolve(ctx)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if resolved.Token != snapshot.Token {
		t.Fatalf("Resolve() token = %q, want %q", resolved.Token, snapshot.Token)
	}
	resolved.Targets[0].Container.Labels["tampered"] = "true"
	if maps.Equal(resolved.Targets[0].Container.Labels, snapshot.Targets[0].Container.Labels) {
		t.Fatal("Resolve() returned mutable internal labels")
	}

	validated, err := client.ValidateRunning(
		ctx,
		snapshot,
		[]string{"dc-b-dmz", "dc-a-internal"},
	)
	if err != nil {
		t.Fatalf("ValidateRunning() error = %v", err)
	}
	if !validated.Validated || validated.ReceiptDigest != receipt.ReceiptDigest {
		t.Fatalf("ValidateRunning() receipt = %+v", validated)
	}

	wantResolve := []string{
		"--instance",
		testTopologyInstance,
		"--docker-context",
		testDockerContext,
		"targets",
		"resolve",
		"--consumer-task",
		TaskKey,
		"--consumer-run-id",
		testRunID,
	}
	if !slices.Equal(commands.calls[0].arguments, wantResolve) || len(commands.calls[0].stdin) != 0 {
		t.Fatalf("resolve call = %+v, want arguments %v and empty stdin", commands.calls[0], wantResolve)
	}
	wantValidate := []string{
		"--instance",
		testTopologyInstance,
		"--docker-context",
		testDockerContext,
		"targets",
		"validate",
		"--snapshot",
		"-",
		"--expected-state",
		topologyExpectedStateRunning,
		"--target",
		"dc-a-internal",
		"--target",
		"dc-b-dmz",
	}
	if !slices.Equal(commands.calls[1].arguments, wantValidate) {
		t.Fatalf("validate arguments = %v, want %v", commands.calls[1].arguments, wantValidate)
	}
	stdinSnapshot := TopologyTargetSnapshot{}
	if err := decodeTopologyDocument(commands.calls[1].stdin, &stdinSnapshot); err != nil {
		t.Fatalf("validate stdin = invalid snapshot: %v", err)
	}
	if stdinSnapshot.Token != snapshot.Token {
		t.Fatalf("validate stdin token = %q, want original %q", stdinSnapshot.Token, snapshot.Token)
	}
	if len(dockerContexts.calls) != 4 {
		t.Fatalf("Docker context validations = %d, want before and after each command", len(dockerContexts.calls))
	}
	for _, call := range dockerContexts.calls {
		if call.name != testDockerContext || call.host != testDockerHost {
			t.Fatalf("Docker context validation = %+v, want pinned config", call)
		}
	}
}

func TestTopologyTargetClientRejectsUnboundedAndTamperedDocuments(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		output func(*testing.T) []byte
		want   string
	}{
		{
			name: "token mismatch",
			output: func(t *testing.T) []byte {
				snapshot := validTopologySnapshot(t)
				snapshot.Token = "sha256:" + strings.Repeat("f", 64)
				return topologyJSON(t, snapshot)
			},
			want: "token mismatch",
		},
		{
			name: "unknown field",
			output: func(t *testing.T) []byte {
				encoded := topologyJSON(t, validTopologySnapshot(t))
				var document map[string]any
				if err := json.Unmarshal(encoded, &document); err != nil {
					t.Fatal(err)
				}
				document["replacement"] = true
				return topologyJSON(t, document)
			},
			want: "unknown field",
		},
		{
			name: "wrong namespace with recomputed token",
			output: func(t *testing.T) []byte {
				snapshot := validTopologySnapshot(t)
				snapshot.Targets[0].Namespace = "marketmesh-e2e"
				return topologyJSON(t, retokenTopologySnapshot(t, snapshot))
			},
			want: "identity is invalid",
		},
		{
			name: "foreign kubernetes labels with recomputed token",
			output: func(t *testing.T) []byte {
				snapshot := validTopologySnapshot(t)
				delete(snapshot.Targets[0].KubernetesNode.Labels, "marketmesh.dev/owner-task")
				return topologyJSON(t, retokenTopologySnapshot(t, snapshot))
			},
			want: "Kubernetes identity is invalid",
		},
		{
			name: "short sandbox id with recomputed token",
			output: func(t *testing.T) []byte {
				snapshot := validTopologySnapshot(t)
				snapshot.Targets[0].SandboxID = strings.Repeat("a", 12)
				return topologyJSON(t, retokenTopologySnapshot(t, snapshot))
			},
			want: "live binding is incomplete",
		},
		{
			name: "unexpected subnet with recomputed token",
			output: func(t *testing.T) []byte {
				snapshot := validTopologySnapshot(t)
				snapshot.Targets[0].Networks[0].Subnet = "10.36.0.0/24"
				return topologyJSON(t, retokenTopologySnapshot(t, snapshot))
			},
			want: "subnet is invalid",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			commands := &fakeTopologyCommands{
				t:         t,
				responses: []topologyCommandResponse{{output: test.output(t)}},
			}
			client := newTopologyTargetClient(
				testTopologyConfig(),
				commands,
				&fakeTopologyDockerContexts{},
			)
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()

			_, err := client.Resolve(ctx)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Resolve() error = %v, want %q", err, test.want)
			}
		})
	}

	commands := &fakeTopologyCommands{t: t}
	client := newTopologyTargetClient(
		testTopologyConfig(),
		commands,
		&fakeTopologyDockerContexts{},
	)
	if _, err := client.Resolve(t.Context()); err == nil || !strings.Contains(err.Error(), "deadline") {
		t.Fatalf("Resolve() error = %v, want bounded context rejection", err)
	}
}

func TestTopologyTargetClientRejectsDockerContextDrift(t *testing.T) {
	t.Parallel()

	commands := &fakeTopologyCommands{
		t: t,
		responses: []topologyCommandResponse{{
			output: topologyJSON(t, validTopologySnapshot(t)),
		}},
	}
	client := newTopologyTargetClient(
		testTopologyConfig(),
		commands,
		&fakeTopologyDockerContexts{failAt: 2},
	)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()

	_, err := client.Resolve(ctx)
	if err == nil || !strings.Contains(err.Error(), "endpoint changed") {
		t.Fatalf("Resolve() error = %v, want Docker context drift rejection", err)
	}
}

func TestExecTopologyDockerContextValidatorUsesExactInspection(t *testing.T) {
	t.Parallel()

	commands := &fakeDockerCommands{
		t: t,
		responses: []dockerCommandResponse{{
			result: dockerResult([]byte(`"` + testDockerHost + `"`)),
		}},
	}
	validator := execTopologyDockerContextValidator{commands: commands}
	if err := validator.Validate(t.Context(), testDockerContext, testDockerHost); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	want := []string{
		"context", "inspect", testDockerContext,
		"--format", "{{json .Endpoints.docker.Host}}",
	}
	if len(commands.calls) != 1 || !slices.Equal(commands.calls[0], want) {
		t.Fatalf("Docker calls = %v, want %v", commands.calls, want)
	}
}

func TestValidateTopologyDockerHostRejectsRemoteAndAmbiguousEndpoints(t *testing.T) {
	t.Parallel()

	for _, host := range []string{
		"",
		"tcp://127.0.0.1:2375",
		"unix://relative.sock",
		"unix:///",
		"unix:///private/tmp/../var/run/docker.sock",
		"unix:///private/tmp/docker.sock?context=other",
	} {
		if err := validateTopologyDockerHost(host); err == nil {
			t.Fatalf("validateTopologyDockerHost(%q) error = nil", host)
		}
	}
	if err := validateTopologyDockerHost(testDockerHost); err != nil {
		t.Fatalf("validateTopologyDockerHost(%q) error = %v", testDockerHost, err)
	}
}

func TestTopologyDriverDerivesSelectorsAndValidatesEveryOperation(t *testing.T) {
	t.Parallel()

	snapshot := validTopologySnapshot(t)
	validator := &fakeTopologyValidator{}
	commands := &fakeDockerCommands{
		t: t,
		responses: []dockerCommandResponse{
			{result: dockerResult(jsonOutput(t, []dockerQdisc{{Kind: "noqueue"}}))},
			{result: dockerResult(nil)},
			{result: dockerResult(jsonOutput(t, []dockerQdisc{{Kind: "netem"}}))},
			{result: dockerResult(nil)},
		},
	}
	driver := newTopologyDriver(validator, snapshot, commands)
	fault, err := driver.Fault(TopologyFaultSpec{
		Name:           "degrade-dc-a-internal",
		Kind:           KindDegradation,
		LogicalCluster: "dc-a-internal",
		LogicalNetwork: "dc-a-internal",
		Delay:          25 * time.Millisecond,
		LossPercent:    1,
		CapacityLoss:   1,
	})
	if err != nil {
		t.Fatalf("Fault() error = %v", err)
	}
	if fault.Container.ID != snapshot.Targets[1].Container.ID ||
		fault.Container.Name != snapshot.Targets[1].Container.Name || fault.Interface != "eth1" {
		t.Fatalf("Fault() = %+v, want exact dc-a-internal target", fault)
	}

	inspected, err := driver.Inspect(t.Context(), fault)
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	restore, err := driver.Apply(t.Context(), inspected, fault)
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if err := restore(t.Context()); err != nil {
		t.Fatalf("restore error = %v", err)
	}

	wantValidations := [][]string{
		{"dc-a-internal"},
		{"dc-a-internal"},
		{"dc-a-internal"},
	}
	if !slices.EqualFunc(validator.recordedCalls(), wantValidations, slices.Equal[[]string]) {
		t.Fatalf("validation calls = %v, want %v", validator.recordedCalls(), wantValidations)
	}
	if len(commands.calls) != 4 {
		t.Fatalf("docker calls = %v, want apply and cleanup only", commands.calls)
	}
}

func TestRunnerUsesOnlyPackageOwnedTopologyScopeValidator(t *testing.T) {
	t.Parallel()

	snapshot := validTopologySnapshot(t)
	validator := &fakeTopologyValidator{}
	commands := &fakeDockerCommands{
		t: t,
		responses: []dockerCommandResponse{
			{result: dockerResult(jsonOutput(t, []dockerQdisc{{Kind: "noqueue"}}))},
			{result: dockerResult(nil)},
			{result: dockerResult(jsonOutput(t, []dockerQdisc{{Kind: "netem"}}))},
			{result: dockerResult(nil)},
		},
	}
	driver := newTopologyDriver(validator, snapshot, commands)
	fault, err := driver.Fault(TopologyFaultSpec{
		Name:           "degrade-dc-a-internal",
		Kind:           KindDegradation,
		LogicalCluster: "dc-a-internal",
		LogicalNetwork: "dc-a-internal",
		Delay:          time.Millisecond,
		CapacityLoss:   1,
	})
	if err != nil {
		t.Fatalf("Fault() error = %v", err)
	}
	runner, err := newWithWaiter(
		testConfig(),
		driver,
		&fakeCapacity{values: []uint{3, 2, 3}},
		&fakeDiagnostics{},
		nil,
		fakeWaiter{},
	)
	if err != nil {
		t.Fatalf("newWithWaiter() error = %v", err)
	}

	err = runner.Run(t.Context(), Plan{
		Seed: 36,
		Steps: []Step{{
			Name:   "topology-scope",
			Hold:   time.Millisecond,
			Faults: []Fault{fault},
		}},
	})
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if len(commands.calls) != 4 {
		t.Fatalf("Docker calls = %v, want one apply/cleanup lifecycle", commands.calls)
	}
}

func TestTopologyDriverValidatesPeerOwnerAndFailsClosed(t *testing.T) {
	t.Parallel()

	snapshot := validTopologySnapshot(t)
	validator := &fakeTopologyValidator{}
	commands := &fakeDockerCommands{t: t}
	driver := newTopologyDriver(validator, snapshot, commands)
	fault, err := driver.Fault(TopologyFaultSpec{
		Name:           "partition-dc-a-from-dc-b",
		Kind:           KindPartition,
		LogicalCluster: "dc-a-internal",
		LogicalNetwork: "dc-a-internal",
		PeerNetworks:   []string{"dc-b-dmz"},
		CapacityLoss:   1,
	})
	if err != nil {
		t.Fatalf("Fault() error = %v", err)
	}
	if _, err := driver.Inspect(t.Context(), fault); err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	wantNames := []string{"dc-a-internal", "dc-b-dmz"}
	if calls := validator.recordedCalls(); len(calls) != 1 || !slices.Equal(calls[0], wantNames) {
		t.Fatalf("validation targets = %v, want %v", calls, wantNames)
	}

	staleValidator := &fakeTopologyValidator{failAt: 2}
	staleCommands := &fakeDockerCommands{t: t}
	staleDriver := newTopologyDriver(staleValidator, snapshot, staleCommands)
	inspected, err := staleDriver.Inspect(t.Context(), fault)
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if _, err := staleDriver.Apply(t.Context(), inspected, fault); err == nil ||
		!strings.Contains(err.Error(), "stale topology binding") {
		t.Fatalf("Apply() error = %v, want stale binding rejection", err)
	}
	if len(staleCommands.calls) != 0 {
		t.Fatalf("docker calls = %v, want zero after failed validation", staleCommands.calls)
	}
}

func TestTopologyDriverRefusesCleanupWhenBindingBecomesStale(t *testing.T) {
	t.Parallel()

	snapshot := validTopologySnapshot(t)
	validator := &fakeTopologyValidator{failAt: 3}
	commands := &fakeDockerCommands{
		t: t,
		responses: []dockerCommandResponse{
			{result: dockerResult(jsonOutput(t, []dockerQdisc{{Kind: "noqueue"}}))},
			{result: dockerResult(nil)},
		},
	}
	driver := newTopologyDriver(validator, snapshot, commands)
	fault, err := driver.Fault(TopologyFaultSpec{
		Name:           "degrade-before-stale-cleanup",
		Kind:           KindDegradation,
		LogicalCluster: "dc-b-internal",
		LogicalNetwork: "dc-b-internal",
		Delay:          time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Fault() error = %v", err)
	}
	inspected, err := driver.Inspect(t.Context(), fault)
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	restore, err := driver.Apply(t.Context(), inspected, fault)
	if err != nil {
		t.Fatalf("Apply() error = %v", err)
	}
	if err := restore(t.Context()); err == nil || !strings.Contains(err.Error(), "stale topology binding") {
		t.Fatalf("restore error = %v, want stale binding rejection", err)
	}
	if len(commands.calls) != 2 {
		t.Fatalf("docker calls = %v, want no cleanup mutation after stale binding", commands.calls)
	}
}

func testTopologyConfig() TopologyCLIConfig {
	return TopologyCLIConfig{
		Executable:    "/private/tmp/e2e-topology",
		Instance:      testTopologyInstance,
		DockerContext: testDockerContext,
		DockerHost:    testDockerHost,
		RunID:         testRunID,
	}
}

func validTopologySnapshot(t *testing.T) TopologyTargetSnapshot {
	t.Helper()

	specs := []struct {
		logical string
		dc      string
		zone    string
		id      string
	}{
		{logical: "dc-a-dmz", dc: "dc-a", zone: "dmz", id: "a"},
		{logical: "dc-a-internal", dc: "dc-a", zone: "internal", id: "b"},
		{logical: "dc-b-dmz", dc: "dc-b", zone: "dmz", id: "c"},
		{logical: "dc-b-internal", dc: "dc-b", zone: "internal", id: "d"},
	}
	snapshot := TopologyTargetSnapshot{
		APIVersion:    topologyTargetAPIVersion,
		Task:          topologyTaskKey,
		Environment:   topologyEnvironment,
		Instance:      testTopologyInstance,
		DockerContext: testDockerContext,
		ConsumerTask:  TaskKey,
		ConsumerRunID: testRunID,
		ResolvedAt:    "2026-09-01T12:00:00Z",
		Selector:      TopologyTargetSelector{},
		Targets:       make([]TopologyFaultTarget, 0, len(specs)),
	}
	for index, spec := range specs {
		resource := testTopologyInstance + "-" + spec.logical
		networkNames := []string{spec.logical}
		if spec.zone == "internal" {
			networkNames = []string{spec.dc + "-dmz", spec.logical}
		}
		networks := make([]TopologyFaultTargetNetwork, 0, len(networkNames))
		for networkIndex, logicalNetwork := range networkNames {
			networks = append(networks, validTopologyNetwork(
				logicalNetwork,
				logicalNetwork == spec.logical,
				index,
				networkIndex,
			))
		}
		snapshot.Targets = append(snapshot.Targets, TopologyFaultTarget{
			LogicalCluster:  spec.logical,
			ResourceCluster: resource,
			DC:              spec.dc,
			Zone:            spec.zone,
			Kubeconfig: "/private/tmp/marketmesh/.cache/mm28-topology/" +
				testTopologyInstance + "/kubeconfigs/" + spec.logical + ".yaml",
			KubeContext: "kind-" + resource,
			Namespace:   topologyNamespace,
			Container: TopologyFaultTargetContainer{
				ID:             strings.Repeat(spec.id, 64),
				Name:           resource + "-control-plane",
				ImageID:        "sha256:" + strings.Repeat("e", 64),
				ImageReference: topologyNodeImage,
				StartedAt:      "2026-09-01T11:00:00Z",
				Labels: map[string]string{
					topologyKindClusterLabel: resource,
				},
			},
			KubernetesNode: TopologyKubernetesNode{
				Name: resource + "-control-plane",
				UID:  "node-" + spec.logical,
				Labels: map[string]string{
					"marketmesh.dev/cluster":           spec.logical,
					"marketmesh.dev/topology-instance": testTopologyInstance,
					"marketmesh.dev/owner-task":        topologyTaskKey,
					"marketmesh.dev/dc":                spec.dc,
					"marketmesh.dev/zone":              spec.zone,
				},
			},
			SandboxID: strings.Repeat(spec.id, 64),
			NetNS:     "net:[12345]",
			Networks:  networks,
		})
	}
	token, err := topologySnapshotDigest(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Token = token
	return snapshot
}

func validTopologyNetwork(
	logicalName string,
	primary bool,
	targetIndex int,
	networkIndex int,
) TopologyFaultTargetNetwork {
	subnets := map[string]string{
		"dc-a-dmz":      "172.28.10.0/24",
		"dc-a-internal": "172.28.11.0/24",
		"dc-b-dmz":      "172.28.20.0/24",
		"dc-b-internal": "172.28.21.0/24",
	}
	networkIDs := map[string]string{
		"dc-a-dmz":      strings.Repeat("1", 64),
		"dc-a-internal": strings.Repeat("2", 64),
		"dc-b-dmz":      strings.Repeat("3", 64),
		"dc-b-internal": strings.Repeat("4", 64),
	}
	subnet := subnets[logicalName]
	base := strings.TrimSuffix(subnet, "0/24")
	address := base + string(rune('2'+targetIndex)) + "/24"
	mac := "02:42:ac:1c:" + string(rune('1'+targetIndex)) + string(rune('0'+networkIndex)) + ":02"
	interfaceName := "eth2"
	if primary {
		interfaceName = "eth1"
	}
	return TopologyFaultTargetNetwork{
		LogicalNetwork: logicalName,
		Primary:        primary,
		ID:             networkIDs[logicalName],
		Name:           testTopologyInstance + "-" + logicalName,
		Driver:         "bridge",
		Scope:          "local",
		Subnet:         subnet,
		Labels: map[string]string{
			topologyOwnerLabel:    topologyTaskKey,
			topologyInstanceLabel: testTopologyInstance,
		},
		Endpoint: TopologyTargetEndpoint{
			ID:        strings.Repeat(string(rune('5'+targetIndex+networkIndex)), 64),
			NetworkID: networkIDs[logicalName],
			Address:   address,
			Gateway:   base + "1",
			MAC:       mac,
		},
		Interface: TopologyTargetInterface{
			Name:    interfaceName,
			Index:   networkIndex + 2,
			Address: address,
			MAC:     mac,
		},
	}
}

func validTopologyReceipt(
	t *testing.T,
	snapshot TopologyTargetSnapshot,
	logicalNames []string,
) TopologyValidationReceipt {
	t.Helper()
	selected, err := selectTopologyTargets(snapshot, logicalNames)
	if err != nil {
		t.Fatal(err)
	}
	receipt := TopologyValidationReceipt{
		APIVersion:    topologyValidationAPIVersion,
		SnapshotToken: snapshot.Token,
		ExpectedState: topologyExpectedStateRunning,
		Validated:     true,
		ValidatedAt:   "2026-09-01T12:00:01Z",
		Targets:       make([]TopologyValidatedTarget, 0, len(selected)),
	}
	for _, target := range selected {
		networkIDs := make([]string, 0, len(target.Networks))
		for _, network := range target.Networks {
			networkIDs = append(networkIDs, network.ID)
		}
		receipt.Targets = append(receipt.Targets, TopologyValidatedTarget{
			LogicalCluster: target.LogicalCluster,
			ContainerID:    target.Container.ID,
			State:          topologyExpectedStateRunning,
			StartedAt:      target.Container.StartedAt,
			FinishedAt:     "0001-01-01T00:00:00Z",
			NetworkIDs:     networkIDs,
		})
	}
	digest, err := topologyReceiptDigest(receipt)
	if err != nil {
		t.Fatal(err)
	}
	receipt.ReceiptDigest = digest
	return receipt
}

func retokenTopologySnapshot(
	t *testing.T,
	snapshot TopologyTargetSnapshot,
) TopologyTargetSnapshot {
	t.Helper()
	token, err := topologySnapshotDigest(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.Token = token
	return snapshot
}

func topologyJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
