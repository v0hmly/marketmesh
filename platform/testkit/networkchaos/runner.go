package networkchaos

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"net/netip"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"time"
)

// ErrRunnerUsed обозначает повторный или конкурентный запуск одноразового Runner.
var ErrRunnerUsed = errors.New("networkchaos: runner has already been used")

// Runner последовательно применяет Plan, не создаёт goroutines и всегда
// пытается восстановить уже применённые faults перед возвратом.
type Runner struct {
	config      Config
	driver      Driver
	capacity    CapacitySource
	diagnostics Diagnostics
	observer    Observer
	waiter      waiter
	used        atomic.Bool
}

// snapshotScopeValidator is deliberately private: only package-owned drivers
// may replace the fixed MM-36 resource-label gate with an equally strict
// versioned ownership contract.
type snapshotScopeValidator interface {
	validateSnapshotScope(runID string, fault Fault, snapshot Snapshot) error
}

// New создаёт Runner с real-time cancellable waiter.
func New(
	config Config,
	driver Driver,
	capacity CapacitySource,
	diagnostics Diagnostics,
) (*Runner, error) {
	return newWithWaiter(
		config,
		driver,
		capacity,
		diagnostics,
		nil,
		timerWaiter{},
	)
}

// NewObserved создаёт Runner с bounded lifecycle observer для continuous probe.
func NewObserved(
	config Config,
	driver Driver,
	capacity CapacitySource,
	diagnostics Diagnostics,
	observer Observer,
) (*Runner, error) {
	if isNilDependency(observer) {
		return nil, errors.New("networkchaos: observer must not be nil")
	}
	return newWithWaiter(
		config,
		driver,
		capacity,
		diagnostics,
		observer,
		timerWaiter{},
	)
}

func newWithWaiter(
	config Config,
	driver Driver,
	capacity CapacitySource,
	diagnostics Diagnostics,
	observer Observer,
	waiter waiter,
) (*Runner, error) {
	if err := validateConfig(config); err != nil {
		return nil, err
	}
	if isNilDependency(driver) {
		return nil, errors.New("networkchaos: driver must not be nil")
	}
	if isNilDependency(capacity) {
		return nil, errors.New("networkchaos: capacity source must not be nil")
	}
	if isNilDependency(diagnostics) {
		return nil, errors.New("networkchaos: diagnostics must not be nil")
	}
	if isNilDependency(waiter) {
		return nil, errors.New("networkchaos: waiter must not be nil")
	}

	return &Runner{
		config:      config,
		driver:      driver,
		capacity:    capacity,
		diagnostics: diagnostics,
		observer:    observer,
		waiter:      waiter,
	}, nil
}

func isNilDependency(value any) bool {
	if value == nil {
		return true
	}
	kind := reflect.ValueOf(value).Kind()
	switch kind {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflect.ValueOf(value).IsNil()
	default:
		return false
	}
}

// Run проверяет и выполняет Plan. Ошибки diagnostics и cleanup сохраняются
// через errors.Join; логировать итог должен только вызывающий E2E harness.
func (runner *Runner) Run(ctx context.Context, plan Plan) error {
	if ctx == nil {
		return errors.New("networkchaos: context must not be nil")
	}
	if err := validatePlan(runner.config, plan); err != nil {
		return err
	}
	if !runner.used.CompareAndSwap(false, true) {
		return ErrRunnerUsed
	}

	for stepIndex, step := range plan.Steps {
		if err := runner.runStep(ctx, plan.Seed, stepIndex, step); err != nil {
			return fmt.Errorf("networkchaos: running step %q: %w", step.Name, err)
		}
	}

	return nil
}

func (runner *Runner) runStep(
	ctx context.Context,
	seed int64,
	stepIndex int,
	step Step,
) error {
	ready, err := runner.readyCapacity(ctx)
	if err != nil {
		return err
	}
	if err := runner.validateCapacity(step, ready); err != nil {
		return err
	}

	type appliedFault struct {
		fault   Fault
		restore RestoreFunc
	}
	applied := make([]appliedFault, 0, len(step.Faults))
	var runErr error
	for faultIndex, fault := range step.Faults {
		if observeErr := runner.observe(ctx, Observation{
			Seed:       seed,
			StepIndex:  stepIndex,
			StepName:   step.Name,
			FaultIndex: faultIndex,
			FaultCount: len(step.Faults),
			FaultName:  fault.Name,
			FaultKind:  fault.Kind,
			Phase:      ObservationPhaseBefore,
		}); observeErr != nil {
			runErr = observeErr
			break
		}
	}
	for faultIndex, fault := range step.Faults {
		if runErr != nil {
			break
		}
		snapshot, inspectErr := runner.inspect(ctx, fault)
		if inspectErr != nil {
			runErr = inspectErr
			break
		}
		if scopeErr := runner.validateSnapshotScope(fault, snapshot); scopeErr != nil {
			runErr = scopeErr
			break
		}

		restore, applyErr := runner.apply(ctx, snapshot, fault)
		if restore != nil {
			applied = append(applied, appliedFault{fault: fault, restore: restore})
		}
		if applyErr != nil {
			runErr = fmt.Errorf("applying fault %q: %w", fault.Name, applyErr)
			break
		}
		if restore == nil {
			runErr = fmt.Errorf("applying fault %q: driver returned nil restore", fault.Name)
			break
		}
		if observeErr := runner.observe(ctx, Observation{
			Seed:       seed,
			StepIndex:  stepIndex,
			StepName:   step.Name,
			FaultIndex: faultIndex,
			FaultCount: len(step.Faults),
			FaultName:  fault.Name,
			FaultKind:  fault.Kind,
			Phase:      ObservationPhaseActive,
		}); observeErr != nil {
			runErr = observeErr
			break
		}
	}

	phase := PhaseFaulted
	if runErr != nil {
		phase = PhaseFailed
	} else {
		ready, capacityErr := runner.readyCapacity(ctx)
		if capacityErr != nil {
			runErr = capacityErr
			phase = PhaseFailed
		} else if ready < runner.config.MinimumCapacity {
			runErr = fmt.Errorf(
				"ready capacity %d is below required minimum %d after fault",
				ready,
				runner.config.MinimumCapacity,
			)
			phase = PhaseFailed
		} else if waitErr := runner.waiter.Wait(ctx, step.Hold); waitErr != nil {
			runErr = fmt.Errorf("holding faults: %w", waitErr)
			phase = PhaseFailed
		}
	}

	point := DiagnosticPoint{
		Seed:      seed,
		StepIndex: stepIndex,
		StepName:  step.Name,
		Phase:     phase,
	}
	diagnosticsErr := runner.capture(context.WithoutCancel(ctx), point)
	var restoreErr error
	var recoveryErr error
	var observerErr error
	if len(applied) > 0 {
		restores := make([]RestoreFunc, 0, len(applied))
		for _, item := range applied {
			restores = append(restores, item.restore)
		}
		restoreErr = runner.restore(context.WithoutCancel(ctx), restores)
		recoveryErr = runner.waitForRecovery(context.WithoutCancel(ctx))
		if restoreErr == nil && recoveryErr == nil {
			for faultIndex, item := range applied {
				observerErr = errors.Join(observerErr, runner.observe(
					context.WithoutCancel(ctx),
					Observation{
						Seed:       seed,
						StepIndex:  stepIndex,
						StepName:   step.Name,
						FaultIndex: faultIndex,
						FaultCount: len(applied),
						FaultName:  item.fault.Name,
						FaultKind:  item.fault.Kind,
						Phase:      ObservationPhaseRecovered,
					},
				))
			}
		}
	}
	if len(applied) > 0 && recoveryErr == nil && observerErr == nil {
		recoveredPoint := point
		recoveredPoint.Phase = PhaseRecovered
		diagnosticsErr = errors.Join(
			diagnosticsErr,
			runner.capture(context.WithoutCancel(ctx), recoveredPoint),
		)
	}

	return errors.Join(runErr, diagnosticsErr, restoreErr, recoveryErr, observerErr)
}

func (runner *Runner) validateCapacity(step Step, ready uint) error {
	if ready < runner.config.MinimumCapacity {
		return fmt.Errorf(
			"ready capacity %d is below required minimum %d before fault",
			ready,
			runner.config.MinimumCapacity,
		)
	}

	availableLoss := ready - runner.config.MinimumCapacity
	var declaredLoss uint
	for _, fault := range step.Faults {
		if fault.CapacityLoss > availableLoss-declaredLoss {
			return fmt.Errorf(
				"step %q declares capacity loss beyond safe budget %d",
				step.Name,
				availableLoss,
			)
		}
		declaredLoss += fault.CapacityLoss
	}

	return nil
}

func (runner *Runner) inspect(ctx context.Context, fault Fault) (Snapshot, error) {
	operationCtx, cancel := context.WithTimeout(ctx, runner.config.OperationTimeout)
	defer cancel()

	snapshot, err := runner.driver.Inspect(operationCtx, fault)
	if err != nil {
		return Snapshot{}, fmt.Errorf("inspecting fault %q resources: %w", fault.Name, err)
	}

	return cloneSnapshot(snapshot), nil
}

func (runner *Runner) apply(
	ctx context.Context,
	snapshot Snapshot,
	fault Fault,
) (RestoreFunc, error) {
	operationCtx, cancel := context.WithTimeout(ctx, runner.config.OperationTimeout)
	defer cancel()

	return runner.driver.Apply(operationCtx, snapshot, fault)
}

func (runner *Runner) readyCapacity(ctx context.Context) (uint, error) {
	operationCtx, cancel := context.WithTimeout(ctx, runner.config.OperationTimeout)
	defer cancel()

	ready, err := runner.capacity.Ready(operationCtx)
	if err != nil {
		return 0, fmt.Errorf("reading ready capacity: %w", err)
	}

	return ready, nil
}

func (runner *Runner) capture(ctx context.Context, point DiagnosticPoint) error {
	diagnosticsCtx, cancel := context.WithTimeout(ctx, runner.config.DiagnosticsTimeout)
	defer cancel()

	if err := runner.diagnostics.Capture(diagnosticsCtx, point); err != nil {
		return fmt.Errorf("capturing %s diagnostics for step %q: %w", point.Phase, point.StepName, err)
	}

	return nil
}

func (runner *Runner) observe(ctx context.Context, observation Observation) error {
	if runner.observer == nil {
		return nil
	}
	observerCtx, cancel := context.WithTimeout(ctx, runner.config.RecoveryTimeout)
	defer cancel()

	if err := runner.observer.Observe(observerCtx, observation); err != nil {
		return fmt.Errorf(
			"observing %s phase for fault %q: %w",
			observation.Phase,
			observation.FaultName,
			err,
		)
	}
	return nil
}

func (runner *Runner) validateSnapshotScope(fault Fault, snapshot Snapshot) error {
	validator, ok := runner.driver.(snapshotScopeValidator)
	if !ok {
		return validateSnapshot(runner.config.RunID, fault, snapshot)
	}
	return validator.validateSnapshotScope(runner.config.RunID, fault, snapshot)
}

func (runner *Runner) restore(ctx context.Context, restores []RestoreFunc) error {
	restoreErrors := make([]error, 0)
	for index, restore := range slices.Backward(restores) {
		restoreCtx, cancel := context.WithTimeout(ctx, runner.config.RestoreTimeout)
		err := restore(restoreCtx)
		cancel()
		if err != nil {
			restoreErrors = append(restoreErrors, fmt.Errorf("restoring fault %d: %w", index, err))
		}
	}

	return errors.Join(restoreErrors...)
}

func (runner *Runner) waitForRecovery(ctx context.Context) error {
	recoveryCtx, cancel := context.WithTimeout(ctx, runner.config.RecoveryTimeout)
	defer cancel()

	ticker := time.NewTicker(runner.config.PollInterval)
	defer ticker.Stop()

	var lastErr error
	for {
		ready, err := runner.capacity.Ready(recoveryCtx)
		if err == nil && ready >= runner.config.MinimumCapacity {
			return nil
		}
		if err != nil {
			lastErr = err
		} else {
			lastErr = fmt.Errorf(
				"ready capacity %d is below required minimum %d",
				ready,
				runner.config.MinimumCapacity,
			)
		}

		select {
		case <-recoveryCtx.Done():
			return fmt.Errorf("waiting for steady state: %w: %v", recoveryCtx.Err(), lastErr)
		case <-ticker.C:
		}
	}
}

func validateSnapshot(runID string, fault Fault, snapshot Snapshot) error {
	if snapshot.Interface != fault.Interface {
		return fmt.Errorf(
			"networkchaos: inspected interface %q differs from requested %q",
			snapshot.Interface,
			fault.Interface,
		)
	}
	if err := validateResource(
		runID,
		"container",
		fault.Container,
		snapshot.Container,
	); err != nil {
		return err
	}
	if err := validateNetwork(
		runID,
		"network",
		fault.Network,
		snapshot.Network,
	); err != nil {
		return err
	}
	if len(snapshot.PeerNetworks) != len(fault.PeerNetworks) {
		return errors.New("networkchaos: inspected peer network count differs from requested count")
	}
	for index, peer := range fault.PeerNetworks {
		if err := validateNetwork(
			runID,
			"peer network",
			peer,
			snapshot.PeerNetworks[index],
		); err != nil {
			return err
		}
	}

	return nil
}

func validateNetwork(
	runID string,
	kind string,
	ref ResourceRef,
	network Network,
) error {
	if err := validateResource(
		runID,
		kind,
		ref,
		network.Resource,
	); err != nil {
		return err
	}
	if len(network.Prefixes) == 0 {
		return fmt.Errorf("networkchaos: %s %q has no inspected prefixes", kind, ref.Name)
	}
	for _, prefix := range network.Prefixes {
		if !isPrivatePrefix(prefix) {
			return fmt.Errorf(
				"networkchaos: %s %q prefix %q is not a private test subnet",
				kind,
				ref.Name,
				prefix,
			)
		}
	}

	return nil
}

func validateResource(
	runID string,
	kind string,
	ref ResourceRef,
	resource Resource,
) error {
	if resource.ID != ref.ID || resource.Name != ref.Name {
		return fmt.Errorf("networkchaos: inspected %s differs from exact requested resource", kind)
	}
	if !strings.HasPrefix(resource.Name, runID+"-") {
		return fmt.Errorf(
			"networkchaos: %s %q is outside run prefix %q",
			kind,
			resource.Name,
			runID+"-",
		)
	}
	if resource.Labels[TaskLabel] != TaskKey {
		return fmt.Errorf("networkchaos: %s %q has invalid task label", kind, resource.Name)
	}
	if resource.Labels[RunLabel] != runID {
		return fmt.Errorf("networkchaos: %s %q has invalid run label", kind, resource.Name)
	}
	if resource.Labels[DisposableLabel] != "true" {
		return fmt.Errorf("networkchaos: %s %q is not explicitly disposable", kind, resource.Name)
	}

	return nil
}

func isPrivatePrefix(prefix netip.Prefix) bool {
	if !prefix.IsValid() || prefix != prefix.Masked() {
		return false
	}
	for _, privatePrefix := range privatePrefixes {
		isSameAddressFamily := privatePrefix.Addr().BitLen() == prefix.Addr().BitLen()
		if isSameAddressFamily &&
			privatePrefix.Bits() <= prefix.Bits() &&
			privatePrefix.Contains(prefix.Addr()) {
			return true
		}
	}

	return false
}

func cloneSnapshot(snapshot Snapshot) Snapshot {
	cloneResource := func(resource Resource) Resource {
		labels := make(map[string]string, len(resource.Labels))
		maps.Copy(labels, resource.Labels)
		resource.Labels = labels
		return resource
	}
	cloneNetwork := func(network Network) Network {
		network.Resource = cloneResource(network.Resource)
		network.Prefixes = append([]netip.Prefix{}, network.Prefixes...)
		return network
	}

	snapshot.Container = cloneResource(snapshot.Container)
	snapshot.Network = cloneNetwork(snapshot.Network)
	peerNetworks := make([]Network, len(snapshot.PeerNetworks))
	for index, network := range snapshot.PeerNetworks {
		peerNetworks[index] = cloneNetwork(network)
	}
	snapshot.PeerNetworks = peerNetworks

	return snapshot
}
