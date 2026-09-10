package app

import (
	"context"
	"errors"

	serviceruntime "github.com/v0hmly/marketmesh/platform/runtime"
	"github.com/v0hmly/marketmesh/platform/telemetry"
	"github.com/v0hmly/marketmesh/services/user/internal/adapter/in/jetstream"
	"github.com/v0hmly/marketmesh/services/user/internal/adapter/out/postgresregistration"
	"github.com/v0hmly/marketmesh/services/user/internal/application/consumeregistration"
)

func (r *profileResources) addRegistration(c config, pipeline *telemetry.Telemetry) error {
	if !c.registration.enabled {
		return nil
	}
	tlsConfig, err := registrationTLS(c.registration, c.profile.trustDomain, c.environment)
	if err != nil {
		return err
	}
	store, err := postgresregistration.New(r.database.RW())
	if err != nil {
		return err
	}
	service, err := consumeregistration.New(store)
	if err != nil {
		return err
	}
	observer, err := newRegistrationObserver(pipeline.Meter("github.com/v0hmly/marketmesh/services/user/registration"))
	if err != nil {
		return err
	}
	p := c.registration
	r.registration, err = jetstream.New(jetstream.Config{
		URL: p.url, TLSConfig: tlsConfig, ConnectTimeout: p.connectTimeout, OperationTimeout: p.operationTimeout,
		FetchTimeout: p.fetchTimeout, RetryDelay: p.retryDelay, Observe: observer.outcome,
		ObserveBacklog: observer.backlog, ObserveAge: observer.age,
	}, service)
	if err != nil {
		return err
	}
	// Stop gRPC before the consumer, and the consumer before its database.
	n := len(r.components) - 1
	r.components = append(r.components[:n], serviceruntime.Component{
		Name: "user-registration", Run: r.registration.Run,
		Shutdown: func(context.Context) error { return r.registration.Close() },
	}, r.components[n])
	return nil
}

func (r *profileResources) registrationDependency() serviceruntime.CriticalDependency {
	return serviceruntime.CriticalDependency{Name: "user-registration-schema", Check: func(ctx context.Context) error {
		rows, err := r.database.RW().Query(ctx, "SELECT event_id, subject_id, payload_hash FROM users.registration_inbox LIMIT 0")
		if err != nil {
			return errors.New("user registration: inbox schema unavailable")
		}
		rows.Close()
		if rows.Err() != nil {
			return errors.New("user registration: inbox schema unavailable")
		}
		var allowed bool
		err = r.database.RW().QueryRow(ctx, `SELECT
 has_table_privilege(current_user,'users.registration_inbox','SELECT')
 AND has_table_privilege(current_user,'users.registration_inbox','INSERT')
 AND has_table_privilege(current_user,'users.registration_inbox','UPDATE')
 AND has_table_privilege(current_user,'users.profiles','SELECT')
 AND has_table_privilege(current_user,'users.profiles','INSERT')`).Scan(&allowed)
		if err != nil || !allowed {
			return errors.New("user registration: database privileges unavailable")
		}
		return nil
	}}
}
