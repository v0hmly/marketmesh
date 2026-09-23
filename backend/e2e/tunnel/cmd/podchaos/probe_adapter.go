package main

import (
	"context"
	"errors"

	"github.com/v0hmly/marketmesh/e2e/tunnel/podchaos"
	"github.com/v0hmly/marketmesh/e2e/tunnel/probe"
)

type probeTimeline interface {
	WaitSteady(context.Context, probe.SteadyRequirement) (probe.SteadyState, error)
	Mark(probe.Marker) error
}

type probeAdapter struct {
	timeline probeTimeline
	steady   probe.SteadyRequirement
	revision string
}

func (adapter *probeAdapter) WaitSteady(ctx context.Context) error {
	if adapter == nil || adapter.timeline == nil {
		return errors.New("podchaos runner: probe timeline is required")
	}
	_, err := adapter.timeline.WaitSteady(ctx, adapter.steady)
	return err
}

func (adapter *probeAdapter) Mark(
	ctx context.Context,
	marker podchaos.FaultMarker,
) error {
	if adapter == nil || adapter.timeline == nil || ctx == nil {
		return errors.New("podchaos runner: probe marker input is invalid")
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		return errors.New("podchaos runner: probe marker context is unbounded")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return adapter.timeline.Mark(probe.Marker{
		FaultID: marker.FaultID, DataCenter: probe.DataCenter(marker.Step.DC),
		Zone:      markerZone(marker.Step.Component),
		Component: probe.Component(marker.Step.Component), Role: probe.Role(marker.Step.Role),
		Phase: markerPhase(marker.Phase), Result: markerResult(marker.Status),
		Revision: adapter.revision,
	})
}

func markerZone(component podchaos.Component) probe.Zone {
	if component == podchaos.ComponentGatewayIn {
		return probe.ZoneDMZ
	}
	if component == podchaos.ComponentGatewayOut {
		return probe.ZoneInternal
	}
	return probe.ZoneUnknown
}

func markerPhase(phase podchaos.MarkerPhase) probe.MarkerPhase {
	if phase == podchaos.MarkerPhaseStarted {
		return probe.MarkerPhaseStarted
	}
	if phase == podchaos.MarkerPhaseEnded {
		return probe.MarkerPhaseRecovered
	}
	return ""
}

func markerResult(status podchaos.MarkerStatus) probe.MarkerResult {
	if status == podchaos.MarkerStatusPassed {
		return probe.MarkerResultSuccess
	}
	if status == podchaos.MarkerStatusFailed {
		return probe.MarkerResultFailure
	}
	return probe.MarkerResultUnknown
}
