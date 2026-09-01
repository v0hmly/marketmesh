package rolling

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
)

const maximumStageTimeout = 30 * time.Minute

// Runner sequences probe markers, one-target mutations, and automatic rollback.
type Runner struct {
	config Config
	kube   Kubernetes
	probe  Probe
}

// NewRunner validates bounded lifecycle dependencies.
func NewRunner(config Config, kube Kubernetes, probe Probe) (*Runner, error) {
	if err := validateRunID(config.RunID); err != nil {
		return nil, err
	}
	if isNilDependency(kube) {
		return nil, errors.New("rolling: kubernetes adapter is required")
	}
	if isNilDependency(probe) {
		return nil, errors.New("rolling: continuous probe is required")
	}
	if config.TotalTimeout <= 0 || config.TotalTimeout > 2*time.Hour {
		return nil, errors.New("rolling: total timeout is outside bounds")
	}
	for name, value := range map[string]time.Duration{
		"step":        config.StepTimeout,
		"steady":      config.SteadyTimeout,
		"diagnostics": config.DiagnosticsTimeout,
		"rollback":    config.RollbackTimeout,
	} {
		if value <= 0 || value > maximumStageTimeout {
			return nil, fmt.Errorf("rolling: %s timeout is outside bounds", name)
		}
	}
	if config.Output == nil {
		config.Output = io.Discard
	}
	if config.Now == nil {
		config.Now = time.Now
	}

	return &Runner{config: config, kube: kube, probe: probe}, nil
}

// Run executes one positive variant. Any post-mutation failure triggers rollback.
func (runner *Runner) Run(ctx context.Context, plan Plan) error {
	if ctx == nil {
		return errors.New("rolling: run context must not be nil")
	}
	if err := validatePlan(plan); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, runner.config.TotalTimeout)
	defer cancel()
	start := runner.config.Now()
	prepareCtx, cancelPrepare := context.WithTimeout(ctx, runner.config.StepTimeout)
	prepareErr := runner.kube.Prepare(prepareCtx)
	cancelPrepare()
	if prepareErr != nil {
		return fmt.Errorf("rolling: preparing kubernetes targets: %w", prepareErr)
	}

	for _, step := range plan.Steps {
		if err := validateTarget(step.Target); err != nil {
			return err
		}
		if err := validateChange(step.Change); err != nil {
			return err
		}
		faultID, err := faultIDForStep(plan.Variant, step)
		if err != nil {
			return err
		}
		if err := runner.runStep(ctx, start, faultID, step); err != nil {
			return err
		}
	}

	return nil
}

// VerifyRollback requires a built-in readiness fault to fail, then proves recovery.
func (runner *Runner) VerifyRollback(ctx context.Context, target Target, fault Fault) error {
	if ctx == nil {
		return errors.New("rolling: rollback context must not be nil")
	}
	if err := validateTarget(target); err != nil {
		return err
	}
	if err := validateRevision(fault.Revision); err != nil {
		return fmt.Errorf("rolling: validating fault revision: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, runner.config.TotalTimeout)
	defer cancel()
	start := runner.config.Now()
	prepareCtx, cancelPrepare := context.WithTimeout(ctx, runner.config.StepTimeout)
	prepareErr := runner.kube.Prepare(prepareCtx)
	cancelPrepare()
	if prepareErr != nil {
		return fmt.Errorf("rolling: preparing kubernetes targets: %w", prepareErr)
	}
	faultID, err := rollbackFaultID(target)
	if err != nil {
		return err
	}
	if err := runner.waitSteady(ctx, start, target, faultID, PhaseBefore); err != nil {
		return err
	}
	snapshot, err := runner.preflight(ctx, target)
	if err != nil {
		return err
	}
	if err := runner.mark(start, faultID, target, PhaseRollout, ResultStarted, fault.Revision); err != nil {
		return err
	}
	updateCtx, cancelUpdate := context.WithTimeout(ctx, runner.config.StepTimeout)
	updateErr := runner.kube.InjectReadinessFault(updateCtx, target, fault, snapshot)
	cancelUpdate()
	if updateErr != nil {
		return runner.failAndRollback(ctx, start, faultID, target, fault.Revision, snapshot, updateErr)
	}
	waitCtx, cancelWait := context.WithTimeout(ctx, runner.config.StepTimeout)
	waitErr := runner.kube.Wait(waitCtx, target, Expectation{
		UID:            snapshot.UID,
		Image:          snapshot.Image,
		ConfigRevision: fault.Revision,
		Desired:        snapshot.Desired,
	})
	cancelWait()
	if waitErr == nil {
		waitErr = errors.New("rolling: readiness fault unexpectedly became ready")
	}
	rollbackErr := runner.failAndRollback(ctx, start, faultID, target, fault.Revision, snapshot, nil)
	if rollbackErr != nil || !errors.Is(waitErr, ErrReadinessNotReached) {
		return errors.Join(waitErr, rollbackErr)
	}

	return nil
}

func (runner *Runner) runStep(ctx context.Context, start time.Time, faultID string, step Step) error {
	if err := runner.waitSteady(ctx, start, step.Target, faultID, PhaseBefore); err != nil {
		return err
	}
	snapshot, err := runner.preflight(ctx, step.Target)
	if err != nil {
		return err
	}
	if err := runner.mark(start, faultID, step.Target, PhaseRollout, ResultStarted, step.Change.Revision); err != nil {
		return err
	}
	updateCtx, cancelUpdate := context.WithTimeout(ctx, runner.config.StepTimeout)
	updateErr := runner.kube.Update(updateCtx, step.Target, step.Change, snapshot)
	cancelUpdate()
	if updateErr != nil {
		return runner.failAndRollback(
			ctx,
			start,
			faultID,
			step.Target,
			step.Change.Revision,
			snapshot,
			fmt.Errorf("rolling: updating target: %w", updateErr),
		)
	}
	waitCtx, cancelWait := context.WithTimeout(ctx, runner.config.StepTimeout)
	waitErr := runner.kube.Wait(
		waitCtx,
		step.Target,
		expectationFromChange(snapshot, step.Change),
	)
	cancelWait()
	if waitErr != nil {
		return runner.failAndRollback(
			ctx,
			start,
			faultID,
			step.Target,
			step.Change.Revision,
			snapshot,
			fmt.Errorf("rolling: waiting for target: %w", waitErr),
		)
	}
	if err := runner.waitSteady(ctx, start, step.Target, faultID, PhaseSteady); err != nil {
		return runner.failAndRollback(ctx, start, faultID, step.Target, step.Change.Revision, snapshot, err)
	}
	if err := runner.mark(start, faultID, step.Target, PhaseRecovered, ResultPassed, step.Change.Revision); err != nil {
		return runner.failAndRollback(ctx, start, faultID, step.Target, step.Change.Revision, snapshot, err)
	}
	_, _ = fmt.Fprintf(
		runner.config.Output,
		"rolling: %s/%s %s revision=%s ready\n",
		step.Target.DC,
		step.Target.Zone,
		step.Target.Component,
		step.Change.Revision,
	)

	return nil
}

func (runner *Runner) preflight(ctx context.Context, target Target) (Snapshot, error) {
	preflightCtx, cancel := context.WithTimeout(ctx, runner.config.StepTimeout)
	defer cancel()
	snapshot, err := runner.kube.Preflight(preflightCtx, target)
	if err != nil {
		return Snapshot{}, fmt.Errorf("rolling: preflight for %s/%s: %w", target.DC, target.Component, err)
	}

	return snapshot, nil
}

func (runner *Runner) waitSteady(
	ctx context.Context,
	start time.Time,
	target Target,
	faultID string,
	phase Phase,
) error {
	steadyCtx, cancel := context.WithTimeout(ctx, runner.config.SteadyTimeout)
	defer cancel()
	if err := runner.probe.WaitSteady(steadyCtx, target); err != nil {
		return fmt.Errorf("rolling: waiting for probe steady state: %w", err)
	}

	return runner.mark(start, faultID, target, phase, ResultPassed, "steady")
}

func (runner *Runner) failAndRollback(
	ctx context.Context,
	start time.Time,
	faultID string,
	target Target,
	revision string,
	snapshot Snapshot,
	cause error,
) error {
	var markerErr error
	if cause != nil {
		markerErr = runner.mark(start, faultID, target, PhaseRollout, ResultFailed, revision)
	}
	diagnosticCtx, cancelDiagnostics := context.WithTimeout(
		context.WithoutCancel(ctx),
		runner.config.DiagnosticsTimeout,
	)
	diagnosticErr := runner.kube.Diagnostics(diagnosticCtx, target)
	cancelDiagnostics()
	rollbackMarkerErr := runner.mark(start, faultID, target, PhaseRollback, ResultStarted, revision)
	rollbackCtx, cancelRollback := context.WithTimeout(
		context.WithoutCancel(ctx),
		runner.config.RollbackTimeout,
	)
	rollbackErr := runner.kube.Rollback(rollbackCtx, target, revision, snapshot)
	if rollbackErr == nil {
		rollbackErr = runner.kube.Wait(rollbackCtx, target, expectationFromSnapshot(snapshot))
	}
	cancelRollback()
	if rollbackErr != nil {
		rollbackMarkerErr = errors.Join(
			rollbackMarkerErr,
			runner.mark(start, faultID, target, PhaseRollback, ResultFailed, revision),
		)
		return errors.Join(cause, markerErr, diagnosticErr, rollbackMarkerErr, rollbackErr)
	}
	steadyErr := runner.waitSteady(
		context.WithoutCancel(ctx),
		start,
		target,
		faultID,
		PhaseSteady,
	)
	recoveredMarkerErr := runner.mark(
		start,
		faultID,
		target,
		PhaseRecovered,
		ResultPassed,
		safeRevision(revision),
	)
	if cause == nil {
		return errors.Join(markerErr, diagnosticErr, rollbackMarkerErr, steadyErr, recoveredMarkerErr)
	}

	return errors.Join(cause, markerErr, diagnosticErr, rollbackMarkerErr, steadyErr, recoveredMarkerErr)
}

func (runner *Runner) mark(
	start time.Time,
	faultID string,
	target Target,
	phase Phase,
	result Result,
	revision string,
) error {
	marker := Marker{
		RunID:     runner.config.RunID,
		FaultID:   faultID,
		DC:        target.DC,
		Zone:      target.Zone,
		Component: target.Component,
		Phase:     phase,
		Result:    result,
		Revision:  safeRevision(revision),
		Offset:    runner.config.Now().Sub(start),
	}
	if marker.Offset < 0 {
		return errors.New("rolling: marker clock moved backwards")
	}
	if err := runner.probe.Mark(marker); err != nil {
		return fmt.Errorf("rolling: recording %s marker: %w", phase, err)
	}

	return nil
}
