package testkit

import (
	"sync"
	"testing"
	"time"
)

// Clock — изолированные управляемые часы без глобального состояния.
// Все методы безопасны для конкурентного использования.
type Clock struct {
	mu      sync.Mutex
	now     time.Time
	timers  map[*Timer]struct{}
	tickers map[*Ticker]struct{}
	isDone  bool
}

// Timer — управляемый аналог time.Timer для Clock.
type Timer struct {
	C <-chan time.Time

	channel  chan time.Time
	clock    *Clock
	deadline time.Time
	isActive bool
}

// Ticker — управляемый аналог time.Ticker для Clock.
type Ticker struct {
	C <-chan time.Time

	channel  chan time.Time
	clock    *Clock
	next     time.Time
	interval time.Duration
	isActive bool
}

// NewClock создаёт fake clock в start и останавливает все его timer/ticker в
// Cleanup. Экземпляры не разделяют состояние и подходят для t.Parallel.
func NewClock(t testing.TB, start time.Time) *Clock {
	t.Helper()

	clock := newClock(start)
	t.Cleanup(clock.stop)

	return clock
}

func newClock(start time.Time) *Clock {
	return &Clock{
		now:     start,
		timers:  make(map[*Timer]struct{}),
		tickers: make(map[*Ticker]struct{}),
	}
}

// Now возвращает текущее fake-время.
func (clock *Clock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()

	return clock.now
}

// NewTimer создаёт timer относительно текущего fake-времени. Неположительная
// duration делает timer готовым при следующем Advance, включая Advance(0).
func (clock *Clock) NewTimer(duration time.Duration) *Timer {
	clock.mu.Lock()
	defer clock.mu.Unlock()

	channel := make(chan time.Time, 1)
	timer := &Timer{
		C:        channel,
		channel:  channel,
		clock:    clock,
		deadline: clock.now.Add(duration),
		isActive: !clock.isDone,
	}
	if timer.isActive {
		clock.timers[timer] = struct{}{}
	}

	return timer
}

// NewTicker создаёт ticker относительно текущего fake-времени.
// Как и time.NewTicker, метод паникует для неположительного interval.
func (clock *Clock) NewTicker(interval time.Duration) *Ticker {
	if interval <= 0 {
		panic("testkit: non-positive ticker interval")
	}

	clock.mu.Lock()
	defer clock.mu.Unlock()

	channel := make(chan time.Time, 1)
	ticker := &Ticker{
		C:        channel,
		channel:  channel,
		clock:    clock,
		next:     clock.now.Add(interval),
		interval: interval,
		isActive: !clock.isDone,
	}
	if ticker.isActive {
		clock.tickers[ticker] = struct{}{}
	}

	return ticker
}

// Advance сдвигает fake-время и неблокирующе доставляет готовые события.
// Несчитанные ticker events объединяются так же, как у bounded time.Ticker.
func (clock *Clock) Advance(duration time.Duration) {
	if duration < 0 {
		panic("testkit: clock cannot move backwards")
	}

	clock.mu.Lock()
	defer clock.mu.Unlock()

	if clock.isDone {
		return
	}
	clock.now = clock.now.Add(duration)

	for timer := range clock.timers {
		if !timer.isActive || clock.now.Before(timer.deadline) {
			continue
		}
		select {
		case timer.channel <- timer.deadline:
		default:
		}
		timer.isActive = false
		delete(clock.timers, timer)
	}

	for ticker := range clock.tickers {
		if !ticker.isActive || clock.now.Before(ticker.next) {
			continue
		}
		tick := ticker.next
		select {
		case ticker.channel <- tick:
		default:
		}

		elapsed := clock.now.Sub(ticker.next)
		remainder := elapsed % ticker.interval
		ticker.next = clock.now.Add(ticker.interval - remainder)
	}
}

// Stop предотвращает последующую доставку события timer. Результат сообщает,
// был ли timer активен до вызова.
func (timer *Timer) Stop() bool {
	clock := timer.clock
	clock.mu.Lock()
	defer clock.mu.Unlock()

	wasActive := timer.isActive
	timer.isActive = false
	delete(clock.timers, timer)

	return wasActive
}

// Reset очищает недоставленное старое событие и планирует timer относительно
// текущего fake-времени. Результат сообщает, был ли timer активен.
func (timer *Timer) Reset(duration time.Duration) bool {
	clock := timer.clock
	clock.mu.Lock()
	defer clock.mu.Unlock()

	wasActive := timer.isActive
	drainTime(timer.channel)
	timer.deadline = clock.now.Add(duration)
	timer.isActive = !clock.isDone
	if timer.isActive {
		clock.timers[timer] = struct{}{}
	}

	return wasActive
}

// Stop предотвращает последующие события ticker и не закрывает C, повторяя
// контракт time.Ticker.
func (ticker *Ticker) Stop() {
	clock := ticker.clock
	clock.mu.Lock()
	defer clock.mu.Unlock()

	ticker.isActive = false
	delete(clock.tickers, ticker)
}

// Reset очищает недоставленное старое событие и устанавливает новый interval.
// Как и time.Ticker.Reset, метод паникует для неположительного interval.
func (ticker *Ticker) Reset(interval time.Duration) {
	if interval <= 0 {
		panic("testkit: non-positive ticker interval")
	}

	clock := ticker.clock
	clock.mu.Lock()
	defer clock.mu.Unlock()

	drainTime(ticker.channel)
	ticker.interval = interval
	ticker.next = clock.now.Add(interval)
	ticker.isActive = !clock.isDone
	if ticker.isActive {
		clock.tickers[ticker] = struct{}{}
	}
}

func (clock *Clock) stop() {
	clock.mu.Lock()
	defer clock.mu.Unlock()

	clock.isDone = true
	for timer := range clock.timers {
		timer.isActive = false
	}
	for ticker := range clock.tickers {
		ticker.isActive = false
	}
	clear(clock.timers)
	clear(clock.tickers)
}

func drainTime(channel chan time.Time) {
	select {
	case <-channel:
	default:
	}
}
