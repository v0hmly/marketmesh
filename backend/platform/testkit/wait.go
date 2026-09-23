package testkit

import (
	"testing"
	"time"
)

// Wait ожидает одно значение из channel не дольше timeout. Закрытый channel и
// превышение timeout завершают текущий тест без возможности бесконечного wait.
func Wait[T any](t testing.TB, timeout time.Duration, channel <-chan T) T {
	t.Helper()

	if timeout <= 0 {
		t.Fatalf("testkit: wait timeout must be positive")
	}
	if channel == nil {
		t.Fatalf("testkit: wait channel must not be nil")
	}

	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case value, open := <-channel:
		if !open {
			var zero T
			t.Fatalf("testkit: wait channel closed without a value")
			return zero
		}
		return value
	case <-timer.C:
		var zero T
		t.Fatalf("testkit: wait exceeded %s", timeout)
		return zero
	}
}

// Eventually проверяет condition сразу, а затем с bounded interval до timeout.
// Helper не создаёт неограниченных ожиданий и всегда останавливает свои timers.
func Eventually(
	t testing.TB,
	timeout time.Duration,
	interval time.Duration,
	condition func() bool,
) {
	t.Helper()

	if timeout <= 0 {
		t.Fatalf("testkit: eventually timeout must be positive")
	}
	if interval <= 0 {
		t.Fatalf("testkit: eventually interval must be positive")
	}
	if condition == nil {
		t.Fatalf("testkit: eventually condition must not be nil")
	}
	if condition() {
		return
	}

	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if condition() {
				return
			}
		case <-deadline.C:
			t.Fatalf("testkit: condition was not satisfied within %s", timeout)
			return
		}
	}
}
