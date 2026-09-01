package testkit_test

import (
	"sync/atomic"
	"testing"
	"time"

	"github.com/v0hmly/marketmesh/platform/testkit"
)

func TestWaitReturnsChannelValue(t *testing.T) {
	t.Parallel()

	values := make(chan int, 1)
	values <- 42
	if value := testkit.Wait(t, time.Second, values); value != 42 {
		t.Fatalf("Wait() = %d, want 42", value)
	}
}

func TestEventuallyChecksImmediatelyAndPolls(t *testing.T) {
	t.Parallel()

	var attempts atomic.Int32
	testkit.Eventually(t, time.Second, time.Millisecond, func() bool {
		return attempts.Add(1) == 3
	})
	if attempts.Load() != 3 {
		t.Fatalf("condition attempts = %d, want 3", attempts.Load())
	}
}
