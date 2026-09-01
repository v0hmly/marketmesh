package probe

import (
	"context"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type invokerFunc func(ctx context.Context, request Request) Response

func (function invokerFunc) Invoke(ctx context.Context, request Request) Response {
	return function(ctx, request)
}

type nilInvoker struct{}

func (*nilInvoker) Invoke(context.Context, Request) Response {
	return Response{}
}

type nilClock struct{}

func (*nilClock) Now() time.Time {
	return time.Time{}
}

func (*nilClock) NewTicker(time.Duration) Ticker {
	return nil
}

type nilIDGenerator struct{}

func (*nilIDGenerator) Next() string {
	return ""
}

type sequenceIDGenerator struct {
	current atomic.Uint64
}

func (generator *sequenceIDGenerator) Next() string {
	return formatRequestID(generator.current.Add(1))
}

type manualClock struct {
	mu            sync.Mutex
	now           time.Time
	tickers       []*manualTicker
	tickerCreated chan struct{}
}

type manualTicker struct {
	mu        sync.Mutex
	channel   chan time.Time
	stopped   chan struct{}
	isStopped bool
}

func newManualClock(t *testing.T) *manualClock {
	t.Helper()

	return &manualClock{
		now:           time.Date(2026, time.September, 1, 10, 0, 0, 0, time.UTC),
		tickerCreated: make(chan struct{}, 4),
	}
}

func (clock *manualClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()

	return clock.now
}

func (clock *manualClock) NewTicker(time.Duration) Ticker {
	ticker := &manualTicker{
		channel: make(chan time.Time),
		stopped: make(chan struct{}),
	}
	clock.mu.Lock()
	clock.tickers = append(clock.tickers, ticker)
	clock.mu.Unlock()
	clock.tickerCreated <- struct{}{}

	return ticker
}

func (clock *manualClock) waitForTickerStops(t *testing.T, count int) {
	t.Helper()

	clock.mu.Lock()
	tickers := append([]*manualTicker{}, clock.tickers...)
	clock.mu.Unlock()
	if len(tickers) < count {
		t.Fatalf("ticker count = %d, want at least %d", len(tickers), count)
	}

	for _, ticker := range tickers[:count] {
		timer := time.NewTimer(time.Second)
		select {
		case <-ticker.stopped:
			timer.Stop()
		case <-timer.C:
			t.Fatal("timed out waiting for ticker stop")
		}
	}
}

func (clock *manualClock) waitForTickers(t *testing.T, count int) {
	t.Helper()

	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	for range count {
		select {
		case <-clock.tickerCreated:
		case <-timer.C:
			t.Fatal("timed out waiting for ticker creation")
		}
	}
}

func (clock *manualClock) advance(t *testing.T, duration time.Duration) {
	t.Helper()

	clock.mu.Lock()
	clock.now = clock.now.Add(duration)
	now := clock.now
	tickers := append([]*manualTicker{}, clock.tickers...)
	clock.mu.Unlock()

	for _, ticker := range tickers {
		ticker.mu.Lock()
		isStopped := ticker.isStopped
		ticker.mu.Unlock()
		if isStopped {
			continue
		}

		timer := time.NewTimer(time.Second)
		select {
		case ticker.channel <- now:
			timer.Stop()
		case <-timer.C:
			t.Fatal("timed out delivering manual tick")
		}
	}
}

func (ticker *manualTicker) C() <-chan time.Time {
	return ticker.channel
}

func (ticker *manualTicker) Stop() {
	ticker.mu.Lock()
	defer ticker.mu.Unlock()

	if ticker.isStopped {
		return
	}
	ticker.isStopped = true
	close(ticker.stopped)
}

func defaultTestConfig() Config {
	return Config{
		RunTimeout:     time.Second,
		StopTimeout:    time.Second,
		RequestTimeout: 100 * time.Millisecond,
		Read: StreamConfig{
			RPS:           10,
			Concurrency:   1,
			QueueCapacity: 2,
		},
		RecordCapacity: 16,
		EventCapacity:  64,
	}
}

func waitValue[T any](t *testing.T, channel <-chan T) T {
	t.Helper()

	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case value := <-channel:
		return value
	case <-timer.C:
		var zero T
		t.Fatal("timed out waiting for test value")
		return zero
	}
}

func waitForOutcome(t *testing.T, runner *Runner, outcome Outcome, count int) {
	t.Helper()

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		runner.stateMu.Lock()
		clientJournal := runner.journal
		runner.stateMu.Unlock()
		if clientJournal != nil {
			clientJournal.mu.Lock()
			matched := 0
			for _, record := range clientJournal.records {
				if record.Outcome == outcome {
					matched++
				}
			}
			clientJournal.mu.Unlock()
			if matched >= count {
				return
			}
		}
		runtime.Gosched()
	}
	t.Fatalf("timed out waiting for %d %q outcomes", count, outcome)
}

func formatRequestID(value uint64) string {
	const hexDigits = "0123456789abcdef"

	var result [requestIDSize * 2]byte
	for index := range result {
		result[index] = '0'
	}
	for index := len(result) - 1; value > 0; index-- {
		result[index] = hexDigits[value&0xf]
		value >>= 4
	}

	return string(result[:])
}
