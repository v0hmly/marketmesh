package topology

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

const (
	testContainerID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testImageID     = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testNetworkID   = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	testEndpointID  = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	testMAC         = "02:42:ac:1c:0a:02"
	testNodeUID     = "11111111-2222-3333-4444-555555555555"
	testNetNS       = "net:[4026533001]"
)

func TestResolveAndValidateTargets(t *testing.T) {
	t.Parallel()

	manager, runtime := newTargetFixture(t)
	snapshot, err := manager.ResolveTargets(t.Context(), TargetResolveRequest{
		ConsumerTask:  "MM-36",
		ConsumerRunID: "mm36-target-test",
		Selector:      TargetSelector{DC: "dc-a", Zone: "dmz"},
	})
	if err != nil {
		t.Fatalf("ResolveTargets() error = %v", err)
	}
	if snapshot.APIVersion != TargetAPIVersion || snapshot.Token == "" || len(snapshot.Targets) != 1 {
		t.Fatalf("snapshot is incomplete: %+v", snapshot)
	}
	target := snapshot.Targets[0]
	if target.Container.ID != testContainerID || target.Container.ImageID != testImageID ||
		target.KubernetesNode.UID != testNodeUID || target.NetNS != testNetNS {
		t.Fatalf("target immutable identity is incomplete: %+v", target)
	}
	if len(target.Networks) != 1 || target.Networks[0].ID != testNetworkID ||
		target.Networks[0].Endpoint.ID != testEndpointID || target.Networks[0].Interface.Name != "eth0" {
		t.Fatalf("target network identity is incomplete: %+v", target.Networks)
	}

	receipt, err := manager.ValidateTargets(t.Context(), snapshot, TargetValidateRequest{
		ExpectedState: ExpectedStateRunning,
		LogicalNames:  []string{"dc-a-dmz"},
	})
	if err != nil {
		t.Fatalf("ValidateTargets() error = %v", err)
	}
	if receipt.SnapshotToken != snapshot.Token || len(receipt.Targets) != 1 ||
		receipt.Targets[0].LogicalCluster != "dc-a-dmz" || receipt.Targets[0].State != "running" {
		t.Fatalf("validation receipt mismatch: %+v", receipt)
	}
	if err := runtime.assertReadOnly(); err != nil {
		t.Fatal(err)
	}
}

func TestValidateTargetsFailClosedOnRuntimeReplacement(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*fakeTargetRuntime)
	}{
		{
			name: "stale container id",
			mutate: func(runtime *fakeTargetRuntime) {
				runtime.containerID = strings.Repeat("1", 64)
			},
		},
		{
			name: "image replacement",
			mutate: func(runtime *fakeTargetRuntime) {
				runtime.imageID = "sha256:" + strings.Repeat("2", 64)
			},
		},
		{
			name: "container label replacement",
			mutate: func(runtime *fakeTargetRuntime) {
				runtime.clusterLabel = "foreign-cluster"
			},
		},
		{
			name: "network id replacement",
			mutate: func(runtime *fakeTargetRuntime) {
				runtime.networkID = strings.Repeat("3", 64)
			},
		},
		{
			name: "network label replacement",
			mutate: func(runtime *fakeTargetRuntime) {
				runtime.ownerTask = "MM-999"
			},
		},
		{
			name: "endpoint replacement",
			mutate: func(runtime *fakeTargetRuntime) {
				runtime.endpointID = strings.Repeat("4", 64)
			},
		},
		{
			name: "interface replacement",
			mutate: func(runtime *fakeTargetRuntime) {
				runtime.interfaceIndex = 9
			},
		},
		{
			name: "netns replacement",
			mutate: func(runtime *fakeTargetRuntime) {
				runtime.netNS = "net:[4026533999]"
			},
		},
		{
			name: "kubernetes uid replacement",
			mutate: func(runtime *fakeTargetRuntime) {
				runtime.nodeUID = "99999999-2222-3333-4444-555555555555"
			},
		},
		{
			name: "ambiguous interface",
			mutate: func(runtime *fakeTargetRuntime) {
				runtime.duplicateInterface = true
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			manager, runtime := newTargetFixture(t)
			snapshot := resolveFixtureSnapshot(t, manager)
			test.mutate(runtime)
			_, err := manager.ValidateTargets(t.Context(), snapshot, TargetValidateRequest{
				ExpectedState: ExpectedStateRunning,
			})
			if err == nil {
				t.Fatal("ValidateTargets() error = nil, want fail-closed replacement rejection")
			}
			if readOnlyErr := runtime.assertReadOnly(); readOnlyErr != nil {
				t.Fatal(readOnlyErr)
			}
		})
	}
}

func TestValidateTargetsStoppedDoesNotInspectLiveEndpoint(t *testing.T) {
	t.Parallel()

	manager, runtime := newTargetFixture(t)
	snapshot := resolveFixtureSnapshot(t, manager)
	runtime.state = "exited"
	runtime.running = false
	runtime.finishedAt = "2026-09-01T10:05:00Z"
	runtime.rejectExec = true
	runtime.commands = nil
	runtime.execCalls = 0

	receipt, err := manager.ValidateTargets(t.Context(), snapshot, TargetValidateRequest{
		ExpectedState: ExpectedStateStopped,
	})
	if err != nil {
		t.Fatalf("ValidateTargets(stopped) error = %v", err)
	}
	if len(receipt.Targets) != 1 || receipt.Targets[0].State != "exited" {
		t.Fatalf("stopped receipt mismatch: %+v", receipt)
	}
	if runtime.execCalls != 0 {
		t.Fatalf("stopped validation exec calls = %d, want 0", runtime.execCalls)
	}
	runtime.retainEndpoint = true
	if _, err := manager.ValidateTargets(t.Context(), snapshot, TargetValidateRequest{
		ExpectedState: ExpectedStateStopped,
	}); err == nil {
		t.Fatal("ValidateTargets(stopped) error = nil, want live endpoint rejection")
	}
	runtime.retainEndpoint = false
	runtime.retainSandboxKey = true
	if _, err := manager.ValidateTargets(t.Context(), snapshot, TargetValidateRequest{
		ExpectedState: ExpectedStateStopped,
	}); err == nil {
		t.Fatal("ValidateTargets(stopped) error = nil, want live sandbox key rejection")
	}
	if readOnlyErr := runtime.assertReadOnly(); readOnlyErr != nil {
		t.Fatal(readOnlyErr)
	}
	runtime.retainSandboxKey = false
	runtime.containerID = strings.Repeat("7", 64)
	if _, err := manager.ValidateTargets(t.Context(), snapshot, TargetValidateRequest{
		ExpectedState: ExpectedStateStopped,
	}); err == nil {
		t.Fatal("ValidateTargets(stopped) error = nil, want stale container id rejection")
	}
}

func TestRebindTargetProvesStoppedToStartedTransition(t *testing.T) {
	t.Parallel()

	manager, runtime := newTargetFixture(t)
	original := resolveFixtureSnapshot(t, manager)
	stoppedReceipt := stopFixtureTarget(t, manager, runtime, original)
	startFixtureTarget(runtime)

	result, err := manager.RebindTarget(t.Context(), TargetRebindInput{
		Snapshot:       original,
		StoppedReceipt: stoppedReceipt,
	}, "dc-a-dmz")
	if err != nil {
		t.Fatalf("RebindTarget() error = %v", err)
	}
	if result.APIVersion != TargetRebindAPIVersion || result.Snapshot.PreviousToken != original.Token ||
		result.Snapshot.Token == original.Token || result.Transition.FromToken != original.Token ||
		result.Transition.ToToken != result.Snapshot.Token || result.Transition.TransitionDigest == "" {
		t.Fatalf("rebind result is incomplete: %+v", result)
	}
	if result.Snapshot.Targets[0].Networks[0].Endpoint.ID != runtime.endpointID ||
		result.Snapshot.Targets[0].SandboxID != runtime.sandboxID ||
		result.Snapshot.Targets[0].Container.StartedAt != runtime.startedAt {
		t.Fatalf("rebound live binding mismatch: %+v", result.Snapshot.Targets[0])
	}
	if _, err := manager.ValidateTargets(t.Context(), original, TargetValidateRequest{
		ExpectedState: ExpectedStateRunning,
	}); err == nil {
		t.Fatal("ValidateTargets(old snapshot) error = nil, want stale binding rejection")
	}
	if _, err := manager.ValidateTargets(t.Context(), result.Snapshot, TargetValidateRequest{
		ExpectedState: ExpectedStateRunning,
	}); err != nil {
		t.Fatalf("ValidateTargets(rebound snapshot) error = %v", err)
	}

	retry, err := manager.RebindTarget(t.Context(), TargetRebindInput{
		Snapshot:       original,
		StoppedReceipt: stoppedReceipt,
	}, "dc-a-dmz")
	if err != nil {
		t.Fatalf("idempotent RebindTarget() error = %v", err)
	}
	if retry.Snapshot.Token != result.Snapshot.Token ||
		retry.Transition.TransitionDigest != result.Transition.TransitionDigest {
		t.Fatal("idempotent rebind produced a different token or transition digest")
	}
	runtime.finishedAt = "2026-09-01T10:07:00Z"
	runtime.startedAt = "2026-09-01T10:08:00Z"
	if _, err := manager.RebindTarget(t.Context(), TargetRebindInput{
		Snapshot:       original,
		StoppedReceipt: stoppedReceipt,
	}, "dc-a-dmz"); err == nil {
		t.Fatal("replayed stopped receipt error = nil after a second restart")
	}
	if _, err := manager.RebindTarget(t.Context(), TargetRebindInput{
		Snapshot:       result.Snapshot,
		StoppedReceipt: stoppedReceipt,
	}, "dc-a-dmz"); err == nil {
		t.Fatal("second RebindTarget() error = nil, want receipt replay rejection")
	}
}

func TestRebindTargetFailClosed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		resign bool
		mutate func(*fakeTargetRuntime, *TargetValidationReceipt)
	}{
		{
			name: "forged stopped receipt",
			mutate: func(_ *fakeTargetRuntime, receipt *TargetValidationReceipt) {
				receipt.ReceiptDigest = "sha256:" + strings.Repeat("0", 64)
			},
		},
		{
			name:   "cross target receipt",
			resign: true,
			mutate: func(_ *fakeTargetRuntime, receipt *TargetValidationReceipt) {
				receipt.Targets[0].LogicalCluster = "dc-b-dmz"
			},
		},
		{
			name: "no new generation",
			mutate: func(runtime *fakeTargetRuntime, _ *TargetValidationReceipt) {
				runtime.startedAt = "2026-09-01T10:00:00Z"
			},
		},
		{
			name: "image replacement",
			mutate: func(runtime *fakeTargetRuntime, _ *TargetValidationReceipt) {
				runtime.imageID = "sha256:" + strings.Repeat("1", 64)
			},
		},
		{
			name: "network replacement",
			mutate: func(runtime *fakeTargetRuntime, _ *TargetValidationReceipt) {
				runtime.networkID = strings.Repeat("2", 64)
			},
		},
		{
			name: "ip replacement",
			mutate: func(runtime *fakeTargetRuntime, _ *TargetValidationReceipt) {
				runtime.ipAddress = "172.28.10.3"
			},
		},
		{
			name: "extra network",
			mutate: func(runtime *fakeTargetRuntime, _ *TargetValidationReceipt) {
				runtime.extraNetwork = true
			},
		},
		{
			name: "kubernetes not ready",
			mutate: func(runtime *fakeTargetRuntime, _ *TargetValidationReceipt) {
				runtime.kubernetesErr = errors.New("not ready")
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			manager, runtime := newTargetFixture(t)
			original := resolveFixtureSnapshot(t, manager)
			stoppedReceipt := stopFixtureTarget(t, manager, runtime, original)
			startFixtureTarget(runtime)
			test.mutate(runtime, &stoppedReceipt)
			if test.resign {
				digest, digestErr := targetValidationReceiptDigest(stoppedReceipt)
				if digestErr != nil {
					t.Fatalf("targetValidationReceiptDigest() error = %v", digestErr)
				}
				stoppedReceipt.ReceiptDigest = digest
			}
			if _, err := manager.RebindTarget(t.Context(), TargetRebindInput{
				Snapshot:       original,
				StoppedReceipt: stoppedReceipt,
			}, "dc-a-dmz"); err == nil {
				t.Fatal("RebindTarget() error = nil, want fail-closed transition rejection")
			}
			if readOnlyErr := runtime.assertReadOnly(); readOnlyErr != nil {
				t.Fatal(readOnlyErr)
			}
		})
	}
}

func stopFixtureTarget(
	t *testing.T,
	manager *Topology,
	runtime *fakeTargetRuntime,
	snapshot TargetSnapshot,
) TargetValidationReceipt {
	t.Helper()
	runtime.state = "exited"
	runtime.running = false
	runtime.finishedAt = "2026-09-01T10:05:00Z"
	receipt, err := manager.ValidateTargets(t.Context(), snapshot, TargetValidateRequest{
		ExpectedState: ExpectedStateStopped,
		LogicalNames:  []string{"dc-a-dmz"},
	})
	if err != nil {
		t.Fatalf("ValidateTargets(stopped) error = %v", err)
	}
	return receipt
}

func startFixtureTarget(runtime *fakeTargetRuntime) {
	runtime.state = "running"
	runtime.running = true
	runtime.startedAt = "2026-09-01T10:06:00Z"
	runtime.endpointID = strings.Repeat("6", 64)
	runtime.mac = "02:42:ac:1c:0a:03"
	runtime.sandboxID = strings.Repeat("7", 64)
	runtime.netNS = "net:[4026533002]"
	runtime.interfaceIndex = 4
}

func TestValidateTargetsRejectsSnapshotTamperingAndSelectionErrors(t *testing.T) {
	t.Parallel()

	manager, _ := newTargetFixture(t)
	snapshot := resolveFixtureSnapshot(t, manager)

	tampered := snapshot
	tampered.Targets = append([]FaultTarget{}, snapshot.Targets...)
	tampered.Targets[0].Container.ID = strings.Repeat("f", 64)
	if _, err := manager.ValidateTargets(t.Context(), tampered, TargetValidateRequest{
		ExpectedState: ExpectedStateRunning,
	}); err == nil {
		t.Fatal("ValidateTargets() error = nil, want token mismatch")
	}

	foreign := snapshot
	foreign.Instance = "foreign-instance"
	if _, err := manager.ValidateTargets(t.Context(), foreign, TargetValidateRequest{
		ExpectedState: ExpectedStateRunning,
	}); err == nil {
		t.Fatal("ValidateTargets() error = nil, want foreign instance rejection")
	}

	if _, err := manager.ValidateTargets(t.Context(), snapshot, TargetValidateRequest{
		ExpectedState: ExpectedStateRunning,
		LogicalNames:  []string{"dc-b-dmz"},
	}); err == nil {
		t.Fatal("ValidateTargets() error = nil, want absent target rejection")
	}
	if _, err := manager.ValidateTargets(t.Context(), snapshot, TargetValidateRequest{
		ExpectedState: ExpectedStateRunning,
		LogicalNames:  []string{"dc-a-dmz", "dc-a-dmz"},
	}); err == nil {
		t.Fatal("ValidateTargets() error = nil, want duplicate target rejection")
	}
}

func TestTargetSelectorCardinality(t *testing.T) {
	t.Parallel()

	config, err := NewConfig("/workspace/marketmesh", "mm38-test", "default")
	if err != nil {
		t.Fatalf("NewConfig() error = %v", err)
	}
	manager := New(config, nil, nil)
	tests := []struct {
		name     string
		selector TargetSelector
		expected int
	}{
		{name: "all", expected: 4},
		{name: "dc", selector: TargetSelector{DC: "dc-a"}, expected: 2},
		{name: "zone", selector: TargetSelector{DC: "dc-b", Zone: "internal"}, expected: 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			clusters, selectErr := manager.selectedClusters(test.selector)
			if selectErr != nil {
				t.Fatalf("selectedClusters() error = %v", selectErr)
			}
			if len(clusters) != test.expected {
				t.Fatalf("len(clusters) = %d, want %d", len(clusters), test.expected)
			}
		})
	}
	if _, err := manager.selectedClusters(TargetSelector{Zone: "dmz"}); err == nil {
		t.Fatal("selectedClusters() error = nil, want zone without dc rejection")
	}
}

func TestDecodeTargetSnapshotStrict(t *testing.T) {
	t.Parallel()

	manager, _ := newTargetFixture(t)
	snapshot := resolveFixtureSnapshot(t, manager)
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if _, err := DecodeTargetSnapshot(bytes.NewReader(encoded)); err != nil {
		t.Fatalf("DecodeTargetSnapshot() error = %v", err)
	}

	tests := []struct {
		name string
		data []byte
	}{
		{name: "unknown field", data: []byte(`{"api_version":"x","unknown":true}`)},
		{name: "duplicate field", data: []byte(`{"api_version":"x","api_version":"y"}`)},
		{name: "trailing document", data: append(append([]byte{}, encoded...), []byte(` {}`)...)},
		{name: "oversized", data: bytes.Repeat([]byte{'x'}, targetSnapshotInputLimit+1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := DecodeTargetSnapshot(bytes.NewReader(test.data)); err == nil {
				t.Fatal("DecodeTargetSnapshot() error = nil, want strict rejection")
			}
		})
	}
}

func TestDecodeTargetRebindInputStrict(t *testing.T) {
	t.Parallel()

	manager, runtime := newTargetFixture(t)
	snapshot := resolveFixtureSnapshot(t, manager)
	receipt := stopFixtureTarget(t, manager, runtime, snapshot)
	encoded, err := json.Marshal(TargetRebindInput{
		Snapshot:       snapshot,
		StoppedReceipt: receipt,
	})
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	if _, err := DecodeTargetRebindInput(bytes.NewReader(encoded)); err != nil {
		t.Fatalf("DecodeTargetRebindInput() error = %v", err)
	}
	for _, data := range [][]byte{
		[]byte(`{"snapshot":{},"stopped_receipt":{},"unknown":true}`),
		append(append([]byte{}, encoded...), []byte(` {}`)...),
	} {
		if _, err := DecodeTargetRebindInput(bytes.NewReader(data)); err == nil {
			t.Fatal("DecodeTargetRebindInput() error = nil, want strict rejection")
		}
	}
}

func resolveFixtureSnapshot(t *testing.T, manager *Topology) TargetSnapshot {
	t.Helper()
	snapshot, err := manager.ResolveTargets(t.Context(), TargetResolveRequest{
		ConsumerTask:  "MM-35",
		ConsumerRunID: "mm35-target-test",
		Selector:      TargetSelector{DC: "dc-a", Zone: "dmz"},
	})
	if err != nil {
		t.Fatalf("ResolveTargets() error = %v", err)
	}
	return snapshot
}

func newTargetFixture(t *testing.T) (*Topology, *fakeTargetRuntime) {
	t.Helper()
	config, err := NewConfig("/workspace/marketmesh", "mm38-test", "default")
	if err != nil {
		t.Fatalf("NewConfig() error = %v", err)
	}
	cluster, err := config.Cluster("dc-a", "dmz")
	if err != nil {
		t.Fatalf("Cluster() error = %v", err)
	}
	runtime := &fakeTargetRuntime{
		config:         config,
		cluster:        cluster,
		containerID:    testContainerID,
		imageID:        testImageID,
		clusterLabel:   cluster.Name,
		networkID:      testNetworkID,
		endpointID:     testEndpointID,
		ownerTask:      TaskKey,
		instance:       config.Instance,
		state:          "running",
		running:        true,
		interfaceIndex: 2,
		mac:            testMAC,
		ipAddress:      "172.28.10.2",
		gateway:        "172.28.10.1",
		sandboxID:      strings.Repeat("e", 64),
		netNS:          testNetNS,
		nodeUID:        testNodeUID,
		startedAt:      "2026-09-01T10:00:00Z",
		finishedAt:     "0001-01-01T00:00:00Z",
		commands:       []Command{},
	}
	manager := New(config, runtime, nil)
	manager.now = func() time.Time {
		return time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
	}
	return manager, runtime
}

type fakeTargetRuntime struct {
	config             Config
	cluster            Cluster
	containerID        string
	imageID            string
	clusterLabel       string
	networkID          string
	endpointID         string
	ownerTask          string
	instance           string
	state              string
	running            bool
	interfaceIndex     int
	mac                string
	ipAddress          string
	gateway            string
	sandboxID          string
	netNS              string
	nodeUID            string
	startedAt          string
	finishedAt         string
	duplicateInterface bool
	extraNetwork       bool
	retainEndpoint     bool
	retainSandboxKey   bool
	rejectExec         bool
	kubernetesErr      error
	execCalls          int
	commands           []Command
}

func (r *fakeTargetRuntime) Run(_ context.Context, command Command) (Result, error) {
	r.commands = append(r.commands, command)
	if command.Program == r.config.KubectlPath {
		if r.kubernetesErr != nil {
			return Result{}, r.kubernetesErr
		}
		return r.kubernetesNodeResult()
	}
	if command.Program != "docker" || len(command.Args) < 4 || command.Args[0] != "--context" ||
		command.Args[1] != r.config.DockerContext {
		return Result{}, fmt.Errorf("unexpected command: %+v", command)
	}
	args := command.Args[2:]
	switch {
	case len(args) == 3 && args[0] == "container" && args[1] == "inspect":
		return r.containerResult(args[2])
	case len(args) == 3 && args[0] == "network" && args[1] == "inspect":
		return r.networkResult(args[2])
	case len(args) >= 4 && args[0] == "exec":
		r.execCalls++
		if r.rejectExec {
			return Result{}, errors.New("exec rejected")
		}
		if args[2] == "readlink" {
			return Result{Stdout: r.netNS + "\n"}, nil
		}
		if args[2] == "ip" {
			return r.interfaceResult()
		}
	}
	return Result{}, fmt.Errorf("unexpected docker command: %v", args)
}

func (r *fakeTargetRuntime) containerResult(selector string) (Result, error) {
	if selector != r.cluster.NodeName && selector != testContainerID && selector != r.containerID {
		return Result{}, errors.New("container not found")
	}
	networks := map[string]any{
		r.cluster.NetworkName: map[string]any{
			"NetworkID": r.networkID,
		},
	}
	sandboxID := ""
	sandboxKey := ""
	if r.running || r.retainEndpoint {
		sandboxID = r.sandboxID
		networks[r.cluster.NetworkName] = map[string]any{
			"NetworkID":   r.networkID,
			"EndpointID":  r.endpointID,
			"Gateway":     r.gateway,
			"IPAddress":   r.ipAddress,
			"IPPrefixLen": 24,
			"MacAddress":  r.mac,
		}
	}
	if r.running || r.retainSandboxKey {
		sandboxKey = "/var/run/docker/netns/" + r.sandboxID[:12]
	}
	if r.extraNetwork {
		networks["foreign-network"] = map[string]any{
			"NetworkID": strings.Repeat("9", 64),
		}
	}
	payload := []map[string]any{{
		"Id":    r.containerID,
		"Name":  "/" + r.cluster.NodeName,
		"Image": r.imageID,
		"Config": map[string]any{
			"Image":  NodeImage,
			"Labels": map[string]string{clusterLabelKey: r.clusterLabel},
		},
		"State": map[string]any{
			"Status":     r.state,
			"Running":    r.running,
			"Paused":     false,
			"Restarting": false,
			"Dead":       false,
			"StartedAt":  r.startedAt,
			"FinishedAt": r.finishedAt,
		},
		"NetworkSettings": map[string]any{
			"SandboxID":  sandboxID,
			"SandboxKey": sandboxKey,
			"Networks":   networks,
		},
	}}
	return marshalResult(payload)
}

func (r *fakeTargetRuntime) networkResult(selector string) (Result, error) {
	if selector != r.cluster.NetworkName && selector != testNetworkID && selector != r.networkID {
		return Result{}, errors.New("network not found")
	}
	containers := map[string]any{}
	if r.running || r.retainEndpoint {
		containers[r.containerID] = map[string]string{
			"Name":        r.cluster.NodeName,
			"EndpointID":  r.endpointID,
			"MacAddress":  r.mac,
			"IPv4Address": r.ipAddress + "/24",
		}
	}
	payload := []map[string]any{{
		"Id":     r.networkID,
		"Name":   r.cluster.NetworkName,
		"Driver": "bridge",
		"Scope":  "local",
		"Labels": map[string]string{
			ownerLabelKey:    r.ownerTask,
			instanceLabelKey: r.instance,
		},
		"IPAM": map[string]any{
			"Config": []map[string]string{{"Subnet": r.cluster.DockerSubnet}},
		},
		"Containers": containers,
	}}
	return marshalResult(payload)
}

func (r *fakeTargetRuntime) interfaceResult() (Result, error) {
	interfaces := []map[string]any{{
		"ifindex": r.interfaceIndex,
		"ifname":  "eth0",
		"address": r.mac,
		"addr_info": []map[string]any{{
			"family":    "inet",
			"local":     r.ipAddress,
			"prefixlen": 24,
		}},
	}}
	if r.duplicateInterface {
		interfaces = append(interfaces, map[string]any{
			"ifindex": 3,
			"ifname":  "eth1",
			"address": r.mac,
			"addr_info": []map[string]any{{
				"family":    "inet",
				"local":     r.ipAddress,
				"prefixlen": 24,
			}},
		})
	}
	return marshalResult(interfaces)
}

func (r *fakeTargetRuntime) kubernetesNodeResult() (Result, error) {
	payload := map[string]any{
		"metadata": map[string]any{
			"name":   r.cluster.NodeName,
			"uid":    r.nodeUID,
			"labels": targetKubernetesLabels(r.cluster, r.config.Instance),
		},
	}
	return marshalResult(payload)
}

func (r *fakeTargetRuntime) assertReadOnly() error {
	for _, command := range r.commands {
		if command.Program == r.config.KubectlPath {
			if !containsSequence(command.Args, []string{"get", "node"}) {
				return fmt.Errorf("unexpected kubectl mutation candidate: %v", command.Args)
			}
			continue
		}
		args := command.Args[2:]
		isInspect := len(args) >= 2 && ((args[0] == "container" && args[1] == "inspect") ||
			(args[0] == "network" && args[1] == "inspect"))
		isReadOnlyExec := len(args) >= 3 && args[0] == "exec" && (args[2] == "ip" || args[2] == "readlink")
		if !isInspect && !isReadOnlyExec {
			return fmt.Errorf("unexpected destructive command: %v", args)
		}
	}
	return nil
}

func containsSequence(values, sequence []string) bool {
	for index := 0; index+len(sequence) <= len(values); index++ {
		if slices.Equal(values[index:index+len(sequence)], sequence) {
			return true
		}
	}
	return false
}

func marshalResult(value any) (Result, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return Result{}, err
	}
	return Result{Stdout: string(encoded)}, nil
}
