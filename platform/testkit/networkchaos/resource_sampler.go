package networkchaos

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sync/atomic"
	"time"
)

const maxResourceReadTimeout = time.Minute

// ResourceObservation — одна фактическая low-cardinality выборка процесса и
// bounded tunnel queues. Источник не должен включать payload или request IDs.
type ResourceObservation struct {
	Goroutines uint64
	HeapBytes  uint64
	QueueDepth map[TrafficClass]uint64
}

// ResourceSource читает одну bounded выборку из явно настроенного target.
type ResourceSource interface {
	Read(ctx context.Context) (ResourceObservation, error)
}

// ResourceSamplerConfig ограничивает частоту, каждое чтение и общий объём
// ledger. Общую длительность ограничивает обязательный deadline Run context.
type ResourceSamplerConfig struct {
	PollInterval time.Duration
	ReadTimeout  time.Duration
	MaxSamples   int
}

// ResourceSampler собирает один ordered ledger параллельно soak traffic.
// Повторное или конкурентное использование запрещено.
type ResourceSampler struct {
	config ResourceSamplerConfig
	source ResourceSource
	clock  resourceSamplerClock
	ready  chan struct{}
	used   atomic.Bool
}

type resourceSamplerClock interface {
	Now() time.Time
	NewTicker(time.Duration) resourceSamplerTicker
}

type resourceSamplerTicker interface {
	C() <-chan time.Time
	Stop()
}

type systemResourceSamplerClock struct{}

func (systemResourceSamplerClock) Now() time.Time {
	return time.Now()
}

func (systemResourceSamplerClock) NewTicker(interval time.Duration) resourceSamplerTicker {
	return systemResourceSamplerTicker{ticker: time.NewTicker(interval)}
}

type systemResourceSamplerTicker struct {
	ticker *time.Ticker
}

func (ticker systemResourceSamplerTicker) C() <-chan time.Time {
	return ticker.ticker.C
}

func (ticker systemResourceSamplerTicker) Stop() {
	ticker.ticker.Stop()
}

// NewResourceSampler проверяет bounded config и явный source.
func NewResourceSampler(
	config ResourceSamplerConfig,
	source ResourceSource,
) (*ResourceSampler, error) {
	return newResourceSampler(config, source, systemResourceSamplerClock{})
}

func newResourceSampler(
	config ResourceSamplerConfig,
	source ResourceSource,
	clock resourceSamplerClock,
) (*ResourceSampler, error) {
	if config.PollInterval <= 0 || config.PollInterval > maxBoundedDuration {
		return nil, errors.New("networkchaos: resource poll interval is outside bounds")
	}
	if config.ReadTimeout <= 0 || config.ReadTimeout > maxResourceReadTimeout {
		return nil, errors.New("networkchaos: resource read timeout is outside bounds")
	}
	if config.MaxSamples < 2 || config.MaxSamples > maxResourceSamples {
		return nil, fmt.Errorf(
			"networkchaos: resource max samples must be between 2 and %d",
			maxResourceSamples,
		)
	}
	if isNilDependency(source) {
		return nil, errors.New("networkchaos: resource source must not be nil")
	}
	if isNilDependency(clock) {
		return nil, errors.New("networkchaos: resource sampler clock must not be nil")
	}
	return &ResourceSampler{
		config: config,
		source: source,
		clock:  clock,
		ready:  make(chan struct{}),
	}, nil
}

// Ready закрывается ровно после успешного baseline sample. Если Run завершился
// ошибкой раньше baseline, канал остаётся открытым, а caller обязан выбрать
// результат Run и не начинать fault mutation.
func (sampler *ResourceSampler) Ready() <-chan struct{} {
	return sampler.ready
}

// Run читает baseline немедленно, затем PollInterval samples до закрытия stop.
// stop — единственный успешный terminal signal. Истечение ctx, ошибка source,
// недостаточный ledger или достижение MaxSamples до stop возвращают ошибку.
func (sampler *ResourceSampler) Run(
	ctx context.Context,
	stop <-chan struct{},
) ([]ResourceSample, error) {
	if ctx == nil {
		return nil, errors.New("networkchaos: resource sampler context must not be nil")
	}
	deadline, hasDeadline := ctx.Deadline()
	if !hasDeadline {
		return nil, errors.New("networkchaos: resource sampler context must have a bounded deadline")
	}
	remaining := time.Until(deadline)
	if remaining <= 0 || remaining > maxBoundedDuration {
		return nil, errors.New("networkchaos: resource sampler context must have a bounded deadline")
	}
	if stop == nil {
		return nil, errors.New("networkchaos: resource sampler stop channel must not be nil")
	}
	if !sampler.used.CompareAndSwap(false, true) {
		return nil, errors.New("networkchaos: resource sampler has already been used")
	}

	startedAt := sampler.clock.Now()
	baseline, err := sampler.read(ctx)
	if err != nil {
		return nil, err
	}
	samples := []ResourceSample{sampleFromObservation(startedAt, startedAt, baseline)}
	close(sampler.ready)
	ticker := sampler.clock.NewTicker(sampler.config.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			if ctxErr := ctx.Err(); ctxErr != nil {
				return samples, fmt.Errorf("networkchaos: resource sampler deadline: %w", ctxErr)
			}
			if len(samples) < 2 {
				return samples, errors.New("networkchaos: resource sampler stopped before two samples")
			}
			return samples, nil
		case <-ctx.Done():
			return samples, fmt.Errorf("networkchaos: resource sampler deadline: %w", ctx.Err())
		case sampledAt := <-ticker.C():
			if len(samples) >= sampler.config.MaxSamples {
				return samples, errors.New("networkchaos: resource sampler reached max samples before stop")
			}
			observation, readErr := sampler.read(ctx)
			if readErr != nil {
				return samples, readErr
			}
			sample := sampleFromObservation(startedAt, sampledAt, observation)
			if sample.Elapsed <= samples[len(samples)-1].Elapsed {
				return samples, errors.New("networkchaos: resource sampler clock did not advance")
			}
			samples = append(samples, sample)
		}
	}
}

func (sampler *ResourceSampler) read(ctx context.Context) (ResourceObservation, error) {
	readCtx, cancel := context.WithTimeout(ctx, sampler.config.ReadTimeout)
	defer cancel()
	observation, err := sampler.source.Read(readCtx)
	if err != nil {
		return ResourceObservation{}, fmt.Errorf("networkchaos: reading resource observation: %w", err)
	}
	if err := validateResourceObservation(observation); err != nil {
		return ResourceObservation{}, err
	}
	observation.QueueDepth = maps.Clone(observation.QueueDepth)
	return observation, nil
}

func validateResourceObservation(observation ResourceObservation) error {
	if observation.Goroutines == 0 {
		return errors.New("networkchaos: resource observation has no goroutines")
	}
	if observation.HeapBytes == 0 {
		return errors.New("networkchaos: resource observation has no heap measurement")
	}
	if len(observation.QueueDepth) != len(trafficClasses) {
		return errors.New("networkchaos: resource observation must contain exactly three queue classes")
	}
	for _, class := range trafficClasses {
		if _, found := observation.QueueDepth[class]; !found {
			return fmt.Errorf("networkchaos: resource observation queue class %q is missing", class)
		}
	}
	return nil
}

func sampleFromObservation(
	startedAt time.Time,
	sampledAt time.Time,
	observation ResourceObservation,
) ResourceSample {
	return ResourceSample{
		Elapsed:    sampledAt.Sub(startedAt),
		Goroutines: observation.Goroutines,
		HeapBytes:  observation.HeapBytes,
		QueueDepth: maps.Clone(observation.QueueDepth),
	}
}
