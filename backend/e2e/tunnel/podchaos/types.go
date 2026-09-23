package podchaos

import (
	"context"
	"slices"
	"time"
)

// DC identifies one finite E2E failure domain.
type DC string

const (
	DCUnknown DC = ""
	DCA       DC = "dc-a"
	DCB       DC = "dc-b"
)

// Component identifies the tunnel workload whose pod is removed.
type Component string

const (
	ComponentUnknown    Component = ""
	ComponentGatewayIn  Component = "gateway-in"
	ComponentGatewayOut Component = "gateway-out"
)

// Role selects a pod from the current smooth-WRR snapshot. Active means a
// tunnel with active requests; Standby means an eligible tunnel without active
// requests at that instant. Adapters must resolve the role again immediately
// before every fault and fail on an empty or ambiguous mapping.
type Role string

const (
	RoleUnknown Role = ""
	RoleActive  Role = "active"
	RoleStandby Role = "standby"
)

// Step describes one logical pod outage in the required failure matrix.
type Step struct {
	DC        DC
	Component Component
	Role      Role
}

// Plan is one complete ordering of all DC/component/role combinations.
type Plan struct {
	ID    string
	Steps []Step
}

// Execution describes the complete two-order run for one disposable topology.
type Execution struct {
	RunID string
	Plans []Plan
}

// Fault is one validated, ordered failure expectation shared with the final
// SLO adapter. ID is the same value that Scenario writes into probe markers.
type Fault struct {
	ID   string
	Step Step
}

// Faults returns a defensive, ordered copy of all fault expectations. Invalid
// or non-opposite plans fail before any caller can start a destructive action.
func (execution Execution) Faults() ([]Fault, error) {
	execution = cloneExecution(execution)
	if err := validateExecution(execution); err != nil {
		return nil, err
	}

	faults := make([]Fault, 0, requiredPlans*requiredStepsPerPlan)
	for _, plan := range execution.Plans {
		for stepIndex, step := range plan.Steps {
			faults = append(faults, Fault{
				ID:   faultID(plan.ID, stepIndex, step),
				Step: step,
			})
		}
	}
	return faults, nil
}

// Config bounds every external operation performed by Scenario.
type Config struct {
	OperationTimeout    time.Duration
	RecoveryTimeout     time.Duration
	DiagnosticsTimeout  time.Duration
	DeletionGracePeriod time.Duration
}

// DefaultConfig returns conservative local E2E bounds. SLO recovery limits are
// still evaluated by the probe/spec adapters and cannot be weakened here.
func DefaultConfig() Config {
	return Config{
		OperationTimeout:    30 * time.Second,
		RecoveryTimeout:     5 * time.Minute,
		DiagnosticsTimeout:  2 * time.Minute,
		DeletionGracePeriod: 30 * time.Second,
	}
}

// DefaultExecution returns two opposite failure orders. Each role is resolved
// dynamically by the controller when its step starts.
func DefaultExecution(runID string) Execution {
	return Execution{
		RunID: runID,
		Plans: []Plan{
			{
				ID: "a-active",
				Steps: orderedSteps(
					[]DC{DCA, DCB},
					[]Role{RoleActive, RoleStandby},
					[]Component{ComponentGatewayIn, ComponentGatewayOut},
				),
			},
			{
				ID: "b-standby",
				Steps: orderedSteps(
					[]DC{DCB, DCA},
					[]Role{RoleStandby, RoleActive},
					[]Component{ComponentGatewayOut, ComponentGatewayIn},
				),
			},
		},
	}
}

func orderedSteps(dcs []DC, roles []Role, components []Component) []Step {
	steps := make([]Step, 0, len(dcs)*len(roles)*len(components))
	for _, dc := range dcs {
		for _, role := range roles {
			for _, component := range components {
				steps = append(steps, Step{
					DC:        dc,
					Component: component,
					Role:      role,
				})
			}
		}
	}
	return steps
}

// PodRef is the exact, ownership-validated Kubernetes target returned by the
// controller. Delete must use every field, including UID, as a precondition.
type PodRef struct {
	KubeconfigPath string
	ContextName    string
	Namespace      string
	Deployment     string
	Name           string
	UID            string
	OwnerRunID     string
}

// State is a bounded capacity snapshot for one dynamically selected pod.
type State struct {
	Selected               Step
	Pod                    PodRef
	Pods                   []PodRef
	DesiredReplicas        int32
	ReadyReplicas          int32
	AvailableReplicas      int32
	HealthyPaths           int
	HealthyPathsWithoutPod int
	IsPodReady             bool
	IsTunnelReady          bool
	IsRolling              bool
}

// DeleteRequest authorizes removal of one exact pod and nothing else.
type DeleteRequest struct {
	RunID       string
	FaultID     string
	Step        Step
	Pod         PodRef
	GracePeriod time.Duration
}

// RecoveryRequest describes the baseline that a replacement pod must restore.
type RecoveryRequest struct {
	RunID    string
	FaultID  string
	Step     Step
	OldPod   PodRef
	Baseline State
}

// MarkerPhase separates the two monotonic fault boundaries.
type MarkerPhase string

const (
	MarkerPhaseUnknown MarkerPhase = ""
	MarkerPhaseStarted MarkerPhase = "started"
	MarkerPhaseEnded   MarkerPhase = "ended"
)

// MarkerStatus is set only on the terminal marker.
type MarkerStatus string

const (
	MarkerStatusUnknown MarkerStatus = ""
	MarkerStatusPassed  MarkerStatus = "passed"
	MarkerStatusFailed  MarkerStatus = "failed"
)

// FaultMarker is safe for the bounded event timeline: it contains no pod UID,
// request ID, payload, secret or peer address. RunID and FaultID identify the
// event but must not be exported as metric labels; metrics use only Step and
// Status finite values.
type FaultMarker struct {
	RunID   string
	FaultID string
	Step    Step
	Phase   MarkerPhase
	Status  MarkerStatus
}

// DiagnosticRequest contains exact Kubernetes references for artifact
// collection but must never be used as metric labels.
type DiagnosticRequest struct {
	RunID       string
	FaultID     string
	Step        Step
	Pod         PodRef
	Replacement PodRef
	IsDeleted   bool
	IsRecovered bool
}

// PodController owns exact Kubernetes reads, one-pod deletion and recovery
// waits. Implementations must use the supplied kubeconfig/context rather than
// process-global or user defaults. Delete must immediately recheck ownership,
// selected role and retained capacity, then apply the supplied UID as a server-
// side precondition so a stale preflight cannot delete a replacement pod.
type PodController interface {
	Preflight(context.Context, string, Step) (State, error)
	Delete(context.Context, DeleteRequest) error
	WaitRecovered(context.Context, RecoveryRequest) (State, error)
}

// Probe exposes only live observations and monotonic markers. The caller must
// stop its continuous runner after Scenario.Run and evaluate the one immutable
// final snapshot; partial live state must never be treated as a passing ledger.
type Probe interface {
	WaitSteady(context.Context) error
	Mark(context.Context, FaultMarker) error
}

// Collector persists bounded, sanitized diagnostics before caller cleanup.
type Collector interface {
	Collect(context.Context, DiagnosticRequest) error
}

func cloneExecution(execution Execution) Execution {
	plans := make([]Plan, 0, len(execution.Plans))
	for _, plan := range execution.Plans {
		plans = append(plans, Plan{
			ID:    plan.ID,
			Steps: slices.Clone(plan.Steps),
		})
	}
	execution.Plans = plans
	return execution
}
