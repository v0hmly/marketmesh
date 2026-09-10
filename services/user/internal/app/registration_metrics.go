package app

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/v0hmly/marketmesh/services/user/internal/adapter/in/jetstream"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

type registrationObserver struct {
	deliveries          metric.Int64Counter
	pending, ackPending metric.Int64Gauge
	observedAt          metric.Float64Gauge
	deliveryAge         metric.Float64Histogram
}

func newRegistrationObserver(meter metric.Meter) (*registrationObserver, error) {
	if meter == nil {
		return nil, errors.New("user registration: meter required")
	}
	o := &registrationObserver{}
	var err error
	if o.deliveries, err = meter.Int64Counter("marketmesh.user.registration.delivery"); err != nil {
		return nil, err
	}
	if o.pending, err = meter.Int64Gauge("marketmesh.user.registration.pending"); err != nil {
		return nil, err
	}
	if o.ackPending, err = meter.Int64Gauge("marketmesh.user.registration.ack_pending"); err != nil {
		return nil, err
	}
	if o.observedAt, err = meter.Float64Gauge("marketmesh.user.registration.backlog_observed_at", metric.WithUnit("s")); err != nil {
		return nil, err
	}
	if o.deliveryAge, err = meter.Float64Histogram("marketmesh.user.registration.delivery_age", metric.WithUnit("s")); err != nil {
		return nil, err
	}
	return o, nil
}

func (o *registrationObserver) outcome(outcome jetstream.Outcome) {
	switch outcome {
	case jetstream.Applied, jetstream.Duplicate, jetstream.Invalid, jetstream.Conflict, jetstream.StoreError, jetstream.BrokerError, jetstream.AckError:
	default:
		outcome = jetstream.BrokerError
	}
	o.deliveries.Add(context.Background(), 1, metric.WithAttributes(attribute.String("outcome", string(outcome))))
}
func (o *registrationObserver) backlog(pending uint64, ackPending int) {
	ctx := context.Background()
	o.pending.Record(ctx, int64(min(pending, math.MaxInt64)))
	o.ackPending.Record(ctx, int64(max(ackPending, 0)))
	o.observedAt.Record(ctx, float64(time.Now().UnixNano())/float64(time.Second))
}
func (o *registrationObserver) age(age time.Duration) {
	o.deliveryAge.Record(context.Background(), max(age.Seconds(), 0))
}
