package rolling

import (
	"context"
	"errors"
	"fmt"

	"github.com/v0hmly/marketmesh/e2e/tunnel/probe"
)

// ContinuousProbeConfig binds one rolling run to the generic MM-31 probe.
type ContinuousProbeConfig struct {
	RunID             string
	ReadSuccesses     uint32
	MutatingSuccesses uint32
}

type continuousProbeRunner interface {
	Mark(marker probe.Marker) error
	WaitSteady(
		ctx context.Context,
		requirement probe.SteadyRequirement,
	) (probe.SteadyState, error)
}

// ContinuousProbe maps the finite MM-34 lifecycle to the generic MM-31 marker
// contract. It neither creates traffic nor retries requests.
type ContinuousProbe struct {
	config ContinuousProbeConfig
	runner continuousProbeRunner
}

// NewContinuousProbe validates the exact run binding and steady-state gate.
func NewContinuousProbe(
	config ContinuousProbeConfig,
	runner continuousProbeRunner,
) (*ContinuousProbe, error) {
	if err := validateRunID(config.RunID); err != nil {
		return nil, err
	}
	if isNilDependency(runner) {
		return nil, errors.New("rolling: probe runner is required")
	}
	if config.ReadSuccesses == 0 || config.MutatingSuccesses == 0 {
		return nil, errors.New("rolling: both probe success streaks are required")
	}

	return &ContinuousProbe{config: config, runner: runner}, nil
}

// Mark records one lifecycle marker after strict finite enum translation.
func (adapter *ContinuousProbe) Mark(marker Marker) error {
	if marker.RunID != adapter.config.RunID {
		return errors.New("rolling: probe marker run id mismatch")
	}
	dataCenter, err := probeDataCenter(marker.DC)
	if err != nil {
		return err
	}
	zone, err := probeZone(marker.Zone)
	if err != nil {
		return err
	}
	component, err := probeComponent(marker.Component)
	if err != nil {
		return err
	}
	phase, err := probePhase(marker.Phase)
	if err != nil {
		return err
	}
	result, err := probeResult(marker.Result)
	if err != nil {
		return err
	}

	return adapter.runner.Mark(probe.Marker{
		FaultID:    marker.FaultID,
		DataCenter: dataCenter,
		Zone:       zone,
		Component:  component,
		Role:       probe.RoleReplica,
		Phase:      phase,
		Result:     result,
		Revision:   marker.Revision,
	})
}

// WaitSteady waits only on already running MM-31 traffic. The target is
// validated so an out-of-plan caller cannot reuse the adapter as a generic
// readiness oracle.
func (adapter *ContinuousProbe) WaitSteady(ctx context.Context, target Target) error {
	if err := validateTarget(target); err != nil {
		return err
	}
	_, err := adapter.runner.WaitSteady(ctx, probe.SteadyRequirement{
		ReadSuccesses:     adapter.config.ReadSuccesses,
		MutatingSuccesses: adapter.config.MutatingSuccesses,
	})
	if err != nil {
		return fmt.Errorf("rolling: continuous probe did not reach steady state: %w", err)
	}

	return nil
}

func probeDataCenter(value string) (probe.DataCenter, error) {
	switch value {
	case "dc-a":
		return probe.DataCenterA, nil
	case "dc-b":
		return probe.DataCenterB, nil
	default:
		return probe.DataCenterUnknown, errors.New("rolling: unknown probe data center")
	}
}

func probeZone(value string) (probe.Zone, error) {
	switch value {
	case "dmz":
		return probe.ZoneDMZ, nil
	case "internal":
		return probe.ZoneInternal, nil
	default:
		return probe.ZoneUnknown, errors.New("rolling: unknown probe zone")
	}
}

func probeComponent(value Component) (probe.Component, error) {
	switch value {
	case ComponentGatewayIn:
		return probe.ComponentGatewayIn, nil
	case ComponentGatewayOut:
		return probe.ComponentGatewayOut, nil
	case ComponentFakeInternal:
		return probe.ComponentInternalService, nil
	default:
		return probe.ComponentUnknown, errors.New("rolling: unknown probe component")
	}
}

func probePhase(value Phase) (probe.MarkerPhase, error) {
	switch value {
	case PhaseBefore:
		return probe.MarkerPhaseBefore, nil
	case PhaseSteady:
		return probe.MarkerPhaseSteady, nil
	case PhaseRollout:
		return probe.MarkerPhaseStarted, nil
	case PhaseRollback:
		return probe.MarkerPhaseRecovering, nil
	case PhaseRecovered:
		return probe.MarkerPhaseRecovered, nil
	default:
		return "", errors.New("rolling: unknown probe phase")
	}
}

func probeResult(value Result) (probe.MarkerResult, error) {
	switch value {
	case ResultStarted:
		return probe.MarkerResultUnknown, nil
	case ResultPassed:
		return probe.MarkerResultSuccess, nil
	case ResultFailed:
		return probe.MarkerResultFailure, nil
	default:
		return "", errors.New("rolling: unknown probe result")
	}
}
