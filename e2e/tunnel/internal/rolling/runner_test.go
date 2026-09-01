package rolling

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestRunnerRun(t *testing.T) {
	t.Parallel()
	kube := &fakeKubernetes{}
	probe := &fakeProbe{}
	runner := newTestRunner(t, kube, probe)
	plan, err := NewPlan(VariantA, testTransitions())
	if err != nil {
		t.Fatalf("NewPlan() error = %v", err)
	}
	if err := runner.Run(t.Context(), plan); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if kube.count("prepare") != 1 || kube.count("preflight") != 12 ||
		kube.count("update") != 12 || kube.count("wait") != 12 {
		t.Fatalf("kubernetes calls = %v", kube.calls)
	}
	if kube.count("diagnostics") != 0 || kube.count("rollback") != 0 {
		t.Fatalf("unexpected recovery calls = %v", kube.calls)
	}
	if probe.steadyCalls != 24 {
		t.Fatalf("WaitSteady() calls = %d, want 24", probe.steadyCalls)
	}
	if len(probe.markers) != 60 {
		t.Fatalf("markers = %d, want 60", len(probe.markers))
	}
	for index := 1; index < len(probe.markers); index++ {
		if probe.markers[index].Offset < probe.markers[index-1].Offset {
			t.Fatalf("marker offset moved backwards at %d", index)
		}
	}
}

func TestRunnerRollsBackAfterWaitFailure(t *testing.T) {
	t.Parallel()
	waitFailure := errors.New("readiness failed")
	kube := &fakeKubernetes{waitErrors: []error{waitFailure, nil}}
	probe := &fakeProbe{}
	runner := newTestRunner(t, kube, probe)
	plan, err := NewPlan(VariantA, testTransitions())
	if err != nil {
		t.Fatalf("NewPlan() error = %v", err)
	}
	err = runner.Run(t.Context(), plan)
	if !errors.Is(err, waitFailure) {
		t.Fatalf("Run() error = %v, want wait failure", err)
	}
	diagnostics := kube.index("diagnostics")
	rollback := kube.index("rollback")
	if diagnostics < 0 || rollback < 0 || diagnostics > rollback {
		t.Fatalf("recovery call order = %v", kube.calls)
	}
	if kube.count("wait") != 2 {
		t.Fatalf("wait calls = %d, want rollout and rollback waits", kube.count("wait"))
	}
}

func TestRunnerVerifyRollback(t *testing.T) {
	t.Parallel()
	kube := &fakeKubernetes{waitErrors: []error{ErrReadinessNotReached, nil}}
	probe := &fakeProbe{}
	runner := newTestRunner(t, kube, probe)
	target, _ := targetFor("dc-a", ComponentGatewayIn)
	if err := runner.VerifyRollback(
		t.Context(),
		target,
		Fault{Revision: "gateway-in-unready-v2"},
	); err != nil {
		t.Fatalf("VerifyRollback() error = %v", err)
	}
	if kube.count("fault") != 1 || kube.count("diagnostics") != 1 ||
		kube.count("rollback") != 1 {
		t.Fatalf("kubernetes calls = %v", kube.calls)
	}
}

func TestRunnerRejectsUnexpectedFaultFailure(t *testing.T) {
	t.Parallel()
	unexpected := errors.New("deployment uid changed")
	kube := &fakeKubernetes{waitErrors: []error{unexpected, nil}}
	runner := newTestRunner(t, kube, &fakeProbe{})
	target, _ := targetFor("dc-a", ComponentGatewayIn)
	err := runner.VerifyRollback(t.Context(), target, Fault{Revision: "gateway-in-unready-v2"})
	if !errors.Is(err, unexpected) {
		t.Fatalf("VerifyRollback() error = %v, want unexpected failure", err)
	}
}

func newTestRunner(t *testing.T, kube Kubernetes, probe Probe) *Runner {
	t.Helper()
	current := time.Unix(1_700_000_000, 0)
	runner, err := NewRunner(Config{
		RunID:              "run-34",
		TotalTimeout:       time.Minute,
		StepTimeout:        time.Second,
		SteadyTimeout:      time.Second,
		DiagnosticsTimeout: time.Second,
		RollbackTimeout:    time.Second,
		Now: func() time.Time {
			current = current.Add(time.Millisecond)
			return current
		},
	}, kube, probe)
	if err != nil {
		t.Fatalf("NewRunner() error = %v", err)
	}

	return runner
}

func testTransitions() map[Component]Transition {
	digest := "registry.test/marketmesh/component@sha256:" + strings.Repeat("a", 64)
	return map[Component]Transition{
		ComponentGatewayIn: {
			Image: digest, ImageRevision: "gateway-in-image-v2", ConfigRevision: "gateway-in-config-v2",
		},
		ComponentGatewayOut: {
			Image: digest, ImageRevision: "gateway-out-image-v2", ConfigRevision: "gateway-out-config-v2",
		},
		ComponentFakeInternal: {
			Image: digest, ImageRevision: "fake-internal-image-v2", ConfigRevision: "fake-internal-config-v2",
		},
	}
}

type fakeKubernetes struct {
	calls      []string
	waitErrors []error
}

func (kube *fakeKubernetes) Prepare(context.Context) error {
	kube.calls = append(kube.calls, "prepare")
	return nil
}

func (kube *fakeKubernetes) Preflight(context.Context, Target) (Snapshot, error) {
	kube.calls = append(kube.calls, "preflight")
	return Snapshot{
		UID: "deployment-uid", Revision: 1, Generation: 1, Desired: 2,
		Image: "registry.test/current@sha256:" + strings.Repeat("b", 64),
	}, nil
}

func (kube *fakeKubernetes) Update(context.Context, Target, Change, Snapshot) error {
	kube.calls = append(kube.calls, "update")
	return nil
}

func (kube *fakeKubernetes) InjectReadinessFault(context.Context, Target, Fault, Snapshot) error {
	kube.calls = append(kube.calls, "fault")
	return nil
}

func (kube *fakeKubernetes) Wait(context.Context, Target, Expectation) error {
	kube.calls = append(kube.calls, "wait")
	if len(kube.waitErrors) == 0 {
		return nil
	}
	err := kube.waitErrors[0]
	kube.waitErrors = kube.waitErrors[1:]
	return err
}

func (kube *fakeKubernetes) Diagnostics(context.Context, Target) error {
	kube.calls = append(kube.calls, "diagnostics")
	return nil
}

func (kube *fakeKubernetes) Rollback(context.Context, Target, string, Snapshot) error {
	kube.calls = append(kube.calls, "rollback")
	return nil
}

func (kube *fakeKubernetes) count(name string) int {
	count := 0
	for _, call := range kube.calls {
		if call == name {
			count++
		}
	}
	return count
}

func (kube *fakeKubernetes) index(name string) int {
	for index, call := range kube.calls {
		if call == name {
			return index
		}
	}
	return -1
}

type fakeProbe struct {
	markers     []Marker
	steadyCalls int
}

func (probe *fakeProbe) Mark(marker Marker) error {
	probe.markers = append(probe.markers, marker)
	return nil
}

func (probe *fakeProbe) WaitSteady(context.Context, Target) error {
	probe.steadyCalls++
	return nil
}
