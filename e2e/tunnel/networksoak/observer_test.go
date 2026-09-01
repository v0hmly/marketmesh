package networksoak

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/v0hmly/marketmesh/e2e/tunnel/probe"
	"github.com/v0hmly/marketmesh/platform/testkit/networkchaos"
)

type probeLifecycle interface {
	Mark(probe.Marker) error
	WaitSteady(context.Context, probe.SteadyRequirement) (probe.SteadyState, error)
}

type markerTarget struct {
	DataCenter probe.DataCenter
	Zone       probe.Zone
	Component  probe.Component
	Role       probe.Role
	Revision   string
}

type probeObserver struct {
	probe            probeLifecycle
	targets          map[string]markerTarget
	steady           probe.SteadyRequirement
	pendingRecovered []probe.Marker
}

func newProbeObserver(
	continuousProbe probeLifecycle,
	targets map[string]markerTarget,
	steady probe.SteadyRequirement,
) (*probeObserver, error) {
	if continuousProbe == nil {
		return nil, errors.New("network soak: continuous probe is required")
	}
	if len(targets) == 0 {
		return nil, errors.New("network soak: marker targets are required")
	}
	if steady.ReadSuccesses == 0 && steady.MutatingSuccesses == 0 {
		return nil, errors.New("network soak: steady requirement is required")
	}
	return &probeObserver{
		probe:   continuousProbe,
		targets: maps.Clone(targets),
		steady:  steady,
	}, nil
}

func (observer *probeObserver) Observe(
	ctx context.Context,
	observation networkchaos.Observation,
) error {
	if ctx == nil {
		return errors.New("network soak: observation context must not be nil")
	}
	if observation.FaultIndex < 0 || observation.FaultCount <= 0 ||
		observation.FaultIndex >= observation.FaultCount {
		return errors.New("network soak: invalid fault position")
	}
	target, found := observer.targets[observation.FaultName]
	if !found {
		return fmt.Errorf("network soak: marker target for fault %q is absent", observation.FaultName)
	}

	marker := probe.Marker{
		FaultID:    observation.FaultName,
		DataCenter: target.DataCenter,
		Zone:       target.Zone,
		Component:  target.Component,
		Role:       target.Role,
		Revision:   target.Revision,
	}
	switch observation.Phase {
	case networkchaos.ObservationPhaseBefore:
		if observation.FaultIndex == 0 {
			if _, err := observer.probe.WaitSteady(ctx, observer.steady); err != nil {
				return err
			}
		}
		marker.Phase = probe.MarkerPhaseBefore
		marker.Result = probe.MarkerResultUnknown
		return observer.probe.Mark(marker)
	case networkchaos.ObservationPhaseActive:
		marker.Phase = probe.MarkerPhaseStarted
		marker.Result = probe.MarkerResultUnknown
		if err := observer.probe.Mark(marker); err != nil {
			return err
		}
		if !isTerminalFault(observation) {
			return nil
		}
		_, err := observer.probe.WaitSteady(ctx, observer.steady)
		return err
	case networkchaos.ObservationPhaseRecovered:
		marker.Phase = probe.MarkerPhaseRecovered
		marker.Result = probe.MarkerResultSuccess
		observer.pendingRecovered = append(observer.pendingRecovered, marker)
		if !isTerminalFault(observation) {
			return nil
		}
		if len(observer.pendingRecovered) != observation.FaultCount {
			return errors.New("network soak: recovered marker sequence is incomplete")
		}
		if _, err := observer.probe.WaitSteady(ctx, observer.steady); err != nil {
			observer.pendingRecovered = nil
			return err
		}
		pending := slices.Clone(observer.pendingRecovered)
		observer.pendingRecovered = nil
		var markerErr error
		for _, recovered := range pending {
			markerErr = errors.Join(markerErr, observer.probe.Mark(recovered))
		}
		return markerErr
	default:
		return fmt.Errorf("network soak: unsupported observation phase %q", observation.Phase)
	}
}

func isTerminalFault(observation networkchaos.Observation) bool {
	return observation.FaultIndex == observation.FaultCount-1
}

type fakeProbeLifecycle struct {
	events  []string
	markers []probe.Marker
	waitErr error
}

func (lifecycle *fakeProbeLifecycle) Mark(marker probe.Marker) error {
	lifecycle.events = append(lifecycle.events, "mark:"+string(marker.Phase)+":"+marker.FaultID)
	lifecycle.markers = append(lifecycle.markers, marker)
	return nil
}

func (lifecycle *fakeProbeLifecycle) WaitSteady(
	context.Context,
	probe.SteadyRequirement,
) (probe.SteadyState, error) {
	lifecycle.events = append(lifecycle.events, "wait")
	return probe.SteadyState{}, lifecycle.waitErr
}

func TestProbeObserverMarksCombinedFaultsAroundBoundedSteadySamples(t *testing.T) {
	t.Parallel()

	lifecycle := &fakeProbeLifecycle{}
	observer, err := newProbeObserver(
		lifecycle,
		map[string]markerTarget{
			"partition-a": {
				DataCenter: probe.DataCenterA,
				Zone:       probe.ZoneInternal,
				Component:  probe.ComponentNetwork,
			},
			"degrade-b": {
				DataCenter: probe.DataCenterB,
				Zone:       probe.ZoneDMZ,
				Component:  probe.ComponentNetwork,
			},
		},
		probe.SteadyRequirement{ReadSuccesses: 3, MutatingSuccesses: 2},
	)
	if err != nil {
		t.Fatalf("newProbeObserver() error = %v", err)
	}

	for index, name := range []string{"partition-a", "degrade-b"} {
		if err := observer.Observe(t.Context(), networkchaos.Observation{
			FaultIndex: index,
			FaultCount: 2,
			FaultName:  name,
			Phase:      networkchaos.ObservationPhaseBefore,
		}); err != nil {
			t.Fatalf("before Observe() error = %v", err)
		}
	}
	for index, name := range []string{"partition-a", "degrade-b"} {
		if err := observer.Observe(t.Context(), networkchaos.Observation{
			FaultIndex: index,
			FaultCount: 2,
			FaultName:  name,
			Phase:      networkchaos.ObservationPhaseActive,
		}); err != nil {
			t.Fatalf("active Observe() error = %v", err)
		}
	}
	for index, name := range []string{"partition-a", "degrade-b"} {
		if err := observer.Observe(t.Context(), networkchaos.Observation{
			FaultIndex: index,
			FaultCount: 2,
			FaultName:  name,
			Phase:      networkchaos.ObservationPhaseRecovered,
		}); err != nil {
			t.Fatalf("recovered Observe() error = %v", err)
		}
	}

	want := []string{
		"wait",
		"mark:before:partition-a",
		"mark:before:degrade-b",
		"mark:started:partition-a",
		"mark:started:degrade-b",
		"wait",
		"wait",
		"mark:recovered:partition-a",
		"mark:recovered:degrade-b",
	}
	if !slices.Equal(lifecycle.events, want) {
		t.Fatalf("events = %v, want %v", lifecycle.events, want)
	}
}

func TestProbeObserverLeavesFaultOpenWhenRecoveryIsNotSteady(t *testing.T) {
	t.Parallel()

	lifecycle := &fakeProbeLifecycle{waitErr: errors.New("steady sample rejected")}
	observer, err := newProbeObserver(
		lifecycle,
		map[string]markerTarget{
			"partition-a": {Component: probe.ComponentNetwork},
		},
		probe.SteadyRequirement{ReadSuccesses: 1},
	)
	if err != nil {
		t.Fatalf("newProbeObserver() error = %v", err)
	}

	err = observer.Observe(t.Context(), networkchaos.Observation{
		FaultIndex: 0,
		FaultCount: 1,
		FaultName:  "partition-a",
		Phase:      networkchaos.ObservationPhaseRecovered,
	})
	if err == nil || !strings.Contains(err.Error(), "steady sample rejected") {
		t.Fatalf("Observe() error = %v, want steady failure", err)
	}
	if len(lifecycle.markers) != 0 {
		t.Fatalf("recovered markers = %v, want open fault", lifecycle.markers)
	}
}
