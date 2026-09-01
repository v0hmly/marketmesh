package podchaos

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

const (
	requiredPlans         = 2
	requiredStepsPerPlan  = 8
	maxRunIDLength        = 32
	maxDNSLabelLength     = 63
	maxOperationTimeout   = 30 * time.Minute
	maxRecoveryTimeout    = 30 * time.Minute
	maxDiagnosticsTimeout = 10 * time.Minute
)

var (
	// ErrInvalidConfiguration identifies a non-bounded Scenario configuration.
	ErrInvalidConfiguration = errors.New("podchaos: invalid configuration")
	// ErrInvalidExecution identifies an incomplete or unsafe failure matrix.
	ErrInvalidExecution = errors.New("podchaos: invalid execution")
	// ErrUnsafeState identifies a target or capacity snapshot that cannot be
	// used for a one-pod destructive action.
	ErrUnsafeState = errors.New("podchaos: unsafe state")
)

// Scenario coordinates one fault at a time. It owns no goroutines, processes,
// Kubernetes resources or cleanup lifecycle.
type Scenario struct {
	config     Config
	controller PodController
	probe      Probe
	collector  Collector
}

// New validates bounded dependencies and creates a reusable Scenario.
func New(
	config Config,
	controller PodController,
	probe Probe,
	collector Collector,
) (*Scenario, error) {
	if err := validateConfig(config); err != nil {
		return nil, err
	}
	if controller == nil {
		return nil, fmt.Errorf("%w: pod controller is required", ErrInvalidConfiguration)
	}
	if probe == nil {
		return nil, fmt.Errorf("%w: probe is required", ErrInvalidConfiguration)
	}
	if collector == nil {
		return nil, fmt.Errorf("%w: diagnostic collector is required", ErrInvalidConfiguration)
	}

	return &Scenario{
		config:     config,
		controller: controller,
		probe:      probe,
		collector:  collector,
	}, nil
}

// Run executes the complete matrix sequentially and stops after the first
// failed fault. Diagnostics for that fault are collected before Run returns.
func (scenario *Scenario) Run(ctx context.Context, execution Execution) error {
	if ctx == nil {
		return fmt.Errorf("%w: context is required", ErrInvalidExecution)
	}
	faults, err := execution.Faults()
	if err != nil {
		return err
	}

	for _, fault := range faults {
		if err := scenario.runStep(ctx, execution.RunID, fault.ID, fault.Step); err != nil {
			return fmt.Errorf("podchaos: run fault %s: %w", fault.ID, err)
		}
	}
	return nil
}

func (scenario *Scenario) runStep(
	ctx context.Context,
	runID string,
	faultID string,
	step Step,
) error {
	diagnostics := DiagnosticRequest{
		RunID:   runID,
		FaultID: faultID,
		Step:    step,
	}

	err := withTimeout(
		ctx,
		scenario.config.RecoveryTimeout,
		func(callCtx context.Context) error {
			return scenario.probe.WaitSteady(callCtx)
		},
	)
	if err != nil {
		return scenario.finish(ctx, diagnostics, fmt.Errorf("wait for pre-fault probe steady state: %w", err))
	}

	baseline, err := withTimeoutValue(
		ctx,
		scenario.config.OperationTimeout,
		func(callCtx context.Context) (State, error) {
			return scenario.controller.Preflight(callCtx, runID, step)
		},
	)
	if err != nil {
		return scenario.finish(ctx, diagnostics, fmt.Errorf("preflight target: %w", err))
	}
	diagnostics.Pod = baseline.Pod
	if err := validateBaseline(runID, step, baseline); err != nil {
		return scenario.finish(ctx, diagnostics, err)
	}

	startMarker := FaultMarker{
		RunID:   runID,
		FaultID: faultID,
		Step:    step,
		Phase:   MarkerPhaseStarted,
		Status:  MarkerStatusUnknown,
	}
	if err := withTimeout(
		ctx,
		scenario.config.OperationTimeout,
		func(callCtx context.Context) error {
			return scenario.probe.Mark(callCtx, startMarker)
		},
	); err != nil {
		return scenario.finish(ctx, diagnostics, fmt.Errorf("write fault start marker: %w", err))
	}

	stepErr := scenario.deleteAndRecover(ctx, runID, faultID, step, baseline, &diagnostics)
	terminalStatus := MarkerStatusPassed
	if stepErr != nil {
		terminalStatus = MarkerStatusFailed
	}
	terminalMarker := FaultMarker{
		RunID:   runID,
		FaultID: faultID,
		Step:    step,
		Phase:   MarkerPhaseEnded,
		Status:  terminalStatus,
	}
	markerErr := scenario.markTerminal(ctx, terminalMarker)
	if markerErr != nil {
		markerErr = fmt.Errorf("write fault end marker: %w", markerErr)
	}

	return scenario.finish(ctx, diagnostics, errors.Join(stepErr, markerErr))
}

func (scenario *Scenario) deleteAndRecover(
	ctx context.Context,
	runID string,
	faultID string,
	step Step,
	baseline State,
	diagnostics *DiagnosticRequest,
) error {
	deleteRequest := DeleteRequest{
		RunID:       runID,
		FaultID:     faultID,
		Step:        step,
		Pod:         baseline.Pod,
		GracePeriod: scenario.config.DeletionGracePeriod,
	}
	if err := withTimeout(
		ctx,
		scenario.config.OperationTimeout,
		func(callCtx context.Context) error {
			return scenario.controller.Delete(callCtx, deleteRequest)
		},
	); err != nil {
		return fmt.Errorf("delete exact pod: %w", err)
	}
	diagnostics.IsDeleted = true

	recovery, err := withTimeoutValue(
		ctx,
		scenario.config.RecoveryTimeout,
		func(callCtx context.Context) (State, error) {
			return scenario.controller.WaitRecovered(callCtx, RecoveryRequest{
				RunID:    runID,
				FaultID:  faultID,
				Step:     step,
				OldPod:   baseline.Pod,
				Baseline: baseline,
			})
		},
	)
	if err != nil {
		return fmt.Errorf("wait for replacement pod: %w", err)
	}
	diagnostics.Replacement = recovery.Pod
	if err := validateRecovery(runID, baseline, recovery); err != nil {
		return err
	}

	err = withTimeout(
		ctx,
		scenario.config.RecoveryTimeout,
		func(callCtx context.Context) error {
			return scenario.probe.WaitSteady(callCtx)
		},
	)
	if err != nil {
		return fmt.Errorf("wait for recovered probe steady state: %w", err)
	}
	diagnostics.IsRecovered = true
	return nil
}

func (scenario *Scenario) markTerminal(ctx context.Context, marker FaultMarker) error {
	detachedCtx := context.WithoutCancel(ctx)
	return withTimeout(
		detachedCtx,
		scenario.config.OperationTimeout,
		func(callCtx context.Context) error {
			return scenario.probe.Mark(callCtx, marker)
		},
	)
}

func (scenario *Scenario) finish(
	ctx context.Context,
	diagnostics DiagnosticRequest,
	operationErr error,
) error {
	detachedCtx := context.WithoutCancel(ctx)
	diagnosticErr := withTimeout(
		detachedCtx,
		scenario.config.DiagnosticsTimeout,
		func(callCtx context.Context) error {
			return scenario.collector.Collect(callCtx, diagnostics)
		},
	)
	if diagnosticErr != nil {
		diagnosticErr = fmt.Errorf("collect diagnostics: %w", diagnosticErr)
	}
	return errors.Join(operationErr, diagnosticErr)
}

func validateConfig(config Config) error {
	values := []struct {
		name     string
		value    time.Duration
		maxValue time.Duration
	}{
		{name: "operation timeout", value: config.OperationTimeout, maxValue: maxOperationTimeout},
		{name: "recovery timeout", value: config.RecoveryTimeout, maxValue: maxRecoveryTimeout},
		{name: "diagnostics timeout", value: config.DiagnosticsTimeout, maxValue: maxDiagnosticsTimeout},
		{name: "deletion grace period", value: config.DeletionGracePeriod, maxValue: maxOperationTimeout},
	}
	for _, value := range values {
		if value.value <= 0 || value.value > value.maxValue {
			return fmt.Errorf(
				"%w: %s must be between zero and %s",
				ErrInvalidConfiguration,
				value.name,
				value.maxValue,
			)
		}
	}
	if config.DeletionGracePeriod%time.Second != 0 {
		return fmt.Errorf(
			"%w: deletion grace period must use whole seconds",
			ErrInvalidConfiguration,
		)
	}
	return nil
}

func validateExecution(execution Execution) error {
	if !isMM32RunID(execution.RunID) {
		return fmt.Errorf(
			"%w: run id must be a DNS label with the mm32- prefix",
			ErrInvalidExecution,
		)
	}
	if len(execution.Plans) != requiredPlans {
		return fmt.Errorf(
			"%w: plans = %d, want %d",
			ErrInvalidExecution,
			len(execution.Plans),
			requiredPlans,
		)
	}

	planIDs := make(map[string]struct{}, len(execution.Plans))
	for _, plan := range execution.Plans {
		if !isDNSLabel(plan.ID) {
			return fmt.Errorf("%w: plan id is not a DNS label", ErrInvalidExecution)
		}
		if _, exists := planIDs[plan.ID]; exists {
			return fmt.Errorf("%w: duplicate plan id %q", ErrInvalidExecution, plan.ID)
		}
		planIDs[plan.ID] = struct{}{}
		if err := validatePlan(plan); err != nil {
			return err
		}
		for index, step := range plan.Steps {
			if !isDNSLabel(faultID(plan.ID, index, step)) {
				return fmt.Errorf(
					"%w: plan %q produces an invalid fault id",
					ErrInvalidExecution,
					plan.ID,
				)
			}
		}
	}
	if !isReverseOrder(execution.Plans[0].Steps, execution.Plans[1].Steps) {
		return fmt.Errorf("%w: second fault order must reverse the first", ErrInvalidExecution)
	}
	return nil
}

func isReverseOrder(first, second []Step) bool {
	if len(first) != len(second) {
		return false
	}
	for index, step := range first {
		if step != second[len(second)-index-1] {
			return false
		}
	}
	return true
}

func validatePlan(plan Plan) error {
	if len(plan.Steps) != requiredStepsPerPlan {
		return fmt.Errorf(
			"%w: plan %q steps = %d, want %d",
			ErrInvalidExecution,
			plan.ID,
			len(plan.Steps),
			requiredStepsPerPlan,
		)
	}
	seen := make(map[Step]struct{}, requiredStepsPerPlan)
	for _, step := range plan.Steps {
		if !validStep(step) {
			return fmt.Errorf("%w: plan %q contains an unknown step", ErrInvalidExecution, plan.ID)
		}
		if _, exists := seen[step]; exists {
			return fmt.Errorf("%w: plan %q contains a duplicate step", ErrInvalidExecution, plan.ID)
		}
		seen[step] = struct{}{}
	}
	return nil
}

func validStep(step Step) bool {
	validDC := step.DC == DCA || step.DC == DCB
	validComponent := step.Component == ComponentGatewayIn || step.Component == ComponentGatewayOut
	validRole := step.Role == RoleActive || step.Role == RoleStandby
	return validDC && validComponent && validRole
}

func validateBaseline(runID string, step Step, state State) error {
	if state.Selected != step {
		return fmt.Errorf("%w: controller resolved a different target", ErrUnsafeState)
	}
	if err := validatePodRef(runID, state.Pod); err != nil {
		return err
	}
	if !state.IsPodReady || !state.IsTunnelReady || state.IsRolling {
		return fmt.Errorf("%w: target pod is not in a stable ready state", ErrUnsafeState)
	}
	if state.DesiredReplicas < 2 {
		return fmt.Errorf("%w: deployment requires at least two replicas", ErrUnsafeState)
	}
	if err := validatePodSet(runID, state.Pod, state.Pods, state.DesiredReplicas); err != nil {
		return err
	}
	if state.ReadyReplicas != state.DesiredReplicas || state.AvailableReplicas != state.DesiredReplicas {
		return fmt.Errorf("%w: deployment capacity is not fully restored", ErrUnsafeState)
	}
	if state.HealthyPaths < 1 ||
		state.HealthyPathsWithoutPod < 1 ||
		state.HealthyPathsWithoutPod > state.HealthyPaths {
		return fmt.Errorf("%w: deleting the pod would remove all healthy paths", ErrUnsafeState)
	}
	return nil
}

func validateRecovery(runID string, baseline State, recovery State) error {
	if recovery.Selected != baseline.Selected {
		return fmt.Errorf("%w: replacement resolved a different target", ErrUnsafeState)
	}
	if err := validatePodRef(runID, recovery.Pod); err != nil {
		return err
	}
	if recovery.Pod.UID == baseline.Pod.UID {
		return fmt.Errorf("%w: replacement pod reused the deleted uid", ErrUnsafeState)
	}
	if recovery.Pod.Deployment != baseline.Pod.Deployment ||
		recovery.Pod.Namespace != baseline.Pod.Namespace ||
		recovery.Pod.ContextName != baseline.Pod.ContextName ||
		recovery.Pod.KubeconfigPath != baseline.Pod.KubeconfigPath {
		return fmt.Errorf("%w: replacement escaped the validated deployment", ErrUnsafeState)
	}
	if err := validatePodSet(runID, recovery.Pod, recovery.Pods, recovery.DesiredReplicas); err != nil {
		return err
	}
	if !recovery.IsPodReady || !recovery.IsTunnelReady || recovery.IsRolling {
		return fmt.Errorf("%w: replacement pod is not in a stable ready state", ErrUnsafeState)
	}
	if recovery.DesiredReplicas != baseline.DesiredReplicas ||
		recovery.ReadyReplicas != baseline.DesiredReplicas ||
		recovery.AvailableReplicas != baseline.DesiredReplicas ||
		recovery.HealthyPaths < baseline.HealthyPaths {
		return fmt.Errorf("%w: replacement did not restore baseline capacity", ErrUnsafeState)
	}
	return nil
}

func validatePodSet(
	runID string,
	selected PodRef,
	pods []PodRef,
	desired int32,
) error {
	if desired < 0 || len(pods) != int(desired) {
		return fmt.Errorf("%w: pod inventory does not match desired replicas", ErrUnsafeState)
	}
	names := make(map[string]struct{}, len(pods))
	uids := make(map[string]struct{}, len(pods))
	isSelectedPresent := false
	for _, pod := range pods {
		if err := validatePodRef(runID, pod); err != nil {
			return err
		}
		if pod.KubeconfigPath != selected.KubeconfigPath ||
			pod.ContextName != selected.ContextName ||
			pod.Namespace != selected.Namespace ||
			pod.Deployment != selected.Deployment {
			return fmt.Errorf("%w: pod inventory crosses deployment boundary", ErrUnsafeState)
		}
		if _, exists := names[pod.Name]; exists {
			return fmt.Errorf("%w: pod inventory contains a duplicate name", ErrUnsafeState)
		}
		if _, exists := uids[pod.UID]; exists {
			return fmt.Errorf("%w: pod inventory contains a duplicate uid", ErrUnsafeState)
		}
		names[pod.Name] = struct{}{}
		uids[pod.UID] = struct{}{}
		if pod == selected {
			isSelectedPresent = true
		}
	}
	if !isSelectedPresent {
		return fmt.Errorf("%w: selected pod is missing from inventory", ErrUnsafeState)
	}
	return nil
}

func validatePodRef(runID string, pod PodRef) error {
	if !isMM32RunID(runID) || pod.OwnerRunID != runID {
		return fmt.Errorf("%w: pod ownership does not match run id", ErrUnsafeState)
	}
	if !filepath.IsAbs(pod.KubeconfigPath) || filepath.Clean(pod.KubeconfigPath) != pod.KubeconfigPath {
		return fmt.Errorf("%w: kubeconfig path must be absolute and clean", ErrUnsafeState)
	}
	if pod.KubeconfigPath == string(filepath.Separator) {
		return fmt.Errorf("%w: kubeconfig path cannot be filesystem root", ErrUnsafeState)
	}
	if !isExactArgument(pod.ContextName) {
		return fmt.Errorf("%w: kube context is not an exact bounded argument", ErrUnsafeState)
	}
	for _, value := range []string{pod.Namespace, pod.Deployment, pod.Name} {
		if !isDNSSubdomain(value) {
			return fmt.Errorf("%w: Kubernetes name is invalid", ErrUnsafeState)
		}
	}
	if !isExactArgument(pod.UID) {
		return fmt.Errorf("%w: pod uid is invalid", ErrUnsafeState)
	}
	return nil
}

func faultID(planID string, index int, step Step) string {
	return fmt.Sprintf(
		"%s-%02d-%s-%s-%s",
		planID,
		index+1,
		step.DC,
		step.Component,
		step.Role,
	)
}

func isDNSLabel(value string) bool {
	if len(value) == 0 || len(value) > maxDNSLabelLength {
		return false
	}
	for index, char := range value {
		isAlphaNumeric := char >= 'a' && char <= 'z' || char >= '0' && char <= '9'
		if !isAlphaNumeric && char != '-' {
			return false
		}
		if char == '-' && (index == 0 || index == len(value)-1) {
			return false
		}
	}
	return true
}

func isMM32RunID(value string) bool {
	return len(value) <= maxRunIDLength &&
		isDNSLabel(value) &&
		strings.HasPrefix(value, "mm32-")
}

func hasDeadline(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	_, ok := ctx.Deadline()
	return ok
}

func isDNSSubdomain(value string) bool {
	if len(value) == 0 || len(value) > 253 {
		return false
	}
	for label := range strings.SplitSeq(value, ".") {
		if !isDNSLabel(label) {
			return false
		}
	}
	return true
}

func isExactArgument(value string) bool {
	if len(value) == 0 || len(value) > 253 || strings.HasPrefix(value, "-") {
		return false
	}
	for _, char := range value {
		isAlphaNumeric := char >= 'a' && char <= 'z' ||
			char >= 'A' && char <= 'Z' ||
			char >= '0' && char <= '9'
		if !isAlphaNumeric && !strings.ContainsRune("-._:/@", char) {
			return false
		}
	}
	return true
}

func withTimeout(
	parent context.Context,
	timeout time.Duration,
	operation func(context.Context) error,
) error {
	callCtx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	return operation(callCtx)
}

func withTimeoutValue[T any](
	parent context.Context,
	timeout time.Duration,
	operation func(context.Context) (T, error),
) (T, error) {
	callCtx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	return operation(callCtx)
}
