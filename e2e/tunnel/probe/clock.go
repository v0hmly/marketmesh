package probe

import "time"

// Clock позволяет детерминированно проверять scheduling и monotonic timeline.
type Clock interface {
	Now() time.Time
	NewTicker(interval time.Duration) Ticker
}

// Ticker — минимальный lifecycle-safe интерфейс scheduler.
type Ticker interface {
	C() <-chan time.Time
	Stop()
}

type systemClock struct{}

type systemTicker struct {
	ticker *time.Ticker
}

func (systemClock) Now() time.Time {
	return time.Now()
}

func (systemClock) NewTicker(interval time.Duration) Ticker {
	return systemTicker{ticker: time.NewTicker(interval)}
}

func (ticker systemTicker) C() <-chan time.Time {
	return ticker.ticker.C
}

func (ticker systemTicker) Stop() {
	ticker.ticker.Stop()
}
