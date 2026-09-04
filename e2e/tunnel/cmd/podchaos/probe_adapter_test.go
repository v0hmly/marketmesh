package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/v0hmly/marketmesh/e2e/tunnel/podchaos"
	"github.com/v0hmly/marketmesh/e2e/tunnel/probe"
)

func TestProbeAdapterMapsFaultMarker(t *testing.T) {
	t.Parallel()

	timeline := &timelineStub{}
	adapter := &probeAdapter{
		timeline: timeline,
		steady: probe.SteadyRequirement{
			ReadSuccesses: 5, MutatingSuccesses: 5,
		},
		revision: "abc123",
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := adapter.Mark(ctx, podchaos.FaultMarker{
		FaultID: "fault-01",
		Step: podchaos.Step{
			DC: podchaos.DCA, Component: podchaos.ComponentGatewayOut,
			Role: podchaos.RoleStandby,
		},
		Phase:  podchaos.MarkerPhaseEnded,
		Status: podchaos.MarkerStatusPassed,
	}); err != nil {
		t.Fatalf("Mark() error = %v", err)
	}
	want := probe.Marker{
		FaultID: "fault-01", DataCenter: probe.DataCenterA,
		Zone: probe.ZoneInternal, Component: probe.ComponentGatewayOut,
		Role: probe.RoleStandby, Phase: probe.MarkerPhaseRecovered,
		Result: probe.MarkerResultSuccess, Revision: "abc123",
	}
	if timeline.marker != want {
		t.Fatalf("marker = %+v, want %+v", timeline.marker, want)
	}
	if err := adapter.WaitSteady(ctx); err != nil {
		t.Fatalf("WaitSteady() error = %v", err)
	}
	if timeline.requirement != adapter.steady {
		t.Fatalf("steady requirement = %+v", timeline.requirement)
	}
}

func TestProbeAdapterFailsClosed(t *testing.T) {
	t.Parallel()

	if err := (&probeAdapter{}).WaitSteady(context.Background()); err == nil {
		t.Fatal("WaitSteady() error = nil")
	}
	adapter := &probeAdapter{timeline: &timelineStub{}, revision: "abc123"}
	if err := adapter.Mark(context.Background(), podchaos.FaultMarker{}); err == nil {
		t.Fatal("Mark(unbounded) error = nil")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	ctx, deadlineCancel := context.WithTimeout(ctx, time.Second)
	defer deadlineCancel()
	if err := adapter.Mark(ctx, podchaos.FaultMarker{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Mark(canceled) error = %v", err)
	}
}

type timelineStub struct {
	requirement probe.SteadyRequirement
	marker      probe.Marker
}

func (timeline *timelineStub) WaitSteady(
	_ context.Context,
	requirement probe.SteadyRequirement,
) (probe.SteadyState, error) {
	timeline.requirement = requirement
	return probe.SteadyState{}, nil
}

func (timeline *timelineStub) Mark(marker probe.Marker) error {
	timeline.marker = marker
	return nil
}
