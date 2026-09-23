package app

import (
	"context"
	"errors"

	platformpostgres "github.com/v0hmly/marketmesh/platform/postgres"
	serviceruntime "github.com/v0hmly/marketmesh/platform/runtime"
	"github.com/v0hmly/marketmesh/platform/telemetry"
	"github.com/v0hmly/marketmesh/services/auth/internal/adapter/out/jetstream"
	"github.com/v0hmly/marketmesh/services/auth/internal/adapter/out/postgresoutbox"
	"github.com/v0hmly/marketmesh/services/auth/internal/adapter/out/registrationmetrics"
	"github.com/v0hmly/marketmesh/services/auth/internal/adapter/out/registrationwire"
	"github.com/v0hmly/marketmesh/services/auth/internal/application/publishregistration"
)

type registrationResources struct {
	rw         platformpostgres.Executor
	publisher  *jetstream.Publisher
	components []serviceruntime.Component
}

func newRegistrationResources(c config, database *platformpostgres.Database, pipeline *telemetry.Telemetry) (*registrationResources, error) {
	if !c.registration.enabled {
		return nil, nil
	}
	r := &registrationResources{rw: database.RW()}
	p := c.registration
	if !p.publish {
		return r, nil
	}
	tlsConfig, err := registrationTLS(p, c.environment)
	if err != nil {
		return nil, err
	}
	store, err := postgresoutbox.New(r.rw)
	if err != nil {
		return nil, err
	}
	observer, err := registrationmetrics.New(pipeline.Meter("github.com/v0hmly/marketmesh/services/auth/registration"))
	if err != nil {
		return nil, errors.New("auth registration: metrics unavailable")
	}
	r.publisher, err = jetstream.New(jetstream.Config{URL: p.url, TLS: tlsConfig, ConnectTimeout: p.connectTimeout, PublishTimeout: p.publishTimeout, ValidatePayload: registrationwire.ValidatePayload})
	if err != nil {
		return nil, err
	}
	worker, err := publishregistration.New(store, r.publisher, observer, publishregistration.Config{LeaseDuration: p.leaseDuration, PublishTimeout: p.publishTimeout, PollInterval: p.pollInterval, RetryInitial: p.retryInitial, RetryMax: p.retryMax, BatchSize: p.batchSize})
	if err != nil {
		_ = r.close()
		return nil, err
	}
	r.components = []serviceruntime.Component{{Name: "auth-registration-publisher", Run: worker.Run, Shutdown: func(context.Context) error { return r.close() }}}
	return r, nil
}

func (r *registrationResources) close() error {
	if r == nil || r.publisher == nil {
		return nil
	}
	return r.publisher.Close()
}

func (r *registrationResources) dependencies() []serviceruntime.CriticalDependency {
	if r == nil {
		return nil
	}
	return []serviceruntime.CriticalDependency{{Name: "auth-registration-schema", Check: func(ctx context.Context) error {
		rows, err := r.rw.Query(ctx, "SELECT event_id, subject_id, occurred_at, payload, attempts, next_attempt_at, lease_token, lease_until, published_at FROM auth.registration_outbox LIMIT 0")
		if err != nil {
			return errors.New("auth registration: schema unavailable")
		}
		rows.Close()
		if rows.Err() != nil {
			return errors.New("auth registration: schema unavailable")
		}
		var permitted bool
		if err := r.rw.QueryRow(ctx, `SELECT has_table_privilege(current_user,'auth.registration_outbox','SELECT') AND has_table_privilege(current_user,'auth.registration_outbox','INSERT') AND has_table_privilege(current_user,'auth.registration_outbox','UPDATE')`).Scan(&permitted); err != nil || !permitted {
			return errors.New("auth registration: outbox permissions unavailable")
		}
		return nil
	}}}
}
