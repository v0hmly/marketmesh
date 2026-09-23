package networkchaos

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	maxResourceSamples    = 100_000
	maxResourceViolations = 64
)

// TrafficClass задаёт фиксированные низкокардинальные классы очередей tunnel.
type TrafficClass string

const (
	TrafficClassControl  TrafficClass = "control"
	TrafficClassAuth     TrafficClass = "auth"
	TrafficClassRealtime TrafficClass = "realtime"
)

var trafficClasses = [...]TrafficClass{
	TrafficClassControl,
	TrafficClassAuth,
	TrafficClassRealtime,
}

// ResourceSample — один безопасный snapshot процесса и bounded tunnel queues.
// Elapsed отсчитывается от baseline, чтобы gate не зависел от wall clock.
type ResourceSample struct {
	Elapsed    time.Duration
	Goroutines uint64
	HeapBytes  uint64
	QueueDepth map[TrafficClass]uint64
}

// ResourceLimits задаёт допустимое увеличение относительно baseline и
// абсолютный предел каждой очереди. Нулевой growth limit запрещает рост.
type ResourceLimits struct {
	MaxGoroutineGrowth uint64
	MaxHeapGrowthBytes uint64
	MaxQueueDepth      map[TrafficClass]uint64
}

// ResourceGateResult сохраняет bounded агрегаты без runtime payload и
// высококардинальных labels. Passed ложен при любом наблюдавшемся превышении.
type ResourceGateResult struct {
	Passed             bool
	SampleCount        int
	BaselineGoroutines uint64
	FinalGoroutines    uint64
	PeakGoroutines     uint64
	BaselineHeapBytes  uint64
	FinalHeapBytes     uint64
	PeakHeapBytes      uint64
	PeakQueueDepth     map[TrafficClass]uint64
	Violations         []string
}

// Gate возвращает ошибку при любом нарушении. Повторный успешный sample не
// может скрыть ранее зафиксированное превышение.
func (result ResourceGateResult) Gate() error {
	if result.Passed {
		return nil
	}
	if len(result.Violations) == 0 {
		return errors.New("networkchaos: resource gate failed without violation details")
	}

	return fmt.Errorf(
		"networkchaos: resource gate failed: %s",
		strings.Join(result.Violations, "; "),
	)
}

// EvaluateResources проверяет весь ordered sample ledger. Валидационная ошибка
// означает неизвестный интервал и сама по себе не может считаться pass.
func EvaluateResources(
	limits ResourceLimits,
	samples []ResourceSample,
) (ResourceGateResult, error) {
	if err := validateResourceLimits(limits); err != nil {
		return ResourceGateResult{}, err
	}
	if err := validateResourceSamples(samples); err != nil {
		return ResourceGateResult{}, err
	}

	baseline := samples[0]
	result := ResourceGateResult{
		Passed:             true,
		SampleCount:        len(samples),
		BaselineGoroutines: baseline.Goroutines,
		FinalGoroutines:    samples[len(samples)-1].Goroutines,
		PeakGoroutines:     baseline.Goroutines,
		BaselineHeapBytes:  baseline.HeapBytes,
		FinalHeapBytes:     samples[len(samples)-1].HeapBytes,
		PeakHeapBytes:      baseline.HeapBytes,
		PeakQueueDepth:     make(map[TrafficClass]uint64, len(trafficClasses)),
		Violations:         []string{},
	}

	for sampleIndex, sample := range samples {
		result.PeakGoroutines = max(result.PeakGoroutines, sample.Goroutines)
		result.PeakHeapBytes = max(result.PeakHeapBytes, sample.HeapBytes)
		if exceedsGrowth(
			sample.Goroutines,
			baseline.Goroutines,
			limits.MaxGoroutineGrowth,
		) {
			result.addViolation(fmt.Sprintf(
				"sample %d goroutines %d exceed baseline %d plus growth %d",
				sampleIndex,
				sample.Goroutines,
				baseline.Goroutines,
				limits.MaxGoroutineGrowth,
			))
		}
		if exceedsGrowth(sample.HeapBytes, baseline.HeapBytes, limits.MaxHeapGrowthBytes) {
			result.addViolation(fmt.Sprintf(
				"sample %d heap bytes %d exceed baseline %d plus growth %d",
				sampleIndex,
				sample.HeapBytes,
				baseline.HeapBytes,
				limits.MaxHeapGrowthBytes,
			))
		}
		for _, class := range trafficClasses {
			depth := sample.QueueDepth[class]
			result.PeakQueueDepth[class] = max(result.PeakQueueDepth[class], depth)
			if depth > limits.MaxQueueDepth[class] {
				result.addViolation(fmt.Sprintf(
					"sample %d %s queue depth %d exceeds limit %d",
					sampleIndex,
					class,
					depth,
					limits.MaxQueueDepth[class],
				))
			}
		}
	}

	return result, nil
}

func (result *ResourceGateResult) addViolation(violation string) {
	result.Passed = false
	if len(result.Violations) < maxResourceViolations {
		result.Violations = append(result.Violations, violation)
	}
}

func validateResourceLimits(limits ResourceLimits) error {
	if len(limits.MaxQueueDepth) != len(trafficClasses) {
		return errors.New("networkchaos: queue limits must contain exactly control, auth and realtime")
	}
	for _, class := range trafficClasses {
		limit, found := limits.MaxQueueDepth[class]
		if !found {
			return fmt.Errorf("networkchaos: queue limit for %q is missing", class)
		}
		if limit == 0 {
			return fmt.Errorf("networkchaos: queue limit for %q must be positive", class)
		}
	}

	return nil
}

func validateResourceSamples(samples []ResourceSample) error {
	if len(samples) < 2 || len(samples) > maxResourceSamples {
		return fmt.Errorf(
			"networkchaos: resource ledger must contain between 2 and %d samples",
			maxResourceSamples,
		)
	}
	if samples[0].Elapsed != 0 {
		return errors.New("networkchaos: first resource sample must be the zero-time baseline")
	}

	previousElapsed := time.Duration(-1)
	for sampleIndex, sample := range samples {
		if sample.Elapsed <= previousElapsed {
			return fmt.Errorf(
				"networkchaos: resource sample %d elapsed time is not strictly increasing",
				sampleIndex,
			)
		}
		if sample.Elapsed > maxBoundedDuration {
			return fmt.Errorf(
				"networkchaos: resource sample %d exceeds maximum duration %s",
				sampleIndex,
				maxBoundedDuration,
			)
		}
		if sample.Goroutines == 0 {
			return fmt.Errorf("networkchaos: resource sample %d has no goroutines", sampleIndex)
		}
		if sample.HeapBytes == 0 {
			return fmt.Errorf("networkchaos: resource sample %d has no heap measurement", sampleIndex)
		}
		if len(sample.QueueDepth) != len(trafficClasses) {
			return fmt.Errorf(
				"networkchaos: resource sample %d must contain exactly three queue classes",
				sampleIndex,
			)
		}
		for _, class := range trafficClasses {
			if _, found := sample.QueueDepth[class]; !found {
				return fmt.Errorf(
					"networkchaos: resource sample %d queue class %q is missing",
					sampleIndex,
					class,
				)
			}
		}
		previousElapsed = sample.Elapsed
	}

	return nil
}

func exceedsGrowth(current uint64, baseline uint64, allowed uint64) bool {
	return current > baseline && current-baseline > allowed
}
