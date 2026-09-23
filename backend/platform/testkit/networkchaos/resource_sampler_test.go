package networkchaos

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeResourceSource struct {
	mu           sync.Mutex
	observations []ResourceObservation
	errAt        int
	calls        chan int
}

func (source *fakeResourceSource) Read(context.Context) (ResourceObservation, error) {
	source.mu.Lock()
	defer source.mu.Unlock()
	call := len(source.observations)
	if source.calls != nil {
		source.calls <- call
	}
	if source.errAt == call+1 {
		return ResourceObservation{}, errors.New("metrics unavailable")
	}
	observation := validResourceObservation(uint64(call))
	source.observations = append(source.observations, observation)
	return observation, nil
}

type fakeResourceSamplerClock struct {
	mu      sync.Mutex
	now     time.Time
	tickers []*fakeResourceSamplerTicker
	created chan struct{}
}

func newFakeResourceSamplerClock() *fakeResourceSamplerClock {
	return &fakeResourceSamplerClock{
		now:     time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC),
		created: make(chan struct{}, 1),
	}
}

func (clock *fakeResourceSamplerClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *fakeResourceSamplerClock) NewTicker(time.Duration) resourceSamplerTicker {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	ticker := &fakeResourceSamplerTicker{channel: make(chan time.Time, 1)}
	clock.tickers = append(clock.tickers, ticker)
	clock.created <- struct{}{}
	return ticker
}

func (clock *fakeResourceSamplerClock) Advance(duration time.Duration) {
	clock.mu.Lock()
	clock.now = clock.now.Add(duration)
	now := clock.now
	tickers := slices.Clone(clock.tickers)
	clock.mu.Unlock()
	for _, ticker := range tickers {
		ticker.channel <- now
	}
}

type fakeResourceSamplerTicker struct {
	channel chan time.Time
}

func (ticker *fakeResourceSamplerTicker) C() <-chan time.Time {
	return ticker.channel
}

func (*fakeResourceSamplerTicker) Stop() {}

type resourceSamplerResult struct {
	samples []ResourceSample
	err     error
}

func TestResourceSamplerBuildsOrderedDefensiveLedgerUntilExplicitStop(t *testing.T) {
	t.Parallel()

	source := &fakeResourceSource{calls: make(chan int, 4)}
	clock := newFakeResourceSamplerClock()
	sampler := testResourceSampler(t, source, clock, 4)
	select {
	case <-sampler.Ready():
		t.Fatal("Ready() closed before Run baseline")
	default:
	}
	stop := make(chan struct{})
	resultChannel := make(chan resourceSamplerResult, 1)
	runCtx := boundedResourceTestContext(t)
	go func() {
		samples, err := sampler.Run(runCtx, stop)
		resultChannel <- resourceSamplerResult{samples: samples, err: err}
	}()

	waitResourceRead(t, source.calls)
	waitResourceTicker(t, clock.created)
	select {
	case <-sampler.Ready():
	default:
		t.Fatal("Ready() remains open after accepted baseline")
	}
	clock.Advance(time.Second)
	waitResourceRead(t, source.calls)
	close(stop)
	result := waitResourceSamplerResult(t, resultChannel)
	if result.err != nil {
		t.Fatalf("Run() error = %v", result.err)
	}
	if len(result.samples) != 2 || result.samples[0].Elapsed != 0 ||
		result.samples[1].Elapsed != time.Second {
		t.Fatalf("samples = %+v, want baseline and one ordered sample", result.samples)
	}
	source.observations[0].QueueDepth[TrafficClassControl] = 999
	if result.samples[0].QueueDepth[TrafficClassControl] == 999 {
		t.Fatal("Run() returned source-owned queue map")
	}
	if _, err := EvaluateResources(testResourceLimits(), result.samples); err != nil {
		t.Fatalf("EvaluateResources() error = %v", err)
	}
	if _, err := sampler.Run(runCtx, make(chan struct{})); err == nil {
		t.Fatal("second Run() error = nil")
	}
}

func TestResourceSamplerFailsClosedOnReadErrorAndSampleLimit(t *testing.T) {
	t.Parallel()

	t.Run("baseline error", func(t *testing.T) {
		t.Parallel()
		source := &fakeResourceSource{errAt: 1, calls: make(chan int, 1)}
		clock := newFakeResourceSamplerClock()
		sampler := testResourceSampler(t, source, clock, 2)
		resultChannel := make(chan resourceSamplerResult, 1)
		runCtx := boundedResourceTestContext(t)
		go func() {
			samples, err := sampler.Run(runCtx, make(chan struct{}))
			resultChannel <- resourceSamplerResult{samples: samples, err: err}
		}()
		waitResourceRead(t, source.calls)
		result := waitResourceSamplerResult(t, resultChannel)
		if result.err == nil || !strings.Contains(result.err.Error(), "metrics unavailable") {
			t.Fatalf("Run() error = %v, want baseline source failure", result.err)
		}
		if len(result.samples) != 0 {
			t.Fatalf("samples = %v, want no accepted baseline", result.samples)
		}
		select {
		case <-sampler.Ready():
			t.Fatal("Ready() closed after rejected baseline")
		default:
		}
	})

	t.Run("read error", func(t *testing.T) {
		t.Parallel()
		source := &fakeResourceSource{errAt: 2, calls: make(chan int, 2)}
		clock := newFakeResourceSamplerClock()
		sampler := testResourceSampler(t, source, clock, 3)
		resultChannel := make(chan resourceSamplerResult, 1)
		runCtx := boundedResourceTestContext(t)
		go func() {
			samples, err := sampler.Run(runCtx, make(chan struct{}))
			resultChannel <- resourceSamplerResult{samples: samples, err: err}
		}()
		waitResourceRead(t, source.calls)
		waitResourceTicker(t, clock.created)
		clock.Advance(time.Second)
		result := waitResourceSamplerResult(t, resultChannel)
		if result.err == nil || !strings.Contains(result.err.Error(), "metrics unavailable") {
			t.Fatalf("Run() error = %v, want source failure", result.err)
		}
		if len(result.samples) != 1 {
			t.Fatalf("samples = %v, want preserved baseline", result.samples)
		}
	})

	t.Run("sample limit", func(t *testing.T) {
		t.Parallel()
		source := &fakeResourceSource{calls: make(chan int, 4)}
		clock := newFakeResourceSamplerClock()
		sampler := testResourceSampler(t, source, clock, 2)
		resultChannel := make(chan resourceSamplerResult, 1)
		runCtx := boundedResourceTestContext(t)
		go func() {
			samples, err := sampler.Run(runCtx, make(chan struct{}))
			resultChannel <- resourceSamplerResult{samples: samples, err: err}
		}()
		waitResourceRead(t, source.calls)
		waitResourceTicker(t, clock.created)
		clock.Advance(time.Second)
		waitResourceRead(t, source.calls)
		clock.Advance(time.Second)
		result := waitResourceSamplerResult(t, resultChannel)
		if result.err == nil || result.err.Error() !=
			"networkchaos: resource sampler reached max samples before stop" {
			t.Fatalf("Run() error = %v, want sample limit", result.err)
		}
		if len(result.samples) != 2 {
			t.Fatalf("samples = %v, want bounded ledger", result.samples)
		}
	})
}

func TestResourceSamplerRejectsInvalidObservationAndUnboundedContext(t *testing.T) {
	t.Parallel()

	source := &fakeResourceSource{}
	clock := newFakeResourceSamplerClock()
	sampler := testResourceSampler(t, source, clock, 2)
	if _, err := sampler.Run(context.Background(), make(chan struct{})); err == nil {
		t.Fatal("Run() error = nil for unbounded context")
	}

	invalid := validResourceObservation(0)
	delete(invalid.QueueDepth, TrafficClassAuth)
	if err := validateResourceObservation(invalid); err == nil {
		t.Fatal("validateResourceObservation() error = nil")
	}
}

func TestResourceSamplerTreatsContextCancellationAsFailure(t *testing.T) {
	t.Parallel()

	source := &fakeResourceSource{calls: make(chan int, 2)}
	clock := newFakeResourceSamplerClock()
	sampler := testResourceSampler(t, source, clock, 2)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	resultChannel := make(chan resourceSamplerResult, 1)
	go func() {
		samples, err := sampler.Run(ctx, make(chan struct{}))
		resultChannel <- resourceSamplerResult{samples: samples, err: err}
	}()
	waitResourceRead(t, source.calls)
	waitResourceTicker(t, clock.created)
	cancel()
	result := waitResourceSamplerResult(t, resultChannel)
	if result.err == nil || !errors.Is(result.err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context cancellation", result.err)
	}
	if len(result.samples) != 1 {
		t.Fatalf("samples = %v, want preserved baseline", result.samples)
	}
}

func testResourceSampler(
	t *testing.T,
	source ResourceSource,
	clock resourceSamplerClock,
	maxSamples int,
) *ResourceSampler {
	t.Helper()
	sampler, err := newResourceSampler(ResourceSamplerConfig{
		PollInterval: time.Second,
		ReadTimeout:  time.Second,
		MaxSamples:   maxSamples,
	}, source, clock)
	if err != nil {
		t.Fatalf("newResourceSampler() error = %v", err)
	}
	return sampler
}

func validResourceObservation(offset uint64) ResourceObservation {
	return ResourceObservation{
		Goroutines: 10 + offset,
		HeapBytes:  1024 + offset,
		QueueDepth: map[TrafficClass]uint64{
			TrafficClassControl:  offset,
			TrafficClassAuth:     offset,
			TrafficClassRealtime: offset,
		},
	}
}

func testResourceLimits() ResourceLimits {
	return ResourceLimits{
		MaxGoroutineGrowth: 5,
		MaxHeapGrowthBytes: 512,
		MaxQueueDepth: map[TrafficClass]uint64{
			TrafficClassControl:  10,
			TrafficClassAuth:     10,
			TrafficClassRealtime: 10,
		},
	}
}

func waitResourceRead(t *testing.T, calls <-chan int) {
	t.Helper()
	select {
	case <-calls:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for resource read")
	}
}

func waitResourceTicker(t *testing.T, created <-chan struct{}) {
	t.Helper()
	select {
	case <-created:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for resource ticker")
	}
}

func boundedResourceTestContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func waitResourceSamplerResult(
	t *testing.T,
	results <-chan resourceSamplerResult,
) resourceSamplerResult {
	t.Helper()
	select {
	case result := <-results:
		return result
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for resource sampler")
		return resourceSamplerResult{}
	}
}

func TestResourceSampleClonesQueueDepth(t *testing.T) {
	t.Parallel()

	observation := validResourceObservation(0)
	startedAt := time.Time{}
	sample := sampleFromObservation(startedAt, startedAt.Add(time.Second), observation)
	if !maps.Equal(sample.QueueDepth, observation.QueueDepth) {
		t.Fatalf("queue depth = %v, want %v", sample.QueueDepth, observation.QueueDepth)
	}
	observation.QueueDepth[TrafficClassControl] = 99
	if sample.QueueDepth[TrafficClassControl] == 99 {
		t.Fatal("sampleFromObservation() retained caller-owned map")
	}
}
