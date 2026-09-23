package probe

import (
	"context"
	"time"
)

// TrafficClass разделяет read и mutating потоки без автоматических повторов.
type TrafficClass string

const (
	TrafficClassRead     TrafficClass = "read"
	TrafficClassMutating TrafficClass = "mutating"
)

// Outcome — конечный низкокардинальный результат одной попытки.
type Outcome string

const (
	OutcomeUnknown         Outcome = "unknown"
	OutcomeSuccess         Outcome = "success"
	OutcomeTimeout         Outcome = "timeout"
	OutcomeCanceled        Outcome = "canceled"
	OutcomeBackpressure    Outcome = "backpressure"
	OutcomeUnavailable     Outcome = "unavailable"
	OutcomeRejected        Outcome = "rejected"
	OutcomeInternalError   Outcome = "internal_error"
	OutcomeInvalidMetadata Outcome = "invalid_metadata"
)

// Request содержит только идентификаторы и порядок попытки. Payload остаётся
// ответственностью transport adapter и никогда не попадает в journal.
type Request struct {
	ID             string
	IdempotencyKey string
	Class          TrafficClass
	Sequence       uint64
}

// Response — безопасное резюме transport adapter без raw error или payload.
// Неизвестный Outcome и некорректные dimension fields преобразуются runner в
// OutcomeInvalidMetadata или OutcomeInternalError.
type Response struct {
	Outcome          Outcome
	RouteID          string
	DataCenter       DataCenter
	Source           string
	InternalSequence uint64
	Duplicate        bool
}

// Invoker выполняет одну попытку. Реализация обязана уважать cancellation и
// deadline ctx, не повторять mutating запрос и не сохранять raw error в Response.
type Invoker interface {
	Invoke(ctx context.Context, request Request) Response
}

// EventKind задаёт конечный набор событий monotonic timeline.
type EventKind string

const (
	EventKindRunStarted       EventKind = "run_started"
	EventKindRequestScheduled EventKind = "request_scheduled"
	EventKindRequestStarted   EventKind = "request_started"
	EventKindRequestFinished  EventKind = "request_finished"
	EventKindMarker           EventKind = "marker"
	EventKindRunStopping      EventKind = "run_stopping"
	EventKindRunFinished      EventKind = "run_finished"
)

// MarkerPhase описывает положение внешнего события в lifecycle сценария.
type MarkerPhase string

const (
	MarkerPhaseBefore     MarkerPhase = "before"
	MarkerPhaseStarted    MarkerPhase = "started"
	MarkerPhaseSteady     MarkerPhase = "steady"
	MarkerPhaseRecovering MarkerPhase = "recovering"
	MarkerPhaseRecovered  MarkerPhase = "recovered"
	MarkerPhaseAfter      MarkerPhase = "after"
)

// MarkerResult описывает результат внешнего lifecycle события.
type MarkerResult string

const (
	MarkerResultUnknown MarkerResult = "unknown"
	MarkerResultSuccess MarkerResult = "success"
	MarkerResultFailure MarkerResult = "failure"
)

// DataCenter — логический DC, не имя disposable cluster или kube context.
type DataCenter string

const (
	DataCenterUnknown DataCenter = ""
	DataCenterA       DataCenter = "dc-a"
	DataCenterB       DataCenter = "dc-b"
)

// Zone — конечная trust zone marker.
type Zone string

const (
	ZoneUnknown  Zone = ""
	ZoneDMZ      Zone = "dmz"
	ZoneInternal Zone = "internal"
	ZoneExternal Zone = "external"
)

// Component — конечная failure/lifecycle boundary marker.
type Component string

const (
	ComponentUnknown           Component = ""
	ComponentGatewayIn         Component = "gateway-in"
	ComponentGatewayOut        Component = "gateway-out"
	ComponentInternalService   Component = "internal-service"
	ComponentKubernetesService Component = "kubernetes-service"
	ComponentFrontDoor         Component = "front-door"
	ComponentNetwork           Component = "network"
	ComponentDataCenter        Component = "data-center"
)

// Role различает bounded роль компонента без pod UID или instance ID.
type Role string

const (
	RoleUnknown Role = ""
	RoleActive  Role = "active"
	RoleStandby Role = "standby"
	RoleReplica Role = "replica"
)

// Marker фиксирует внешнее событие, не выполняя fault или rollout. Все поля
// проходят allowlist-валидацию и имеют ограниченную длину.
type Marker struct {
	FaultID    string
	DataCenter DataCenter
	Zone       Zone
	Component  Component
	Role       Role
	Phase      MarkerPhase
	Result     MarkerResult
	Revision   string
}

// Event — одна запись timeline. Offset вычисляется от StartedAt через
// time.Sub, поэтому сохраняет monotonic semantics внутри процесса.
type Event struct {
	Sequence   uint64
	Offset     time.Duration
	Kind       EventKind
	Class      TrafficClass
	RequestID  string
	Outcome    Outcome
	RouteID    string
	DataCenter DataCenter
	Marker     Marker
}

// ClientRecord хранит полный lifecycle одной запланированной попытки.
type ClientRecord struct {
	RequestID          string
	IdempotencyKey     string
	Class              TrafficClass
	Sequence           uint64
	ScheduledOffset    time.Duration
	StartedOffset      time.Duration
	DeadlineOffset     time.Duration
	FinishedOffset     time.Duration
	Latency            time.Duration
	Outcome            Outcome
	RouteID            string
	DataCenter         DataCenter
	Source             string
	InternalSequence   uint64
	CompletionSequence uint64
	Duplicate          bool
	Dispatched         bool
}

// Snapshot — неизменяемая копия client ledger и timeline одного запуска.
type Snapshot struct {
	StartedAt         time.Time
	FinishedOffset    time.Duration
	Records           []ClientRecord
	Events            []Event
	IsComplete        bool
	IncompleteReasons []string
}

// SteadyRequirement задаёт минимальный success streak для включённых потоков.
// Нулевое значение класса означает, что класс не участвует в ожидании.
type SteadyRequirement struct {
	ReadSuccesses     uint32
	MutatingSuccesses uint32
}

// SteadyState — наблюдённый success streak без запуска дополнительных вызовов.
type SteadyState struct {
	ObservedOffset    time.Duration
	ReadSuccesses     uint32
	MutatingSuccesses uint32
}

// InternalRecord — безопасная запись fake internal service ledger. Порядок
// элементов InternalSnapshot.Records считается порядком наблюдения ledger.
type InternalRecord struct {
	RequestID            string
	IdempotencyKeySHA256 string
	Class                TrafficClass
	Sequence             uint64
	Attempts             uint32
	AcceptedOffset       time.Duration
	CompletedOffset      time.Duration
	Outcome              Outcome
	RouteID              string
	DataCenter           DataCenter
	Source               string
}

// InternalSnapshot сообщает не только записи, но и полноту чтения ledger.
// Адаптер обязан выставить IsComplete=false при pagination/read/cleanup error.
type InternalSnapshot struct {
	Records           []InternalRecord
	IsComplete        bool
	IncompleteReasons []string
}
