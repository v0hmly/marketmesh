package rolling

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/v0hmly/marketmesh/e2e/tunnel/probe"
)

func TestContinuousProbeMapsFiniteLifecycle(t *testing.T) {
	t.Parallel()

	delegate := &probeRunnerStub{}
	adapter, err := NewContinuousProbe(ContinuousProbeConfig{
		RunID: "mm34-run", ReadSuccesses: 7, MutatingSuccesses: 5,
	}, delegate)
	if err != nil {
		t.Fatalf("NewContinuousProbe() error = %v", err)
	}
	target, _ := targetFor("dc-a", ComponentFakeInternal)
	if err := adapter.Mark(Marker{
		RunID: "mm34-run", FaultID: "mm34-a-dc-a-fake-internal-image",
		DC: "dc-a", Zone: "internal", Component: ComponentFakeInternal,
		Phase: PhaseRollback, Result: ResultStarted, Revision: "image-v2",
	}); err != nil {
		t.Fatalf("Mark() error = %v", err)
	}
	if delegate.marker != (probe.Marker{
		FaultID: "mm34-a-dc-a-fake-internal-image", DataCenter: probe.DataCenterA,
		Zone: probe.ZoneInternal, Component: probe.ComponentInternalService,
		Role: probe.RoleReplica, Phase: probe.MarkerPhaseRecovering,
		Result: probe.MarkerResultUnknown, Revision: "image-v2",
	}) {
		t.Fatalf("marker = %#v", delegate.marker)
	}

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := adapter.WaitSteady(ctx, target); err != nil {
		t.Fatalf("WaitSteady() error = %v", err)
	}
	if delegate.requirement != (probe.SteadyRequirement{
		ReadSuccesses: 7, MutatingSuccesses: 5,
	}) {
		t.Fatalf("requirement = %#v", delegate.requirement)
	}
}

func TestContinuousProbeRejectsInvalidBoundary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		config ContinuousProbeConfig
		runner continuousProbeRunner
	}{
		{name: "run id", config: ContinuousProbeConfig{RunID: ""}, runner: &probeRunnerStub{}},
		{name: "runner", config: ContinuousProbeConfig{
			RunID: "mm34-run", ReadSuccesses: 1, MutatingSuccesses: 1,
		}},
		{name: "read streak", config: ContinuousProbeConfig{
			RunID: "mm34-run", MutatingSuccesses: 1,
		}, runner: &probeRunnerStub{}},
		{name: "mutating streak", config: ContinuousProbeConfig{
			RunID: "mm34-run", ReadSuccesses: 1,
		}, runner: &probeRunnerStub{}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := NewContinuousProbe(test.config, test.runner); err == nil {
				t.Fatal("NewContinuousProbe() error = nil")
			}
		})
	}

	adapter, err := NewContinuousProbe(ContinuousProbeConfig{
		RunID: "mm34-run", ReadSuccesses: 1, MutatingSuccesses: 1,
	}, &probeRunnerStub{})
	if err != nil {
		t.Fatalf("NewContinuousProbe() error = %v", err)
	}
	if err := adapter.Mark(Marker{RunID: "other"}); err == nil {
		t.Fatal("Mark() error = nil for foreign run")
	}
}

func TestContinuousProbePropagatesBoundedErrors(t *testing.T) {
	t.Parallel()

	want := errors.New("steady failed")
	delegate := &probeRunnerStub{waitErr: want}
	adapter, err := NewContinuousProbe(ContinuousProbeConfig{
		RunID: "mm34-run", ReadSuccesses: 1, MutatingSuccesses: 1,
	}, delegate)
	if err != nil {
		t.Fatalf("NewContinuousProbe() error = %v", err)
	}
	target, _ := targetFor("dc-b", ComponentGatewayOut)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := adapter.WaitSteady(ctx, target); !errors.Is(err, want) {
		t.Fatalf("WaitSteady() error = %v, want wrapped sentinel", err)
	}
}

type probeRunnerStub struct {
	marker      probe.Marker
	requirement probe.SteadyRequirement
	markErr     error
	waitErr     error
}

func (stub *probeRunnerStub) Mark(marker probe.Marker) error {
	stub.marker = marker
	return stub.markErr
}

func (stub *probeRunnerStub) WaitSteady(
	_ context.Context,
	requirement probe.SteadyRequirement,
) (probe.SteadyState, error) {
	stub.requirement = requirement
	return probe.SteadyState{}, stub.waitErr
}
