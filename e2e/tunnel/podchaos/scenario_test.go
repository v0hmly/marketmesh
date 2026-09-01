package podchaos

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestDefaultExecutionCoversCompleteOppositeOrders(t *testing.T) {
	t.Parallel()

	execution := DefaultExecution("mm32-matrix")
	if err := validateExecution(execution); err != nil {
		t.Fatalf("validate default execution: %v", err)
	}
	if execution.Plans[0].Steps[0] != (Step{
		DC:        DCA,
		Component: ComponentGatewayIn,
		Role:      RoleActive,
	}) {
		t.Fatalf("first plan starts with %+v", execution.Plans[0].Steps[0])
	}
	if execution.Plans[1].Steps[0] != (Step{
		DC:        DCB,
		Component: ComponentGatewayOut,
		Role:      RoleStandby,
	}) {
		t.Fatalf("second plan starts with %+v", execution.Plans[1].Steps[0])
	}

	for _, plan := range execution.Plans {
		seen := make(map[Step]struct{}, requiredStepsPerPlan)
		for _, step := range plan.Steps {
			seen[step] = struct{}{}
		}
		if len(seen) != requiredStepsPerPlan {
			t.Fatalf("plan %q unique steps = %d", plan.ID, len(seen))
		}
	}
	if !isReverseOrder(execution.Plans[0].Steps, execution.Plans[1].Steps) {
		t.Fatal("default plans are not exact reverse orders")
	}

	faults, err := execution.Faults()
	if err != nil {
		t.Fatalf("Faults() error = %v", err)
	}
	if len(faults) != requiredPlans*requiredStepsPerPlan {
		t.Fatalf("Faults() count = %d", len(faults))
	}
	seenFaultIDs := make(map[string]struct{}, len(faults))
	for _, fault := range faults {
		seenFaultIDs[fault.ID] = struct{}{}
	}
	if len(seenFaultIDs) != len(faults) {
		t.Fatalf("Faults() unique IDs = %d, want %d", len(seenFaultIDs), len(faults))
	}
}

func TestScenarioRunsFaultsSequentiallyAndCollectsDiagnostics(t *testing.T) {
	t.Parallel()

	adapters := newFakeAdapters()
	scenario := newTestScenario(t, adapters)
	if err := scenario.Run(t.Context(), DefaultExecution("mm32-success")); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	const totalFaults = requiredPlans * requiredStepsPerPlan
	if len(adapters.deleted) != totalFaults {
		t.Fatalf("deleted pods = %d, want %d", len(adapters.deleted), totalFaults)
	}
	if len(adapters.diagnostics) != totalFaults {
		t.Fatalf("diagnostic requests = %d, want %d", len(adapters.diagnostics), totalFaults)
	}
	if len(adapters.markers) != totalFaults*2 {
		t.Fatalf("markers = %d, want %d", len(adapters.markers), totalFaults*2)
	}
	if adapters.maxDepth != 1 {
		t.Fatalf("maximum simultaneous adapter operations = %d, want 1", adapters.maxDepth)
	}
	if adapters.missingDeadline {
		t.Fatal("an adapter call did not receive a bounded context")
	}

	for index, request := range adapters.diagnostics {
		if !request.IsDeleted || !request.IsRecovered {
			t.Fatalf("diagnostics %d = %+v, want deleted and recovered", index, request)
		}
		if request.Pod.UID == request.Replacement.UID {
			t.Fatalf("diagnostics %d replacement reused uid %q", index, request.Pod.UID)
		}
	}
}

func TestScenarioRefusesDeletionWithoutRetainedPath(t *testing.T) {
	t.Parallel()

	adapters := newFakeAdapters()
	adapters.baseline.HealthyPathsWithoutPod = 0
	scenario := newTestScenario(t, adapters)

	err := scenario.Run(t.Context(), DefaultExecution("mm32-no-capacity"))
	if !errors.Is(err, ErrUnsafeState) {
		t.Fatalf("Run() error = %v, want ErrUnsafeState", err)
	}
	if len(adapters.deleted) != 0 {
		t.Fatalf("deleted pods = %d, want 0", len(adapters.deleted))
	}
	if len(adapters.markers) != 0 {
		t.Fatalf("markers = %d, want 0", len(adapters.markers))
	}
	if len(adapters.diagnostics) != 1 {
		t.Fatalf("diagnostic requests = %d, want 1", len(adapters.diagnostics))
	}
}

func TestScenarioFailsWhenReplacementReusesUID(t *testing.T) {
	t.Parallel()

	adapters := newFakeAdapters()
	adapters.reuseUID = true
	scenario := newTestScenario(t, adapters)

	err := scenario.Run(t.Context(), DefaultExecution("mm32-reused-uid"))
	if !errors.Is(err, ErrUnsafeState) {
		t.Fatalf("Run() error = %v, want ErrUnsafeState", err)
	}
	if len(adapters.deleted) != 1 {
		t.Fatalf("deleted pods = %d, want 1", len(adapters.deleted))
	}
	if len(adapters.markers) != 2 || adapters.markers[1].Status != MarkerStatusFailed {
		t.Fatalf("terminal markers = %+v", adapters.markers)
	}
	if len(adapters.diagnostics) != 1 || adapters.diagnostics[0].IsRecovered {
		t.Fatalf("diagnostics = %+v", adapters.diagnostics)
	}
}

func TestScenarioRefusesUnstableReplicaCountBeforeDeletion(t *testing.T) {
	t.Parallel()

	adapters := newFakeAdapters()
	adapters.baseline.ReadyReplicas++
	scenario := newTestScenario(t, adapters)

	err := scenario.Run(t.Context(), DefaultExecution("mm32-extra-replica"))
	if !errors.Is(err, ErrUnsafeState) {
		t.Fatalf("Run() error = %v, want ErrUnsafeState", err)
	}
	if len(adapters.deleted) != 0 {
		t.Fatalf("deleted pods = %d, want 0", len(adapters.deleted))
	}
}

func TestScenarioFailsWhenDeploymentScalesDownDuringRecovery(t *testing.T) {
	t.Parallel()

	adapters := newFakeAdapters()
	adapters.recoveryDesiredReplicas = 1
	scenario := newTestScenario(t, adapters)

	err := scenario.Run(t.Context(), DefaultExecution("mm32-scale-down"))
	if !errors.Is(err, ErrUnsafeState) {
		t.Fatalf("Run() error = %v, want ErrUnsafeState", err)
	}
	if len(adapters.deleted) != 1 {
		t.Fatalf("deleted pods = %d, want 1", len(adapters.deleted))
	}
}

func TestScenarioCollectsDiagnosticsAfterCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	adapters := newFakeAdapters()
	adapters.deleteHook = func(context.Context, DeleteRequest) error {
		cancel()
		return nil
	}
	scenario := newTestScenario(t, adapters)

	err := scenario.Run(ctx, DefaultExecution("mm32-cancelled"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
	if len(adapters.diagnostics) != 1 {
		t.Fatalf("diagnostic requests = %d, want 1", len(adapters.diagnostics))
	}
	if adapters.diagnosticContextErr != nil {
		t.Fatalf("diagnostic context error = %v, want nil", adapters.diagnosticContextErr)
	}
	if len(adapters.markers) != 2 || adapters.terminalContextErr != nil {
		t.Fatalf("terminal marker state = %+v, context error = %v", adapters.markers, adapters.terminalContextErr)
	}
}

func TestScenarioJoinsOperationAndDiagnosticFailures(t *testing.T) {
	t.Parallel()

	errDelete := errors.New("delete failed")
	errDiagnostics := errors.New("diagnostics failed")
	adapters := newFakeAdapters()
	adapters.deleteHook = func(context.Context, DeleteRequest) error {
		return errDelete
	}
	adapters.collectHook = func(context.Context, DiagnosticRequest) error {
		return errDiagnostics
	}
	scenario := newTestScenario(t, adapters)

	err := scenario.Run(t.Context(), DefaultExecution("mm32-joined-errors"))
	if !errors.Is(err, errDelete) {
		t.Fatalf("Run() error = %v, want delete error", err)
	}
	if !errors.Is(err, errDiagnostics) {
		t.Fatalf("Run() error = %v, want diagnostics error", err)
	}
}

func TestScenarioRejectsIncompleteMatrixBeforeAdapters(t *testing.T) {
	t.Parallel()

	adapters := newFakeAdapters()
	scenario := newTestScenario(t, adapters)
	execution := DefaultExecution("mm32-incomplete")
	execution.Plans[0].Steps = execution.Plans[0].Steps[:len(execution.Plans[0].Steps)-1]

	err := scenario.Run(t.Context(), execution)
	if !errors.Is(err, ErrInvalidExecution) {
		t.Fatalf("Run() error = %v, want ErrInvalidExecution", err)
	}
	if len(adapters.calls) != 0 {
		t.Fatalf("adapter calls = %v, want none", adapters.calls)
	}
}

func TestScenarioRejectsRunIDOutsideWorkloadBounds(t *testing.T) {
	t.Parallel()

	adapters := newFakeAdapters()
	scenario := newTestScenario(t, adapters)
	execution := DefaultExecution("mm32-" + strings.Repeat("a", maxRunIDLength))

	err := scenario.Run(t.Context(), execution)
	if !errors.Is(err, ErrInvalidExecution) {
		t.Fatalf("Run() error = %v, want ErrInvalidExecution", err)
	}
	if len(adapters.calls) != 0 {
		t.Fatalf("adapter calls = %v, want none", adapters.calls)
	}
}

func TestScenarioRejectsDifferentButNonReverseOrdersBeforeAdapters(t *testing.T) {
	t.Parallel()

	adapters := newFakeAdapters()
	scenario := newTestScenario(t, adapters)
	execution := DefaultExecution("mm32-non-reverse")
	execution.Plans[1].Steps[0], execution.Plans[1].Steps[1] =
		execution.Plans[1].Steps[1], execution.Plans[1].Steps[0]

	err := scenario.Run(t.Context(), execution)
	if !errors.Is(err, ErrInvalidExecution) {
		t.Fatalf("Run() error = %v, want ErrInvalidExecution", err)
	}
	if len(adapters.calls) != 0 {
		t.Fatalf("adapter calls = %v, want none", adapters.calls)
	}
}

func TestNewRejectsUnboundedConfigurationAndMissingAdapters(t *testing.T) {
	t.Parallel()

	adapters := newFakeAdapters()
	testCases := []struct {
		name       string
		config     Config
		controller PodController
		probe      Probe
		collector  Collector
	}{
		{
			name:       "zero operation timeout",
			config:     Config{},
			controller: adapters,
			probe:      adapters,
			collector:  adapters,
		},
		{
			name:       "excessive recovery timeout",
			config:     withRecoveryTimeout(DefaultConfig(), maxRecoveryTimeout+time.Second),
			controller: adapters,
			probe:      adapters,
			collector:  adapters,
		},
		{
			name: "fractional deletion grace period",
			config: func() Config {
				config := DefaultConfig()
				config.DeletionGracePeriod += time.Millisecond
				return config
			}(),
			controller: adapters,
			probe:      adapters,
			collector:  adapters,
		},
		{
			name:      "missing controller",
			config:    DefaultConfig(),
			probe:     adapters,
			collector: adapters,
		},
		{
			name:       "missing probe",
			config:     DefaultConfig(),
			controller: adapters,
			collector:  adapters,
		},
		{
			name:       "missing collector",
			config:     DefaultConfig(),
			controller: adapters,
			probe:      adapters,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			_, err := New(
				testCase.config,
				testCase.controller,
				testCase.probe,
				testCase.collector,
			)
			if !errors.Is(err, ErrInvalidConfiguration) {
				t.Fatalf("New() error = %v, want ErrInvalidConfiguration", err)
			}
		})
	}
}

func TestValidatePodRefRejectsAmbiguousTargets(t *testing.T) {
	t.Parallel()

	valid := newFakeAdapters().baseline.Pod
	testCases := []struct {
		name   string
		mutate func(*PodRef)
	}{
		{
			name: "relative kubeconfig",
			mutate: func(pod *PodRef) {
				pod.KubeconfigPath = "kubeconfig"
			},
		},
		{
			name: "wildcard context",
			mutate: func(pod *PodRef) {
				pod.ContextName = "kind-*"
			},
		},
		{
			name: "shell metacharacter context",
			mutate: func(pod *PodRef) {
				pod.ContextName = "kind-mm32;other"
			},
		},
		{
			name: "wrong owner",
			mutate: func(pod *PodRef) {
				pod.OwnerRunID = "mm32-other"
			},
		},
		{
			name: "namespace flag",
			mutate: func(pod *PodRef) {
				pod.Namespace = "--all"
			},
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()

			pod := valid
			testCase.mutate(&pod)
			if err := validatePodRef("mm32-test", pod); !errors.Is(err, ErrUnsafeState) {
				t.Fatalf("validatePodRef() error = %v, want ErrUnsafeState", err)
			}
		})
	}
}

func withRecoveryTimeout(config Config, timeout time.Duration) Config {
	config.RecoveryTimeout = timeout
	return config
}

func newTestScenario(t *testing.T, adapters *fakeAdapters) *Scenario {
	t.Helper()

	config := DefaultConfig()
	config.OperationTimeout = time.Second
	config.RecoveryTimeout = time.Second
	config.DiagnosticsTimeout = time.Second
	scenario, err := New(config, adapters, adapters, adapters)
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	return scenario
}

type fakeAdapters struct {
	baseline State

	calls                   []string
	deleted                 []DeleteRequest
	markers                 []FaultMarker
	diagnostics             []DiagnosticRequest
	depth                   int
	maxDepth                int
	missingDeadline         bool
	reuseUID                bool
	recoveryDesiredReplicas int32
	deleteHook              func(context.Context, DeleteRequest) error
	collectHook             func(context.Context, DiagnosticRequest) error
	diagnosticContextErr    error
	terminalContextErr      error
}

func newFakeAdapters() *fakeAdapters {
	return &fakeAdapters{
		baseline: State{
			Pod: PodRef{
				KubeconfigPath: "/tmp/mm32/kubeconfig",
				ContextName:    "kind-mm32-dc-a-dmz",
				Namespace:      "mm32-test",
				Deployment:     "gateway-in",
				Name:           "gateway-in-old",
				UID:            "uid-old",
				OwnerRunID:     "mm32-test",
			},
			DesiredReplicas:        2,
			ReadyReplicas:          2,
			AvailableReplicas:      2,
			HealthyPaths:           2,
			HealthyPathsWithoutPod: 1,
			IsPodReady:             true,
			IsTunnelReady:          true,
		},
		calls:       []string{},
		deleted:     []DeleteRequest{},
		markers:     []FaultMarker{},
		diagnostics: []DiagnosticRequest{},
	}
}

func (adapters *fakeAdapters) Preflight(
	ctx context.Context,
	runID string,
	step Step,
) (State, error) {
	adapters.begin(ctx, "preflight")
	defer adapters.end()

	state := adapters.baseline
	state.Selected = step
	state.Pod.ContextName = "kind-mm32-" + string(step.DC)
	state.Pod.Namespace = runID
	state.Pod.Deployment = string(step.Component)
	state.Pod.Name = fmt.Sprintf("%s-%s-old", step.Component, step.Role)
	state.Pod.UID = fmt.Sprintf("uid-%s-%s-%s-old", step.DC, step.Component, step.Role)
	state.Pod.OwnerRunID = runID
	peer := state.Pod
	peer.Name += "-peer"
	peer.UID += "-peer"
	state.Pods = []PodRef{state.Pod, peer}
	return state, nil
}

func (adapters *fakeAdapters) Delete(ctx context.Context, request DeleteRequest) error {
	adapters.begin(ctx, "delete")
	defer adapters.end()

	adapters.deleted = append(adapters.deleted, request)
	if adapters.deleteHook != nil {
		return adapters.deleteHook(ctx, request)
	}
	return nil
}

func (adapters *fakeAdapters) WaitRecovered(
	ctx context.Context,
	request RecoveryRequest,
) (State, error) {
	adapters.begin(ctx, "wait-recovered")
	defer adapters.end()

	if err := ctx.Err(); err != nil {
		return State{}, err
	}
	recovery := request.Baseline
	recovery.Pod.Name = fmt.Sprintf("%s-%s-new", request.Step.Component, request.Step.Role)
	recovery.Pod.UID = fmt.Sprintf(
		"uid-%s-%s-%s-new",
		request.Step.DC,
		request.Step.Component,
		request.Step.Role,
	)
	if adapters.reuseUID {
		recovery.Pod.UID = request.OldPod.UID
	}
	if adapters.recoveryDesiredReplicas != 0 {
		recovery.DesiredReplicas = adapters.recoveryDesiredReplicas
	}
	for index, pod := range recovery.Pods {
		if pod == request.OldPod {
			recovery.Pods[index] = recovery.Pod
		}
	}
	return recovery, nil
}

func (adapters *fakeAdapters) WaitSteady(
	ctx context.Context,
) error {
	adapters.begin(ctx, "wait-steady")
	defer adapters.end()

	return nil
}

func (adapters *fakeAdapters) Mark(ctx context.Context, marker FaultMarker) error {
	adapters.begin(ctx, "mark")
	defer adapters.end()

	if marker.Phase == MarkerPhaseEnded {
		adapters.terminalContextErr = ctx.Err()
	}
	adapters.markers = append(adapters.markers, marker)
	return nil
}

func (adapters *fakeAdapters) Collect(ctx context.Context, request DiagnosticRequest) error {
	adapters.begin(ctx, "collect")
	defer adapters.end()

	adapters.diagnosticContextErr = ctx.Err()
	adapters.diagnostics = append(adapters.diagnostics, request)
	if adapters.collectHook != nil {
		return adapters.collectHook(ctx, request)
	}
	return nil
}

func (adapters *fakeAdapters) begin(ctx context.Context, operation string) {
	adapters.calls = append(adapters.calls, operation)
	adapters.depth++
	adapters.maxDepth = max(adapters.maxDepth, adapters.depth)
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		adapters.missingDeadline = true
	}
}

func (adapters *fakeAdapters) end() {
	adapters.depth--
}

func TestScenarioOperationOrder(t *testing.T) {
	t.Parallel()

	adapters := newFakeAdapters()
	scenario := newTestScenario(t, adapters)
	if err := scenario.Run(t.Context(), DefaultExecution("mm32-order")); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	expectedFirstFault := []string{
		"wait-steady",
		"preflight",
		"mark",
		"delete",
		"wait-recovered",
		"wait-steady",
		"mark",
		"collect",
	}
	if !slices.Equal(adapters.calls[:len(expectedFirstFault)], expectedFirstFault) {
		t.Fatalf("first fault calls = %v, want %v", adapters.calls[:len(expectedFirstFault)], expectedFirstFault)
	}
}
