// Package registrationmetrics exposes bounded delivery outcomes without identity data.
package registrationmetrics

import (
	"context"
	"errors"
	"time"

	"github.com/v0hmly/marketmesh/services/auth/internal/application/publishregistration"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

type Observer struct {
	attempts metric.Int64Counter
	pending  metric.Int64Gauge
	age      metric.Float64Gauge
}

func New(meter metric.Meter) (*Observer, error) {
	if meter == nil {
		return nil, errors.New("registration metrics: meter required")
	}
	count, err := meter.Int64Counter("marketmesh.auth.registration.delivery", metric.WithDescription("Registration delivery outcomes."))
	if err != nil {
		return nil, err
	}
	pending, err := meter.Int64Gauge("marketmesh.auth.registration.pending", metric.WithDescription("Unpublished registration events."))
	if err != nil {
		return nil, err
	}
	age, err := meter.Float64Gauge("marketmesh.auth.registration.oldest_pending_age", metric.WithUnit("s"), metric.WithDescription("Age of oldest unpublished registration event."))
	if err != nil {
		return nil, err
	}
	return &Observer{count, pending, age}, nil
}

func (o *Observer) ObserveBacklog(ctx context.Context, count int64, age time.Duration) {
	o.pending.Record(ctx, max(count, 0))
	o.age.Record(ctx, max(age.Seconds(), 0))
}
func (o *Observer) Attempt(ctx context.Context, outcome publishregistration.Outcome) {
	switch outcome {
	case publishregistration.OutcomePublished, publishregistration.OutcomePublishError, publishregistration.OutcomeStoreError, publishregistration.OutcomeLeaseLost:
	default:
		outcome = publishregistration.OutcomeStoreError
	}
	o.attempts.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", string(outcome))))
}

var _ publishregistration.Observer = (*Observer)(nil)
