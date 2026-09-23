package networksoak

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/v0hmly/marketmesh/e2e/tunnel/internal/servicechaos"
	"github.com/v0hmly/marketmesh/e2e/tunnel/probe"
)

type serviceFaultKey struct {
	fault servicechaos.Fault
	dc    probe.DataCenter
}

type serviceFaultState struct {
	occurrences uint32
	preparedID  string
	activeID    string
}

type serviceProbeObserver struct {
	probe      probeLifecycle
	steady     probe.SteadyRequirement
	states     map[serviceFaultKey]*serviceFaultState
	preparedID string
	activeID   string
}

func newServiceProbeObserver(
	continuousProbe probeLifecycle,
	steady probe.SteadyRequirement,
) (*serviceProbeObserver, error) {
	if isNilProbeLifecycle(continuousProbe) {
		return nil, errors.New("network soak: continuous probe is required")
	}
	if steady.ReadSuccesses == 0 || steady.MutatingSuccesses == 0 {
		return nil, errors.New("network soak: service chaos requires both traffic classes")
	}
	return &serviceProbeObserver{
		probe:  continuousProbe,
		steady: steady,
		states: make(map[serviceFaultKey]*serviceFaultState),
	}, nil
}

func (observer *serviceProbeObserver) Observe(
	ctx context.Context,
	observation servicechaos.Observation,
) error {
	if ctx == nil {
		return errors.New("network soak: service observation context must not be nil")
	}
	if !observation.RequireRead || !observation.RequireMutation {
		return errors.New("network soak: service observation must require both traffic classes")
	}
	faultedDC, _, err := serviceDataCenters(
		observation.FaultedDC,
		observation.EligibleDC,
	)
	if err != nil {
		return err
	}
	component, maxOccurrences, err := serviceFaultContract(observation.Fault)
	if err != nil {
		return err
	}
	key := serviceFaultKey{fault: observation.Fault, dc: faultedDC}
	state := observer.states[key]
	if state == nil {
		state = &serviceFaultState{}
		observer.states[key] = state
	}
	if state.occurrences >= maxOccurrences && observation.Phase != servicechaos.PhaseRecovered {
		return fmt.Errorf(
			"network soak: service fault %q in %s exceeds occurrence contract",
			observation.Fault,
			faultedDC,
		)
	}

	marker := probe.Marker{
		DataCenter: faultedDC,
		Zone:       probe.ZoneInternal,
		Component:  component,
		Role:       probe.RoleUnknown,
	}
	if component == probe.ComponentInternalService {
		marker.Role = probe.RoleReplica
	}
	switch observation.Phase {
	case servicechaos.PhaseBaseline:
		if observer.preparedID != "" || observer.activeID != "" ||
			state.preparedID != "" || state.activeID != "" {
			return errors.New("network soak: duplicate or overlapping service baseline")
		}
		if _, err := observer.probe.WaitSteady(ctx, observer.steady); err != nil {
			return err
		}
		marker.FaultID = serviceFaultID(observation.Fault, faultedDC, state.occurrences+1)
		marker.Phase = probe.MarkerPhaseBefore
		marker.Result = probe.MarkerResultUnknown
		if err := observer.probe.Mark(marker); err != nil {
			return err
		}
		state.preparedID = marker.FaultID
		observer.preparedID = marker.FaultID
		return nil
	case servicechaos.PhaseActive:
		if observer.activeID != "" || state.activeID != "" {
			return errors.New("network soak: overlapping service faults are forbidden")
		}
		if state.preparedID == "" && observation.Fault != servicechaos.FaultDeletePods {
			return errors.New("network soak: service fault active without baseline")
		}
		marker.FaultID = state.preparedID
		if marker.FaultID == "" {
			marker.FaultID = serviceFaultID(observation.Fault, faultedDC, state.occurrences+1)
		}
		marker.Phase = probe.MarkerPhaseStarted
		marker.Result = probe.MarkerResultUnknown
		if err := observer.probe.Mark(marker); err != nil {
			return err
		}
		state.preparedID = ""
		state.activeID = marker.FaultID
		observer.preparedID = ""
		observer.activeID = marker.FaultID
		state.occurrences++
		_, err := observer.probe.WaitSteady(ctx, observer.steady)
		return err
	case servicechaos.PhaseRecovered:
		if state.activeID == "" || observer.activeID != state.activeID {
			return errors.New("network soak: service recovery without active fault")
		}
		if _, err := observer.probe.WaitSteady(ctx, observer.steady); err != nil {
			return err
		}
		marker.FaultID = state.activeID
		marker.Phase = probe.MarkerPhaseRecovered
		marker.Result = probe.MarkerResultSuccess
		if err := observer.probe.Mark(marker); err != nil {
			return err
		}
		state.activeID = ""
		observer.activeID = ""
		return nil
	default:
		return fmt.Errorf("network soak: unsupported service observation phase %q", observation.Phase)
	}
}

func serviceDataCenters(
	faulted string,
	eligible string,
) (probe.DataCenter, probe.DataCenter, error) {
	faultedDC := probe.DataCenter(faulted)
	eligibleDC := probe.DataCenter(eligible)
	if faultedDC != probe.DataCenterA && faultedDC != probe.DataCenterB {
		return probe.DataCenterUnknown, probe.DataCenterUnknown,
			errors.New("network soak: invalid faulted service data center")
	}
	if eligibleDC != probe.DataCenterA && eligibleDC != probe.DataCenterB || eligibleDC == faultedDC {
		return probe.DataCenterUnknown, probe.DataCenterUnknown,
			errors.New("network soak: invalid eligible service data center")
	}
	return faultedDC, eligibleDC, nil
}

func serviceFaultContract(
	fault servicechaos.Fault,
) (probe.Component, uint32, error) {
	switch fault {
	case servicechaos.FaultDeletePods:
		return probe.ComponentInternalService, 2, nil
	case servicechaos.FaultScaleToZero:
		return probe.ComponentInternalService, 1, nil
	case servicechaos.FaultEmptySelector, servicechaos.FaultRecreateService:
		return probe.ComponentKubernetesService, 1, nil
	default:
		return probe.ComponentUnknown, 0, fmt.Errorf(
			"network soak: unsupported service fault %q",
			fault,
		)
	}
}

func serviceFaultID(fault servicechaos.Fault, dc probe.DataCenter, occurrence uint32) string {
	return fmt.Sprintf("mm33-%s-%s-%d", fault, dc, occurrence)
}

func TestServiceProbeObserverMapsBoundedMM33Lifecycle(t *testing.T) {
	t.Parallel()

	lifecycle := &fakeProbeLifecycle{}
	observer, err := newServiceProbeObserver(
		lifecycle,
		probe.SteadyRequirement{ReadSuccesses: 2, MutatingSuccesses: 2},
	)
	if err != nil {
		t.Fatalf("newServiceProbeObserver() error = %v", err)
	}
	observations := []servicechaos.Observation{
		serviceObservation(servicechaos.FaultDeletePods, servicechaos.PhaseBaseline),
		serviceObservation(servicechaos.FaultDeletePods, servicechaos.PhaseActive),
		serviceObservation(servicechaos.FaultDeletePods, servicechaos.PhaseRecovered),
		serviceObservation(servicechaos.FaultDeletePods, servicechaos.PhaseActive),
		serviceObservation(servicechaos.FaultDeletePods, servicechaos.PhaseRecovered),
		serviceObservation(servicechaos.FaultEmptySelector, servicechaos.PhaseBaseline),
		serviceObservation(servicechaos.FaultEmptySelector, servicechaos.PhaseActive),
		serviceObservation(servicechaos.FaultEmptySelector, servicechaos.PhaseRecovered),
	}
	for _, observation := range observations {
		if err := observer.Observe(t.Context(), observation); err != nil {
			t.Fatalf("Observe(%+v) error = %v", observation, err)
		}
	}

	want := []string{
		"wait",
		"mark:before:mm33-delete-pods-dc-a-1",
		"mark:started:mm33-delete-pods-dc-a-1",
		"wait",
		"wait",
		"mark:recovered:mm33-delete-pods-dc-a-1",
		"mark:started:mm33-delete-pods-dc-a-2",
		"wait",
		"wait",
		"mark:recovered:mm33-delete-pods-dc-a-2",
		"wait",
		"mark:before:mm33-empty-service-selector-dc-a-1",
		"mark:started:mm33-empty-service-selector-dc-a-1",
		"wait",
		"wait",
		"mark:recovered:mm33-empty-service-selector-dc-a-1",
	}
	if !slices.Equal(lifecycle.events, want) {
		t.Fatalf("events = %v, want %v", lifecycle.events, want)
	}
	if lifecycle.markers[0].DataCenter != probe.DataCenterA ||
		lifecycle.markers[0].Zone != probe.ZoneInternal ||
		lifecycle.markers[0].Component != probe.ComponentInternalService ||
		lifecycle.markers[0].Role != probe.RoleReplica ||
		lifecycle.markers[len(lifecycle.markers)-1].Component !=
			probe.ComponentKubernetesService ||
		lifecycle.markers[len(lifecycle.markers)-1].Role != probe.RoleUnknown {
		t.Fatalf("markers = %+v, want exact service dimensions", lifecycle.markers)
	}
}

func TestServiceProbeObserverAcceptsExactMM33ObservationMatrix(t *testing.T) {
	t.Parallel()

	lifecycle := &fakeProbeLifecycle{}
	observer, err := newServiceProbeObserver(
		lifecycle,
		probe.SteadyRequirement{ReadSuccesses: 1, MutatingSuccesses: 1},
	)
	if err != nil {
		t.Fatalf("newServiceProbeObserver() error = %v", err)
	}
	observations := make([]servicechaos.Observation, 0, 28)
	for _, pair := range [][2]string{{"dc-a", "dc-b"}, {"dc-b", "dc-a"}} {
		observations = append(observations, serviceObservationForDC(
			servicechaos.FaultDeletePods,
			servicechaos.PhaseBaseline,
			pair[0],
			pair[1],
		))
		for range 2 {
			observations = append(
				observations,
				serviceObservationForDC(
					servicechaos.FaultDeletePods,
					servicechaos.PhaseActive,
					pair[0],
					pair[1],
				),
				serviceObservationForDC(
					servicechaos.FaultDeletePods,
					servicechaos.PhaseRecovered,
					pair[0],
					pair[1],
				),
			)
		}
		for _, fault := range []servicechaos.Fault{
			servicechaos.FaultScaleToZero,
			servicechaos.FaultEmptySelector,
			servicechaos.FaultRecreateService,
		} {
			for _, phase := range []servicechaos.Phase{
				servicechaos.PhaseBaseline,
				servicechaos.PhaseActive,
				servicechaos.PhaseRecovered,
			} {
				observations = append(
					observations,
					serviceObservationForDC(fault, phase, pair[0], pair[1]),
				)
			}
		}
	}
	for _, observation := range observations {
		if err := observer.Observe(t.Context(), observation); err != nil {
			t.Fatalf("Observe(%+v) error = %v", observation, err)
		}
	}
	if len(observations) != 28 || len(lifecycle.markers) != len(observations) {
		t.Fatalf(
			"observations = %d, markers = %d, want exact MM-33 matrix",
			len(observations),
			len(lifecycle.markers),
		)
	}
	phaseCounts := make(map[string]map[probe.MarkerPhase]int)
	for _, marker := range lifecycle.markers {
		if phaseCounts[marker.FaultID] == nil {
			phaseCounts[marker.FaultID] = make(map[probe.MarkerPhase]int)
		}
		phaseCounts[marker.FaultID][marker.Phase]++
	}
	if len(phaseCounts) != 10 {
		t.Fatalf("fault IDs = %v, want ten unique MM-33 occurrences", phaseCounts)
	}
	for faultID, phases := range phaseCounts {
		if phases[probe.MarkerPhaseStarted] != 1 || phases[probe.MarkerPhaseRecovered] != 1 {
			t.Fatalf("fault %s marker phases = %v, want one start and recovery", faultID, phases)
		}
	}
}

func TestServiceProbeObserverRejectsInvalidOrOverlappingLifecycle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		observations []servicechaos.Observation
		want         string
	}{
		{
			name: "eligible DC must differ",
			observations: []servicechaos.Observation{{
				Fault: servicechaos.FaultScaleToZero, Phase: servicechaos.PhaseBaseline,
				FaultedDC: "dc-a", EligibleDC: "dc-a", RequireRead: true, RequireMutation: true,
			}},
			want: "invalid eligible service data center",
		},
		{
			name: "both traffic classes are required",
			observations: []servicechaos.Observation{{
				Fault: servicechaos.FaultScaleToZero, Phase: servicechaos.PhaseBaseline,
				FaultedDC: "dc-a", EligibleDC: "dc-b", RequireRead: true,
			}},
			want: "must require both traffic classes",
		},
		{
			name: "non-pod active requires baseline",
			observations: []servicechaos.Observation{
				serviceObservation(servicechaos.FaultScaleToZero, servicechaos.PhaseActive),
			},
			want: "active without baseline",
		},
		{
			name: "third pod deletion is rejected",
			observations: []servicechaos.Observation{
				serviceObservation(servicechaos.FaultDeletePods, servicechaos.PhaseBaseline),
				serviceObservation(servicechaos.FaultDeletePods, servicechaos.PhaseActive),
				serviceObservation(servicechaos.FaultDeletePods, servicechaos.PhaseRecovered),
				serviceObservation(servicechaos.FaultDeletePods, servicechaos.PhaseActive),
				serviceObservation(servicechaos.FaultDeletePods, servicechaos.PhaseRecovered),
				serviceObservation(servicechaos.FaultDeletePods, servicechaos.PhaseActive),
			},
			want: "exceeds occurrence contract",
		},
		{
			name: "faults cannot overlap across keys",
			observations: []servicechaos.Observation{
				serviceObservation(servicechaos.FaultScaleToZero, servicechaos.PhaseBaseline),
				serviceObservation(servicechaos.FaultScaleToZero, servicechaos.PhaseActive),
				serviceObservation(servicechaos.FaultEmptySelector, servicechaos.PhaseBaseline),
			},
			want: "duplicate or overlapping service baseline",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			observer, err := newServiceProbeObserver(
				&fakeProbeLifecycle{},
				probe.SteadyRequirement{ReadSuccesses: 1, MutatingSuccesses: 1},
			)
			if err != nil {
				t.Fatalf("newServiceProbeObserver() error = %v", err)
			}
			for _, observation := range test.observations {
				err = observer.Observe(t.Context(), observation)
				if err != nil {
					break
				}
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Observe() error = %v, want %q", err, test.want)
			}
		})
	}
}

func serviceObservation(
	fault servicechaos.Fault,
	phase servicechaos.Phase,
) servicechaos.Observation {
	return serviceObservationForDC(fault, phase, "dc-a", "dc-b")
}

func serviceObservationForDC(
	fault servicechaos.Fault,
	phase servicechaos.Phase,
	faultedDC string,
	eligibleDC string,
) servicechaos.Observation {
	return servicechaos.Observation{
		Fault: fault, Phase: phase, FaultedDC: faultedDC, EligibleDC: eligibleDC,
		RequireRead: true, RequireMutation: true,
	}
}
