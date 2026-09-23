package testkit_test

import (
	"sync"
	"testing"
	"time"

	"github.com/v0hmly/marketmesh/platform/testkit"
)

func TestClockControlsTimerAndTicker(t *testing.T) {
	t.Parallel()

	start := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
	clock := testkit.NewClock(t, start)
	timer := clock.NewTimer(10 * time.Second)
	ticker := clock.NewTicker(3 * time.Second)

	clock.Advance(3 * time.Second)
	if tick := testkit.Wait(t, time.Second, ticker.C); !tick.Equal(start.Add(3 * time.Second)) {
		t.Fatalf("ticker = %s, want %s", tick, start.Add(3*time.Second))
	}
	select {
	case value := <-timer.C:
		t.Fatalf("timer fired early at %s", value)
	default:
	}

	clock.Advance(7 * time.Second)
	if fired := testkit.Wait(t, time.Second, timer.C); !fired.Equal(start.Add(10 * time.Second)) {
		t.Fatalf("timer = %s, want %s", fired, start.Add(10*time.Second))
	}
	if clock.Now() != start.Add(10*time.Second) {
		t.Fatalf("clock now = %s", clock.Now())
	}
}

func TestClockResetAndStop(t *testing.T) {
	t.Parallel()

	clock := testkit.NewClock(t, time.Time{})
	timer := clock.NewTimer(time.Second)
	if !timer.Stop() {
		t.Fatal("active timer reported stopped")
	}
	if timer.Reset(2 * time.Second) {
		t.Fatal("stopped timer reported active during reset")
	}
	clock.Advance(2 * time.Second)
	testkit.Wait(t, time.Second, timer.C)

	ticker := clock.NewTicker(time.Second)
	ticker.Reset(2 * time.Second)
	clock.Advance(2 * time.Second)
	testkit.Wait(t, time.Second, ticker.C)
	ticker.Stop()
	clock.Advance(2 * time.Second)
	select {
	case tick := <-ticker.C:
		t.Fatalf("stopped ticker fired at %s", tick)
	default:
	}
}

func TestClockIsConcurrentSafe(t *testing.T) {
	t.Parallel()

	clock := testkit.NewClock(t, time.Time{})
	const workers = 32
	var group sync.WaitGroup
	for range workers {
		group.Go(func() {
			timer := clock.NewTimer(time.Second)
			timer.Reset(2 * time.Second)
			timer.Stop()
			_ = clock.Now()
		})
	}
	group.Wait()
	clock.Advance(2 * time.Second)
}

func TestClockCleanupStopsOwnedResources(t *testing.T) {
	t.Parallel()

	var clock *testkit.Clock
	var timer *testkit.Timer
	t.Run("owner", func(t *testing.T) {
		clock = testkit.NewClock(t, time.Time{})
		timer = clock.NewTimer(time.Second)
	})

	clock.Advance(time.Second)
	select {
	case value := <-timer.C:
		t.Fatalf("timer fired after owner cleanup at %s", value)
	default:
	}
}
