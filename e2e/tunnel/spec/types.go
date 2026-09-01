// Package spec defines the executable availability contract shared by tunnel
// E2E probes and fault scenarios. The package evaluates a finite request ledger;
// it does not create infrastructure, generate load, or inject failures.
package spec

import "time"

const (
	// ScenarioSchemaVersion identifies the supported scenario configuration format.
	ScenarioSchemaVersion = "marketmesh.tunnel.slo.scenario/v1"
	// RunSchemaVersion identifies the supported probe ledger format.
	RunSchemaVersion = "marketmesh.tunnel.slo.run/v1"
	// ReportSchemaVersion identifies the supported JSON report format.
	ReportSchemaVersion = "marketmesh.tunnel.slo.report/v1"

	partsPerMillion = uint64(1_000_000)
)

// Duration is a time.Duration encoded as a human-readable JSON string.
type Duration time.Duration

// String returns the canonical Go duration representation.
func (d Duration) String() string {
	return time.Duration(d).String()
}

// Value returns the standard-library duration value.
func (d Duration) Value() time.Duration {
	return time.Duration(d)
}

// ScenarioKind separates zero-downtime maintenance from emergency recovery.
type ScenarioKind string

const (
	ScenarioKindUnknown         ScenarioKind = ""
	ScenarioKindPlannedRolling  ScenarioKind = "planned_rolling_update"
	ScenarioKindEmergencyOutage ScenarioKind = "emergency_outage"
)

// RequestClass separates retry-safe reads from state-changing requests.
type RequestClass string

const (
	RequestClassUnknown        RequestClass = ""
	RequestClassReadIdempotent RequestClass = "read_idempotent"
	RequestClassMutating       RequestClass = "mutating"
)

// FaultTarget is a finite, low-cardinality failure boundary.
type FaultTarget string

const (
	FaultTargetUnknown           FaultTarget = ""
	FaultTargetGatewayIn         FaultTarget = "gateway_in"
	FaultTargetGatewayOut        FaultTarget = "gateway_out"
	FaultTargetInternalService   FaultTarget = "internal_service"
	FaultTargetKubernetesService FaultTarget = "kubernetes_service"
	FaultTargetNetwork           FaultTarget = "network"
	FaultTargetDC                FaultTarget = "dc"
)

// FaultMode describes the bounded action performed by a separate fault runner.
type FaultMode string

const (
	FaultModeUnknown                FaultMode = ""
	FaultModeRollingUpdate          FaultMode = "rolling_update"
	FaultModePodOutage              FaultMode = "pod_outage"
	FaultModeServiceEndpointsOutage FaultMode = "service_endpoints_outage"
	FaultModeNetworkPartition       FaultMode = "network_partition"
	FaultModeDCOutage               FaultMode = "dc_outage"
)

// RecoveryAnchor selects the instant from which recovery is measured.
type RecoveryAnchor string

const (
	RecoveryAnchorUnknown      RecoveryAnchor = ""
	RecoveryAnchorFaultStarted RecoveryAnchor = "fault_started"
	RecoveryAnchorFaultEnded   RecoveryAnchor = "fault_ended"
)

// AttemptOutcome is the terminal result of one visible transport attempt.
type AttemptOutcome string

const (
	AttemptOutcomeUnknown AttemptOutcome = "unknown"
	AttemptOutcomeSuccess AttemptOutcome = "success"
	AttemptOutcomeFailure AttemptOutcome = "failure"
)

// ReportStatus is the final fail-closed evaluation status.
type ReportStatus string

const (
	ReportStatusPass ReportStatus = "pass"
	ReportStatusFail ReportStatus = "fail"
)

// Scenario is a versioned, machine-readable SLO configuration.
type Scenario struct {
	SchemaVersion string             `json:"schema_version"`
	ID            string             `json:"id"`
	Kind          ScenarioKind       `json:"kind"`
	WarmUp        Duration           `json:"warm_up"`
	Targets       []ClassTarget      `json:"targets"`
	Faults        []FaultExpectation `json:"faults"`
}

// ClassTarget defines the error budget and downtime limit for one request class.
type ClassTarget struct {
	Class           RequestClass `json:"class"`
	MinEligible     uint64       `json:"min_eligible"`
	MaxErrorRatePPM uint32       `json:"max_error_rate_ppm"`
	MaxDowntime     Duration     `json:"max_downtime"`
}

// FaultExpectation binds one externally executed fault to a recovery contract.
type FaultExpectation struct {
	ID       string          `json:"id"`
	Target   FaultTarget     `json:"target"`
	Mode     FaultMode       `json:"mode"`
	Recovery *RecoveryTarget `json:"recovery,omitempty"`
}

// RecoveryTarget requires a stable success streak for every selected class.
type RecoveryTarget struct {
	Anchor        RecoveryAnchor `json:"anchor"`
	MaxDuration   Duration       `json:"max_duration"`
	SuccessStreak uint32         `json:"success_streak"`
	Classes       []RequestClass `json:"classes"`
}

// Run is the complete finite ledger produced by a probe runner.
type Run struct {
	SchemaVersion string               `json:"schema_version"`
	ScenarioID    string               `json:"scenario_id"`
	RunID         string               `json:"run_id"`
	StartedAt     time.Time            `json:"started_at"`
	EndedAt       time.Time            `json:"ended_at"`
	Planned       []PlannedRequests    `json:"planned"`
	Capacity      []CapacityInterval   `json:"capacity"`
	Exclusions    []ExclusionInterval  `json:"exclusions"`
	Faults        []FaultEvent         `json:"faults"`
	Requests      []RequestObservation `json:"requests"`
}

// PlannedRequests makes omitted probe results detectable by class.
type PlannedRequests struct {
	Class RequestClass `json:"class"`
	Count uint64       `json:"count"`
}

// CapacityInterval states how many DCs were physically capable of serving.
// Intervals must form a gapless partition of the measured window.
type CapacityInterval struct {
	StartedAt             time.Time `json:"started_at"`
	EndedAt               time.Time `json:"ended_at"`
	PhysicallyAvailableDC uint32    `json:"physically_available_dc"`
}

// ExclusionInterval explicitly removes only a proven all-DC outage interval.
type ExclusionInterval struct {
	StartedAt time.Time       `json:"started_at"`
	EndedAt   time.Time       `json:"ended_at"`
	Reason    ExclusionReason `json:"reason"`
}

// ExclusionReason is deliberately finite so arbitrary maintenance cannot hide errors.
type ExclusionReason string

const (
	ExclusionReasonUnknown                    ExclusionReason = ""
	ExclusionReasonAllDCPhysicallyUnavailable ExclusionReason = "all_dc_physically_unavailable"
)

// FaultEvent records timing only; fault execution belongs to another package.
type FaultEvent struct {
	ID        string     `json:"id"`
	StartedAt time.Time  `json:"started_at"`
	EndedAt   *time.Time `json:"ended_at,omitempty"`
}

// RequestObservation records one scheduled logical request and every visible attempt.
type RequestObservation struct {
	ID          string               `json:"id"`
	Class       RequestClass         `json:"class"`
	ScheduledAt time.Time            `json:"scheduled_at"`
	Missing     bool                 `json:"missing"`
	Attempts    []AttemptObservation `json:"attempts"`
	Mutation    *MutationObservation `json:"mutation,omitempty"`
}

// AttemptObservation never contains a payload, credential, peer address, or error text.
type AttemptObservation struct {
	Number     uint32         `json:"number"`
	StartedAt  time.Time      `json:"started_at"`
	FinishedAt time.Time      `json:"finished_at"`
	Outcome    AttemptOutcome `json:"outcome"`
}

// MutationObservation proves idempotency-key uniqueness and applied cardinality.
type MutationObservation struct {
	IdempotencyKey string `json:"idempotency_key"`
	LedgerKnown    bool   `json:"ledger_known"`
	AppliedCount   uint32 `json:"applied_count"`
}

// Report is the versioned JSON result. It deliberately omits per-request IDs and keys.
type Report struct {
	SchemaVersion string            `json:"schema_version"`
	ScenarioID    string            `json:"scenario_id"`
	RunID         string            `json:"run_id"`
	StartedAt     time.Time         `json:"started_at"`
	EndedAt       time.Time         `json:"ended_at"`
	MeasuredAt    time.Time         `json:"measured_at"`
	Status        ReportStatus      `json:"status"`
	Classes       []ClassSummary    `json:"classes"`
	Downtime      []DowntimeWindow  `json:"downtime"`
	Recovery      []RecoverySummary `json:"recovery"`
	Checks        []CheckResult     `json:"checks"`
}

// ClassSummary contains exact integer availability and error-budget accounting.
type ClassSummary struct {
	Class           RequestClass       `json:"class"`
	Planned         uint64             `json:"planned"`
	Recorded        uint64             `json:"recorded"`
	Eligible        uint64             `json:"eligible"`
	Successful      uint64             `json:"successful"`
	Failed          uint64             `json:"failed"`
	Missing         uint64             `json:"missing"`
	Unknown         uint64             `json:"unknown"`
	Retried         uint64             `json:"retried"`
	AvailabilityPPM uint64             `json:"availability_ppm"`
	ErrorBudget     ErrorBudgetSummary `json:"error_budget"`
}

// ErrorBudgetSummary records the configured and consumed failure allowance.
type ErrorBudgetSummary struct {
	RatePPM   uint32 `json:"rate_ppm"`
	Allowed   uint64 `json:"allowed"`
	Consumed  uint64 `json:"consumed"`
	Remaining uint64 `json:"remaining"`
}

// DowntimeWindow is a maximal consecutive sequence of unsuccessful requests.
type DowntimeWindow struct {
	Class     RequestClass `json:"class"`
	StartedAt time.Time    `json:"started_at"`
	EndedAt   time.Time    `json:"ended_at"`
	Duration  Duration     `json:"duration"`
}

// RecoverySummary reports stable recovery separately for every fault and class.
type RecoverySummary struct {
	FaultID     string       `json:"fault_id"`
	Target      FaultTarget  `json:"target"`
	Class       RequestClass `json:"class"`
	AnchorAt    time.Time    `json:"anchor_at"`
	RecoveredAt *time.Time   `json:"recovered_at,omitempty"`
	Duration    Duration     `json:"duration"`
	MaxDuration Duration     `json:"max_duration"`
	Passed      bool         `json:"passed"`
}

// CheckResult maps directly to one JUnit testcase.
type CheckResult struct {
	Name       string      `json:"name"`
	Passed     bool        `json:"passed"`
	Violations []Violation `json:"violations"`
}

// Violation contains a stable code and a bounded diagnostic without request data.
type Violation struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}
