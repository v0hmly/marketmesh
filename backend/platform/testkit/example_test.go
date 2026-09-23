package testkit

import (
	"fmt"
	"time"
)

func ExampleClock_Advance() {
	start := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
	clock := newClock(start)
	timer := clock.NewTimer(time.Minute)

	clock.Advance(time.Minute)
	fmt.Println((<-timer.C).Format(time.RFC3339))
	// Output: 2026-09-01T12:01:00Z
}
