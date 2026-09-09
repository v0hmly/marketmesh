// Package publishregistration delivers durable registration events with leased retries.
package publishregistration

import (
	"context"
	"errors"
	"sync/atomic"
	"time"
)

type Record struct {
	ID         [16]byte
	LeaseToken [16]byte
	Payload    []byte
	Attempts   int
	OccurredAt time.Time
}
type Store interface {
	Claim(context.Context, time.Duration) (Record, bool, error)
	MarkPublished(context.Context, Record) (bool, error)
	Retry(context.Context, Record, time.Duration) (bool, error)
	Pending(context.Context) (int64, time.Time, error)
}
type Publisher interface {
	Publish(context.Context, Record) error
}
type Outcome string

const (
	OutcomePublished    Outcome = "published"
	OutcomePublishError Outcome = "publish_error"
	OutcomeStoreError   Outcome = "store_error"
	OutcomeLeaseLost    Outcome = "lease_lost"
)

type Observer interface {
	ObserveBacklog(context.Context, int64, time.Duration)
	Attempt(context.Context, Outcome)
}
type Config struct {
	LeaseDuration, PublishTimeout, PollInterval, RetryInitial, RetryMax time.Duration
	BatchSize                                                           int
	Clock                                                               func() time.Time
}
type Worker struct {
	store     Store
	publisher Publisher
	observer  Observer
	config    Config
	started   atomic.Bool
}

func New(store Store, publisher Publisher, observer Observer, c Config) (*Worker, error) {
	if store == nil || publisher == nil || observer == nil {
		return nil, errors.New("registration publisher: dependencies required")
	}
	if c.LeaseDuration == 0 {
		c.LeaseDuration = 15 * time.Second
	}
	if c.PublishTimeout == 0 {
		c.PublishTimeout = 5 * time.Second
	}
	if c.PollInterval == 0 {
		c.PollInterval = time.Second
	}
	if c.RetryInitial == 0 {
		c.RetryInitial = time.Second
	}
	if c.RetryMax == 0 {
		c.RetryMax = time.Minute
	}
	if c.BatchSize == 0 {
		c.BatchSize = 32
	}
	if c.Clock == nil {
		c.Clock = time.Now
	}
	if c.LeaseDuration <= c.PublishTimeout || c.LeaseDuration > time.Hour || c.PublishTimeout <= 0 || c.PollInterval <= 0 || c.PollInterval > time.Hour || c.RetryInitial < time.Millisecond || c.RetryMax < c.RetryInitial || c.RetryMax > 24*time.Hour || c.BatchSize < 1 || c.BatchSize > 1024 {
		return nil, errors.New("registration publisher: invalid configuration")
	}
	return &Worker{store: store, publisher: publisher, observer: observer, config: c}, nil
}

// Run tolerates dependency outages; only cancellation ends the background worker.
func (w *Worker) Run(ctx context.Context) error {
	if ctx == nil {
		return errors.New("registration publisher: context required")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !w.started.CompareAndSwap(false, true) {
		return errors.New("registration publisher: worker already started")
	}
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			w.batch(ctx)
			timer.Reset(w.config.PollInterval)
		}
	}
}
func (w *Worker) batch(ctx context.Context) {
	if ctx.Err() != nil {
		return
	}
	count, oldest, err := w.store.Pending(ctx)
	if ctx.Err() != nil {
		return
	}
	if err != nil {
		w.observer.Attempt(ctx, OutcomeStoreError)
	} else {
		age := time.Duration(0)
		if count > 0 && !oldest.IsZero() {
			age = w.config.Clock().Sub(oldest)
			if age < 0 {
				age = 0
			}
		}
		w.observer.ObserveBacklog(ctx, count, age)
	}
	for range w.config.BatchSize {
		if ctx.Err() != nil {
			return
		}
		record, found, err := w.store.Claim(ctx, w.config.LeaseDuration)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			w.observer.Attempt(ctx, OutcomeStoreError)
			return
		}
		if !found {
			return
		}
		publishCtx, cancel := context.WithTimeout(ctx, w.config.PublishTimeout)
		err = w.publisher.Publish(publishCtx, record)
		if err == nil {
			err = publishCtx.Err()
		}
		cancel()
		// Cancellation leaves the lease for recovery, including ambiguous publish outcomes.
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			w.observer.Attempt(ctx, OutcomePublishError)
			saved, retryErr := w.store.Retry(ctx, record, w.backoff(record.Attempts))
			if ctx.Err() != nil {
				return
			}
			if retryErr != nil {
				w.observer.Attempt(ctx, OutcomeStoreError)
			} else if !saved {
				w.observer.Attempt(ctx, OutcomeLeaseLost)
			}
			continue
		}
		saved, markErr := w.store.MarkPublished(ctx, record)
		if ctx.Err() != nil {
			return
		}
		if markErr != nil {
			w.observer.Attempt(ctx, OutcomeStoreError)
		} else if !saved {
			w.observer.Attempt(ctx, OutcomeLeaseLost)
		} else {
			w.observer.Attempt(ctx, OutcomePublished)
		}
	}
}
func (w *Worker) backoff(attempt int) time.Duration {
	delay := w.config.RetryInitial
	for remaining := attempt - 1; remaining > 0 && delay < w.config.RetryMax; remaining-- {
		if delay > w.config.RetryMax/2 {
			return w.config.RetryMax
		}
		delay *= 2
	}
	return min(delay, w.config.RetryMax)
}
