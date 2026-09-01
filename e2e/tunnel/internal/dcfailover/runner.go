package dcfailover

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
	"time"
)

// Runner executes each outage kind for both DCs and owns finalization order.
// A Runner is single-use and rejects concurrent or repeated Run calls.
type Runner struct {
	config    Config
	topology  Topology
	drainer   Drainer
	frontDoor FrontDoor
	readiness Readiness
	probe     Probe
	started   atomic.Bool
}

// Dependencies are the narrow prerequisite contracts consumed by Runner.
type Dependencies struct {
	Topology  Topology
	Drainer   Drainer
	FrontDoor FrontDoor
	Readiness Readiness
	Probe     Probe
}

// New creates a single-use failover runner.
func New(config Config, dependencies Dependencies) (*Runner, error) {
	if err := validateConfig(config); err != nil {
		return nil, err
	}
	if dependencies.Topology == nil || dependencies.Drainer == nil ||
		dependencies.FrontDoor == nil || dependencies.Readiness == nil ||
		dependencies.Probe == nil {
		return nil, errors.New("dcfailover: all adapters are required")
	}

	return &Runner{
		config:    config,
		topology:  dependencies.Topology,
		drainer:   dependencies.Drainer,
		frontDoor: dependencies.FrontDoor,
		readiness: dependencies.Readiness,
		probe:     dependencies.Probe,
	}, nil
}

// Run performs managed and sudden outages for DC-A and then DC-B.
func (runner *Runner) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("dcfailover: run context must not be nil")
	}
	if !runner.started.CompareAndSwap(false, true) {
		return errors.New("dcfailover: runner is single-use")
	}

	snapshot, err := runWithTimeout(
		ctx,
		runner.config.PhaseTimeout,
		func(phaseCtx context.Context) (Snapshot, error) {
			return runner.topology.Preflight(phaseCtx, runner.config.RunID)
		},
	)
	if err != nil {
		return fmt.Errorf("dcfailover: topology preflight: %w", err)
	}
	snapshot, err = validateSnapshot(snapshot, runner.config.RunID)
	if err != nil {
		return err
	}

	return runner.runValidated(ctx, snapshot)
}

func (runner *Runner) runValidated(ctx context.Context, snapshot Snapshot) (resultErr error) {
	probeStarted := false
	defer func() {
		resultErr = errors.Join(
			resultErr,
			runner.finalize(ctx, snapshot, probeStarted),
		)
	}()

	if err := runner.phase(ctx, "waiting for initial baseline", runner.readiness.WaitBaseline); err != nil {
		return err
	}
	if err := runner.phase(ctx, "starting probe", runner.probe.Start); err != nil {
		return err
	}
	probeStarted = true

	for _, dc := range []DC{DCA, DCB} {
		target, err := targetForDC(snapshot, dc)
		if err != nil {
			return err
		}
		for _, kind := range []OutageKind{OutageManaged, OutageSudden} {
			if err := runner.runOutage(ctx, target, kind); err != nil {
				return err
			}
		}
	}

	return nil
}

func (runner *Runner) runOutage(
	ctx context.Context,
	target DCTarget,
	kind OutageKind,
) error {
	dc := target.DC
	if err := runner.phase(ctx, "waiting for survivor readiness", func(phaseCtx context.Context) error {
		return runner.readiness.WaitSurvivor(phaseCtx, otherDC(dc))
	}); err != nil {
		return err
	}
	if err := runner.mark(ctx, dc, kind, MarkerFaultStarted); err != nil {
		return err
	}

	if kind == OutageManaged {
		if err := runner.phase(ctx, "draining dc", func(phaseCtx context.Context) error {
			return runner.drainer.DrainDC(phaseCtx, dc)
		}); err != nil {
			return err
		}
		if err := runner.refreshFrontDoor(ctx); err != nil {
			return err
		}
		if err := runner.waitExcluded(ctx, dc); err != nil {
			return err
		}
		if err := runner.mark(ctx, dc, kind, MarkerDCExcluded); err != nil {
			return err
		}
	}

	if err := runner.phase(ctx, "stopping exact dc resources", func(phaseCtx context.Context) error {
		return runner.topology.StopDC(phaseCtx, cloneTarget(target), kind)
	}); err != nil {
		return err
	}
	if kind == OutageSudden {
		if err := runner.refreshFrontDoor(ctx); err != nil {
			return err
		}
		if err := runner.waitExcluded(ctx, dc); err != nil {
			return err
		}
		if err := runner.mark(ctx, dc, kind, MarkerDCExcluded); err != nil {
			return err
		}
	}

	if err := runner.phase(ctx, "restoring exact dc resources", func(phaseCtx context.Context) error {
		return runner.topology.RestoreDC(phaseCtx, cloneTarget(target))
	}); err != nil {
		return err
	}
	if err := runner.mark(ctx, dc, kind, MarkerDCRestored); err != nil {
		return err
	}
	if err := runner.phase(ctx, "waiting for restored dc readiness", func(phaseCtx context.Context) error {
		return runner.readiness.WaitRestored(phaseCtx, dc)
	}); err != nil {
		return err
	}
	if err := runner.refreshFrontDoor(ctx); err != nil {
		return err
	}
	if err := runner.phase(ctx, "waiting for dc eligibility", func(phaseCtx context.Context) error {
		return runner.readiness.WaitEligible(phaseCtx, dc)
	}); err != nil {
		return err
	}
	if err := runner.mark(ctx, dc, kind, MarkerDCEligible); err != nil {
		return err
	}

	return runner.phase(ctx, "waiting for restored baseline", runner.readiness.WaitBaseline)
}

func (runner *Runner) waitExcluded(ctx context.Context, dc DC) error {
	return runner.phase(ctx, "waiting for dc exclusion", func(phaseCtx context.Context) error {
		return runner.readiness.WaitExcluded(phaseCtx, dc)
	})
}

func (runner *Runner) refreshFrontDoor(ctx context.Context) error {
	return runner.phase(ctx, "refreshing front door health", func(phaseCtx context.Context) error {
		runner.frontDoor.Check(phaseCtx)

		return phaseCtx.Err()
	})
}

func (runner *Runner) mark(
	ctx context.Context,
	dc DC,
	kind OutageKind,
	phase MarkerPhase,
) error {
	return runner.phase(ctx, "recording probe marker", func(phaseCtx context.Context) error {
		return runner.probe.Mark(phaseCtx, Marker{
			DC:         dc,
			OutageKind: kind,
			Phase:      phase,
		})
	})
}

func (runner *Runner) phase(
	ctx context.Context,
	name string,
	operation func(context.Context) error,
) error {
	_, err := runWithTimeout(
		ctx,
		runner.config.PhaseTimeout,
		func(phaseCtx context.Context) (struct{}, error) {
			return struct{}{}, operation(phaseCtx)
		},
	)
	if err != nil {
		return fmt.Errorf("dcfailover: %s: %w", name, err)
	}

	return nil
}

func (runner *Runner) finalize(
	ctx context.Context,
	snapshot Snapshot,
	probeStarted bool,
) error {
	errs := []error{}
	if probeStarted {
		errs = append(errs, runner.finalizeStep(ctx, "stopping probe", runner.probe.Stop))
		errs = append(errs, runner.finalizeStep(ctx, "verifying probe", runner.probe.Verify))
	}
	errs = append(errs, runner.finalizeStep(ctx, "collecting diagnostics", func(finalizeCtx context.Context) error {
		return runner.topology.Inspect(finalizeCtx, cloneSnapshot(snapshot))
	}))
	errs = append(errs, runner.finalizeStep(ctx, "cleaning exact resources", func(finalizeCtx context.Context) error {
		return runner.topology.Cleanup(finalizeCtx, cloneSnapshot(snapshot))
	}))

	return errors.Join(errs...)
}

func (runner *Runner) finalizeStep(
	ctx context.Context,
	name string,
	operation func(context.Context) error,
) error {
	finalizeCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx),
		runner.config.FinalizeTimeout,
	)
	defer cancel()

	if err := operation(finalizeCtx); err != nil {
		return fmt.Errorf("dcfailover: %s: %w", name, err)
	}

	return nil
}

func runWithTimeout[T any](
	ctx context.Context,
	timeoutDuration time.Duration,
	operation func(context.Context) (T, error),
) (T, error) {
	phaseCtx, cancel := context.WithTimeout(ctx, timeoutDuration)
	defer cancel()

	return operation(phaseCtx)
}

func cloneTarget(target DCTarget) DCTarget {
	return DCTarget{DC: target.DC, Clusters: cloneClusters(target.Clusters)}
}

func cloneSnapshot(snapshot Snapshot) Snapshot {
	return Snapshot{
		RunID:       snapshot.RunID,
		Environment: snapshot.Environment,
		Disposable:  snapshot.Disposable,
		Clusters:    cloneClusters(snapshot.Clusters),
	}
}

func cloneClusters(clusters []Cluster) []Cluster {
	cloned := make([]Cluster, len(clusters))
	for index, cluster := range clusters {
		cluster.ContainerNames = slices.Clone(cluster.ContainerNames)
		cluster.NetworkNames = slices.Clone(cluster.NetworkNames)
		cloned[index] = cluster
	}

	return cloned
}
