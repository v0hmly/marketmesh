// Package audit records bounded, non-PII security outcomes.
package audit

import (
	"context"
	"errors"

	"github.com/v0hmly/marketmesh/platform/logger"
	"github.com/v0hmly/marketmesh/services/auth/internal/application/login"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// Recorder writes security events and a low-cardinality counter.
type Recorder struct {
	log      *logger.Logger
	attempts metric.Int64Counter
}

// New constructs an audit recorder.
func New(log *logger.Logger, meter metric.Meter) (*Recorder, error) {
	if log == nil {
		return nil, errors.New("audit: logger must not be nil")
	}
	attempts, err := meter.Int64Counter(
		"marketmesh.auth.login.attempts",
		metric.WithDescription("Number of completed credential verification attempts."),
		metric.WithUnit("{attempt}"),
	)
	if err != nil {
		return nil, errors.New("audit: creating login counter")
	}

	return &Recorder{log: log, attempts: attempts}, nil
}

// LoginSucceeded records a successful verification without identity data.
func (recorder *Recorder) LoginSucceeded(ctx context.Context) {
	recorder.log.InfoContext(ctx, "проверка учётных данных завершена", logger.String("outcome", "success"))
	recorder.attempts.Add(ctx, 1, metric.WithAttributes(attribute.String("outcome", "success")))
}

// LoginFailed records a rejected verification using only finite reason values.
func (recorder *Recorder) LoginFailed(ctx context.Context, reason login.FailureReason) {
	recorder.log.InfoContext(
		ctx,
		"проверка учётных данных завершена",
		logger.String("outcome", "failure"),
		logger.String("reason", string(reason)),
	)
	recorder.attempts.Add(
		ctx,
		1,
		metric.WithAttributes(
			attribute.String("outcome", "failure"),
			attribute.String("reason", string(reason)),
		),
	)
}

var _ login.Audit = (*Recorder)(nil)
