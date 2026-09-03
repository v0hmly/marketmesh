package topology

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

const (
	testMachineID = "01M1JYNYGQG0SPZ1HYQWB1WSE1"
	testIPv4      = "192.168.139.10"
	testMAC       = "52:54:00:12:34:56"
	testIface     = "eth0"
	testBootID    = "11111111-2222-4333-8444-555555555555"
	testNodeUID   = "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	testVMUser    = "operator"
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
	if target.Machine.ID != testMachineID || target.Machine.IPv4 != testIPv4 ||
		target.Machine.MAC != testMAC || target.Machine.Interface != testIface ||
		target.Machine.BootID != testBootID || target.KubernetesNode.UID != testNodeUID {
		t.Fatalf("target immutable identity is incomplete: %+v", target)
	}

	receipt, err := manager.ValidateTargets(t.Context(), snapshot, TargetValidateRequest{
		ExpectedState: ExpectedStateRunning,
		LogicalNames:  []string{"dc-a-dmz"},
	})
	if err != nil {
		t.Fatalf("ValidateTargets() error = %v", err)
	}
	if receipt.SnapshotToken != snapshot.Token || len(receipt.Targets) != 1 ||
		receipt.Targets[0].LogicalCluster != "dc-a-dmz" || receipt.Targets[0].State != "running" ||
		receipt.Targets[0].MachineID != testMachineID {
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
			name: "stale machine id",
			mutate: func(runtime *fakeTargetRuntime) {
				runtime.machineID = "01M1JYNYGQG0SPZ1HYQWB1WSE2"
			},
		},
		{
			name: "machine address replacement",
			mutate: func(runtime *fakeTargetRuntime) {
				runtime.ipv4 = "192.168.139.99"
			},
		},
		{
			name: "interface mac replacement",
			mutate: func(runtime *fakeTargetRuntime) {
				runtime.mac = "52:54:00:65:43:21"
			},
		},
		{
			name: "interface name replacement",
			mutate: func(runtime *fakeTargetRuntime) {
				runtime.ifname = "enp0s9"
			},
		},
		{
			name: "unexpected reboot",
			mutate: func(runtime *fakeTargetRuntime) {
				runtime.bootID = "99999999-2222-4333-8444-555555555555"
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

func TestValidateTargetsStoppedDoesNotEnterMachine(t *testing.T) {
	t.Parallel()

	manager, runtime := newTargetFixture(t)
	snapshot := resolveFixtureSnapshot(t, manager)
	runtime.state = "stopped"
	runtime.commands = nil
	runtime.runCalls = 0

	receipt, err := manager.ValidateTargets(t.Context(), snapshot, TargetValidateRequest{
		ExpectedState: ExpectedStateStopped,
	})
	if err != nil {
		t.Fatalf("ValidateTargets(stopped) error = %v", err)
	}
	if len(receipt.Targets) != 1 || receipt.Targets[0].State != "stopped" {
		t.Fatalf("stopped receipt mismatch: %+v", receipt)
	}
	if runtime.runCalls != 0 {
		t.Fatalf("stopped validation in-guest run calls = %d, want 0", runtime.runCalls)
	}

	runtime.state = "running"
	if _, err := manager.ValidateTargets(t.Context(), snapshot, TargetValidateRequest{
		ExpectedState: ExpectedStateStopped,
	}); err == nil {
		t.Fatal("ValidateTargets(stopped) error = nil, want running machine rejection")
	}
	runtime.state = "stopped"
	runtime.machineID = "01M1JYNYGQG0SPZ1HYQWB1WSE2"
	if _, err := manager.ValidateTargets(t.Context(), snapshot, TargetValidateRequest{
		ExpectedState: ExpectedStateStopped,
	}); err == nil {
		t.Fatal("ValidateTargets(stopped) error = nil, want stale machine id rejection")
	}
	if readOnlyErr := runtime.assertReadOnly(); readOnlyErr != nil {
		t.Fatal(readOnlyErr)
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
	if result.Snapshot.Targets[0].Machine.ID != original.Targets[0].Machine.ID ||
		result.Snapshot.Targets[0].Machine.MAC != original.Targets[0].Machine.MAC ||
		result.Snapshot.Targets[0].Machine.IPv4 != original.Targets[0].Machine.IPv4 ||
		result.Snapshot.Targets[0].Machine.BootID != runtime.bootID {
		t.Fatalf("rebound machine binding mismatch: %+v", result.Snapshot.Targets[0].Machine)
	}
	// netfilter state не переживает stop/start: rebind обязан пересоздать
	// zone firewall rebound VM и дождаться Ready узла.
	if runtime.firewallCalls == 0 {
		t.Fatal("RebindTarget() did not restore the zone firewall")
	}
	firewallCalls := runtime.firewallCalls
	waitFound := false
	for _, command := range runtime.commands {
		if command.Program == runtime.config.KubectlPath &&
			containsSequence(command.Args, []string{"wait", "--for=condition=Ready"}) {
			waitFound = true
		}
	}
	if !waitFound {
		t.Fatal("RebindTarget() did not wait for the rebound node to become Ready")
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
	if runtime.firewallCalls <= firewallCalls {
		t.Fatal("idempotent rebind did not re-apply the zone firewall")
	}
	// После ещё одного restart rebind с исходным receipt привязывается к текущему
	// поколению: в VM-модели orbctl не отдаёт per-stop timestamps, поэтому rebind
	// всегда перечитывает живое состояние машины вместо сверки FinishedAt.
	runtime.bootID = "77777777-2222-4333-8444-555555555555"
	secondRestart, err := manager.RebindTarget(t.Context(), TargetRebindInput{
		Snapshot:       original,
		StoppedReceipt: stoppedReceipt,
	}, "dc-a-dmz")
	if err != nil {
		t.Fatalf("RebindTarget() after second restart error = %v", err)
	}
	if secondRestart.Snapshot.Targets[0].Machine.BootID != runtime.bootID {
		t.Fatal("rebind did not bind to the current machine generation")
	}
	if _, err := manager.RebindTarget(t.Context(), TargetRebindInput{
		Snapshot:       result.Snapshot,
		StoppedReceipt: stoppedReceipt,
	}, "dc-a-dmz"); err == nil {
		t.Fatal("second RebindTarget() error = nil, want receipt replay rejection")
	}
}

func TestRebindTargetFailsWhenFirewallRestoreFails(t *testing.T) {
	t.Parallel()

	manager, runtime := newTargetFixture(t)
	original := resolveFixtureSnapshot(t, manager)
	stoppedReceipt := stopFixtureTarget(t, manager, runtime, original)
	startFixtureTarget(runtime)
	runtime.firewallErr = errors.New("iptables: permission denied")

	if _, err := manager.RebindTarget(t.Context(), TargetRebindInput{
		Snapshot:       original,
		StoppedReceipt: stoppedReceipt,
	}, "dc-a-dmz"); err == nil {
		t.Fatal("RebindTarget() error = nil, want firewall restore failure")
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
				runtime.bootID = testBootID
			},
		},
		{
			name: "machine id replacement",
			mutate: func(runtime *fakeTargetRuntime, _ *TargetValidationReceipt) {
				runtime.machineID = "01M1JYNYGQG0SPZ1HYQWB1WSE2"
			},
		},
		{
			name: "mac replacement",
			mutate: func(runtime *fakeTargetRuntime, _ *TargetValidationReceipt) {
				runtime.mac = "52:54:00:65:43:21"
			},
		},
		{
			name: "ip replacement",
			mutate: func(runtime *fakeTargetRuntime, _ *TargetValidationReceipt) {
				runtime.ipv4 = "192.168.139.11"
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
	runtime.state = "stopped"
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
	runtime.bootID = "66666666-2222-4333-8444-555555555555"
}

func TestValidateTargetsRejectsSnapshotTamperingAndSelectionErrors(t *testing.T) {
	t.Parallel()

	manager, _ := newTargetFixture(t)
	snapshot := resolveFixtureSnapshot(t, manager)

	tampered := snapshot
	tampered.Targets = append([]FaultTarget{}, snapshot.Targets...)
	tampered.Targets[0].Machine.ID = "01M1JYNYGQG0SPZ1HYQWB1WSE2"
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

	config, err := NewConfig("/workspace/marketmesh", "mm44-test")
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
	config, err := NewConfig("/workspace/marketmesh", "mm44-test")
	if err != nil {
		t.Fatalf("NewConfig() error = %v", err)
	}
	cluster, err := config.Cluster("dc-a", "dmz")
	if err != nil {
		t.Fatalf("Cluster() error = %v", err)
	}
	runtime := &fakeTargetRuntime{
		config:    config,
		cluster:   cluster,
		machineID: testMachineID,
		state:     "running",
		ipv4:      testIPv4,
		mac:       testMAC,
		ifname:    testIface,
		bootID:    testBootID,
		nodeUID:   testNodeUID,
		username:  testVMUser,
		peers: map[string]fakePeerMachine{
			"mm44-test-dc-a-internal": {id: "01M1JYNYGQG0SPZ1HYQWB1WSE2", ipv4: "192.168.139.11"},
			"mm44-test-dc-b-dmz":      {id: "01M1JYNYGQG0SPZ1HYQWB1WSE3", ipv4: "192.168.139.12"},
			"mm44-test-dc-b-internal": {id: "01M1JYNYGQG0SPZ1HYQWB1WSE4", ipv4: "192.168.139.13"},
		},
		commands: []Command{},
	}
	manager := New(config, runtime, nil)
	manager.now = func() time.Time {
		return time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
	}
	return manager, runtime
}

// fakePeerMachine — статичная соседняя VM для mesh-восстановления firewall.
type fakePeerMachine struct {
	id   string
	ipv4 string
}

type fakeTargetRuntime struct {
	config             Config
	cluster            Cluster
	machineID          string
	state              string
	ipv4               string
	mac                string
	ifname             string
	bootID             string
	nodeUID            string
	username           string
	peers              map[string]fakePeerMachine
	duplicateInterface bool
	kubernetesErr      error
	firewallErr        error
	runCalls           int
	firewallCalls      int
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
	if command.Program != "orbctl" || len(command.Args) == 0 {
		return Result{}, fmt.Errorf("unexpected command: %+v", command)
	}
	args := command.Args
	switch {
	case len(args) == 4 && args[0] == "info":
		if args[1] == r.cluster.Name {
			return r.machineResult()
		}
		if peer, ok := r.peers[args[1]]; ok {
			return peer.machineResult(args[1])
		}
	case len(args) >= 4 && args[0] == "run" && args[1] == "-m" && args[2] == r.cluster.Name:
		r.runCalls++
		switch args[3] {
		case "ip":
			return r.interfaceResult()
		case "cat":
			return Result{Stdout: r.bootID + "\n"}, nil
		case "sudo":
			return r.sudoResult(args[5:])
		}
	}
	return Result{}, fmt.Errorf("unexpected orbctl command: %v", args)
}

// machineResult повторяет живую схему `orbctl info <name> -f json` v2.2.3:
// объект с вложенным record и top-level ip4/ip6.
func (p fakePeerMachine) machineResult(name string) (Result, error) {
	payload := map[string]any{
		"record": map[string]any{
			"id":    p.id,
			"name":  name,
			"state": "running",
			"image": map[string]any{
				"distro":  "ubuntu",
				"version": "noble",
				"arch":    runtime.GOARCH,
				"variant": "default",
			},
			"config": map[string]any{
				"default_username": testVMUser,
			},
			"builtin": false,
		},
		"ip4": p.ipv4,
		"ip6": "fd07:b51a:cc66:0:ac8c:31ff:fe6b:b491",
	}
	return marshalResult(payload)
}

// sudoResult имитирует root-команды восстановления firewall: iptables уже
// установлен, jump-правила после restart отсутствуют (-C падает, -I вставляет).
func (r *fakeTargetRuntime) sudoResult(guest []string) (Result, error) {
	if len(guest) == 0 || guest[0] != "iptables" {
		return Result{}, fmt.Errorf("unexpected sudo command: %v", guest)
	}
	r.firewallCalls++
	switch {
	case len(guest) == 2 && guest[1] == "--version":
		return Result{Stdout: "iptables v1.8.10 (nf_tables)\n"}, nil
	case r.firewallErr != nil && !slices.Contains(guest, "-L"):
		return Result{}, r.firewallErr
	case slices.Contains(guest, "-C"):
		return Result{}, errors.New("iptables: rule does not exist")
	default:
		return Result{}, nil
	}
}
func (r *fakeTargetRuntime) machineResult() (Result, error) {
	payload := map[string]any{
		"record": map[string]any{
			"id":    r.machineID,
			"name":  r.cluster.Name,
			"state": r.state,
			"image": map[string]any{
				"distro":  "ubuntu",
				"version": "noble",
				"arch":    runtime.GOARCH,
				"variant": "default",
			},
			"config": map[string]any{
				"default_username": r.username,
			},
			"builtin": false,
		},
		"ip4": r.ipv4,
		"ip6": "fd07:b51a:cc66:0:ac8c:31ff:fe6b:b491",
	}
	return marshalResult(payload)
}

func (r *fakeTargetRuntime) interfaceResult() (Result, error) {
	interfaces := []map[string]any{
		{
			"ifindex": 1,
			"ifname":  "lo",
			"address": "00:00:00:00:00:00",
			"addr_info": []map[string]any{{
				"family":    "inet",
				"local":     "127.0.0.1",
				"prefixlen": 8,
			}},
		},
		{
			"ifindex": 2,
			"ifname":  r.ifname,
			"address": r.mac,
			"addr_info": []map[string]any{{
				"family":    "inet",
				"local":     r.ipv4,
				"prefixlen": 24,
			}},
		},
	}
	if r.duplicateInterface {
		interfaces = append(interfaces, map[string]any{
			"ifindex": 3,
			"ifname":  "eth1",
			"address": r.mac,
			"addr_info": []map[string]any{{
				"family":    "inet",
				"local":     r.ipv4,
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
		args := command.Args
		isInfo := len(args) >= 1 && args[0] == "info"
		isReadOnlyRun := len(args) >= 4 && args[0] == "run" &&
			(args[3] == "ip" || args[3] == "cat")
		if !isInfo && !isReadOnlyRun {
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
