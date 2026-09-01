package networkchaos

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/netip"
	"regexp"
	"time"
)

const (
	// TaskLabel связывает disposable resource с конкретной Taskboard-задачей.
	TaskLabel = "com.marketmesh.e2e.task"
	// RunLabel отделяет параллельные E2E-запуски друг от друга.
	RunLabel = "com.marketmesh.e2e.run"
	// DisposableLabel явно разрешает удаление ресурса после диагностики.
	DisposableLabel = "com.marketmesh.e2e.disposable"
	// TaskKey — единственный Taskboard scope, которым владеет пакет.
	TaskKey = "MM-36"

	maxPlanSteps       = 256
	maxFaultsPerStep   = 16
	maxPeerNetworks    = 16
	maxBoundedDuration = 24 * time.Hour
	maxNetworkDelay    = 60 * time.Second
	minBandwidthKbit   = 8
	maxBandwidthKbit   = 10_000_000
)

var (
	runIDPattern        = regexp.MustCompile(`^mm36-[a-z0-9][a-z0-9-]{7,58}[a-z0-9]$`)
	scenarioNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)
	resourceIDPattern   = regexp.MustCompile(`^[a-f0-9]{64}$`)
	resourceNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{1,126}[a-z0-9]$`)
	interfacePattern    = regexp.MustCompile(`^eth[0-9]{1,2}$`)
	privatePrefixes     = []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("172.16.0.0/12"),
		netip.MustParsePrefix("192.168.0.0/16"),
		netip.MustParsePrefix("fc00::/7"),
	}
)

// Kind определяет одну атомарную сетевую деградацию.
type Kind string

const (
	KindUnknown     Kind = ""
	KindPartition   Kind = "partition"
	KindDegradation Kind = "degradation"
)

// Phase отмечает воспроизводимую точку diagnostics без payload или PII.
type Phase string

const (
	PhaseFaulted   Phase = "faulted"
	PhaseFailed    Phase = "failed"
	PhaseRecovered Phase = "recovered"
)

// ResourceRef задаёт точную ссылку на Docker resource. И имя, и immutable ID
// обязательны, чтобы adapter не мог молча выбрать ресурс по текущему context.
type ResourceRef struct {
	ID   string
	Name string
}

// Resource — фактическое состояние Docker resource непосредственно перед
// mutation. Labels используются только для проверки test scope.
type Resource struct {
	ID     string
	Name   string
	Labels map[string]string
}

// Network добавляет к Docker resource приватные test subnets.
type Network struct {
	Resource Resource
	Prefixes []netip.Prefix
}

// Snapshot связывает точный container interface с primary network и, для
// partition, с исчерпывающим набором peer test networks.
type Snapshot struct {
	Container    Resource
	Network      Network
	PeerNetworks []Network
	Interface    string
}

// Fault описывает mutation без shell fragments и произвольных CIDR. Adapter
// получает peer prefixes только из проверенных Docker network snapshots.
type Fault struct {
	Name          string
	Kind          Kind
	Container     ResourceRef
	Network       ResourceRef
	PeerNetworks  []ResourceRef
	Interface     string
	Delay         time.Duration
	Jitter        time.Duration
	LossPercent   float64
	BandwidthKbit uint32
	CapacityLoss  uint
}

// Step объединяет faults, которые действуют одновременно. Последовательность
// Steps вместе с Seed полностью определяет воспроизводимый сценарий.
type Step struct {
	Name   string
	Hold   time.Duration
	Faults []Fault
}

// Plan задаёт seed и точную последовательность faults.
type Plan struct {
	Seed  int64
	Steps []Step
}

// Config ограничивает lifecycle и задаёт минимальную сохранённую capacity.
type Config struct {
	RunID              string
	OperationTimeout   time.Duration
	DiagnosticsTimeout time.Duration
	RestoreTimeout     time.Duration
	RecoveryTimeout    time.Duration
	PollInterval       time.Duration
	MaxStepDuration    time.Duration
	MinimumCapacity    uint
}

// RestoreFunc отменяет одну mutation. Реализация обязана быть идемпотентной и
// учитывать deadline ctx.
type RestoreFunc func(ctx context.Context) error

// Driver разрешает точные resources и применяет fault. Apply может вернуть
// RestoreFunc вместе с ошибкой, если mutation была выполнена частично.
type Driver interface {
	Inspect(ctx context.Context, fault Fault) (Snapshot, error)
	Apply(ctx context.Context, snapshot Snapshot, fault Fault) (RestoreFunc, error)
}

// CapacitySource возвращает число готовых независимых единиц capacity. Значение
// не должно включать stale или draining endpoints.
type CapacitySource interface {
	Ready(ctx context.Context) (uint, error)
}

// DiagnosticPoint идентифицирует snapshot без request data и динамических
// labels.
type DiagnosticPoint struct {
	Seed      int64
	StepIndex int
	StepName  string
	Phase     Phase
}

// Diagnostics собирает logs, events и resource snapshots до cleanup.
type Diagnostics interface {
	Capture(ctx context.Context, point DiagnosticPoint) error
}

// waiter предоставляет cancellable bounded ожидание и позволяет тестам не
// зависеть от wall clock.
type waiter interface {
	Wait(ctx context.Context, duration time.Duration) error
}

type timerWaiter struct{}

func (timerWaiter) Wait(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func validateConfig(config Config) error {
	if !runIDPattern.MatchString(config.RunID) {
		return errors.New("networkchaos: run id must use mm36-<unique-lowercase-id>")
	}

	durations := []struct {
		name  string
		value time.Duration
	}{
		{name: "operation timeout", value: config.OperationTimeout},
		{name: "diagnostics timeout", value: config.DiagnosticsTimeout},
		{name: "restore timeout", value: config.RestoreTimeout},
		{name: "recovery timeout", value: config.RecoveryTimeout},
		{name: "poll interval", value: config.PollInterval},
		{name: "maximum step duration", value: config.MaxStepDuration},
	}
	for _, duration := range durations {
		if duration.value <= 0 {
			return fmt.Errorf("networkchaos: %s must be positive", duration.name)
		}
		if duration.value > maxBoundedDuration {
			return fmt.Errorf(
				"networkchaos: %s must not exceed %s",
				duration.name,
				maxBoundedDuration,
			)
		}
	}
	if config.PollInterval > config.RecoveryTimeout {
		return errors.New("networkchaos: poll interval must not exceed recovery timeout")
	}
	if config.MinimumCapacity == 0 {
		return errors.New("networkchaos: minimum capacity must be positive")
	}

	return nil
}

func validatePlan(config Config, plan Plan) error {
	if len(plan.Steps) == 0 || len(plan.Steps) > maxPlanSteps {
		return fmt.Errorf(
			"networkchaos: plan must contain between 1 and %d steps",
			maxPlanSteps,
		)
	}

	stepNames := make(map[string]struct{}, len(plan.Steps))
	for stepIndex, step := range plan.Steps {
		if !scenarioNamePattern.MatchString(step.Name) {
			return fmt.Errorf(
				"networkchaos: step %d name %q must be a bounded lowercase slug",
				stepIndex,
				step.Name,
			)
		}
		if _, found := stepNames[step.Name]; found {
			return fmt.Errorf("networkchaos: step name %q is duplicated", step.Name)
		}
		stepNames[step.Name] = struct{}{}
		if step.Hold <= 0 || step.Hold > config.MaxStepDuration {
			return fmt.Errorf(
				"networkchaos: step %q hold must be within (0, %s]",
				step.Name,
				config.MaxStepDuration,
			)
		}
		if len(step.Faults) == 0 || len(step.Faults) > maxFaultsPerStep {
			return fmt.Errorf(
				"networkchaos: step %q must contain between 1 and %d faults",
				step.Name,
				maxFaultsPerStep,
			)
		}

		faultNames := make(map[string]struct{}, len(step.Faults))
		mutationTargets := make(map[string]struct{}, len(step.Faults))
		for faultIndex, fault := range step.Faults {
			if err := validateFault(fault); err != nil {
				return fmt.Errorf(
					"networkchaos: step %q fault %d: %w",
					step.Name,
					faultIndex,
					err,
				)
			}
			if _, found := faultNames[fault.Name]; found {
				return fmt.Errorf(
					"networkchaos: step %q fault name %q is duplicated",
					step.Name,
					fault.Name,
				)
			}
			faultNames[fault.Name] = struct{}{}

			mutationTarget := fault.Container.ID + ":" + fault.Interface
			if _, found := mutationTargets[mutationTarget]; found {
				return fmt.Errorf(
					"networkchaos: step %q mutates container interface %q more than once",
					step.Name,
					fault.Container.Name+":"+fault.Interface,
				)
			}
			mutationTargets[mutationTarget] = struct{}{}
		}
	}

	return nil
}

func validateFault(fault Fault) error {
	if !scenarioNamePattern.MatchString(fault.Name) {
		return fmt.Errorf("fault name %q must be a bounded lowercase slug", fault.Name)
	}
	if err := validateRef("container", fault.Container); err != nil {
		return err
	}
	if err := validateRef("network", fault.Network); err != nil {
		return err
	}
	if !interfacePattern.MatchString(fault.Interface) {
		return fmt.Errorf("interface %q must be an explicit ethN name", fault.Interface)
	}

	switch fault.Kind {
	case KindPartition:
		if len(fault.PeerNetworks) == 0 || len(fault.PeerNetworks) > maxPeerNetworks {
			return fmt.Errorf(
				"partition must contain between 1 and %d peer networks",
				maxPeerNetworks,
			)
		}
		if fault.Delay != 0 || fault.Jitter != 0 || fault.LossPercent != 0 || fault.BandwidthKbit != 0 {
			return errors.New("partition must not contain degradation parameters")
		}
		seen := make(map[string]struct{}, len(fault.PeerNetworks))
		for _, peer := range fault.PeerNetworks {
			if err := validateRef("peer network", peer); err != nil {
				return err
			}
			if peer.ID == fault.Network.ID || peer.Name == fault.Network.Name {
				return errors.New("partition peer network must differ from primary network")
			}
			if _, found := seen[peer.ID]; found {
				return fmt.Errorf("peer network id %q is duplicated", peer.ID)
			}
			seen[peer.ID] = struct{}{}
		}
	case KindDegradation:
		if len(fault.PeerNetworks) != 0 {
			return errors.New("degradation must not contain peer networks")
		}
		if fault.Delay < 0 || fault.Jitter < 0 {
			return errors.New("delay and jitter must not be negative")
		}
		if fault.Delay > maxNetworkDelay || fault.Jitter > maxNetworkDelay {
			return fmt.Errorf("delay and jitter must not exceed %s", maxNetworkDelay)
		}
		if fault.Jitter > fault.Delay {
			return errors.New("jitter must not exceed delay")
		}
		if fault.Delay%time.Microsecond != 0 || fault.Jitter%time.Microsecond != 0 {
			return errors.New("delay and jitter must use whole microseconds")
		}
		isInvalidLoss := math.IsNaN(fault.LossPercent) || math.IsInf(fault.LossPercent, 0)
		if isInvalidLoss || fault.LossPercent < 0 || fault.LossPercent >= 100 {
			return errors.New("loss percent must be within [0, 100)")
		}
		if fault.BandwidthKbit > maxBandwidthKbit {
			return fmt.Errorf("bandwidth must not exceed %d kbit", maxBandwidthKbit)
		}
		if fault.BandwidthKbit > 0 && fault.BandwidthKbit < minBandwidthKbit {
			return fmt.Errorf("bandwidth must be at least %d kbit when configured", minBandwidthKbit)
		}
		if fault.Delay == 0 && fault.Jitter == 0 && fault.LossPercent == 0 && fault.BandwidthKbit == 0 {
			return errors.New("degradation must configure delay, jitter, loss or bandwidth")
		}
	default:
		return fmt.Errorf("fault kind %q is unsupported", fault.Kind)
	}

	return nil
}

func validateRef(kind string, ref ResourceRef) error {
	if !resourceIDPattern.MatchString(ref.ID) {
		return fmt.Errorf("%s id %q must be an explicit Docker id", kind, ref.ID)
	}
	if !resourceNamePattern.MatchString(ref.Name) {
		return fmt.Errorf("%s name %q is invalid", kind, ref.Name)
	}

	return nil
}
