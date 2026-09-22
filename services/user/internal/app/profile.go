package app

import (
	"context"
	"errors"
	"net"
	"time"

	authv1 "github.com/v0hmly/marketmesh/api/gen/go/auth/v1"
	userv1 "github.com/v0hmly/marketmesh/api/gen/go/user/v1"
	platformgrpc "github.com/v0hmly/marketmesh/platform/grpc"
	"github.com/v0hmly/marketmesh/platform/logger"
	platformpostgres "github.com/v0hmly/marketmesh/platform/postgres"
	serviceruntime "github.com/v0hmly/marketmesh/platform/runtime"
	"github.com/v0hmly/marketmesh/platform/telemetry"
	"github.com/v0hmly/marketmesh/platform/workloadid"
	"github.com/v0hmly/marketmesh/services/user/internal/adapter/in/internalgrpc"
	"github.com/v0hmly/marketmesh/services/user/internal/adapter/in/jetstream"
	"github.com/v0hmly/marketmesh/services/user/internal/adapter/out/authsession"
	userpostgres "github.com/v0hmly/marketmesh/services/user/internal/adapter/out/postgres"
	"github.com/v0hmly/marketmesh/services/user/internal/adapter/out/postgresaddresses"
	"github.com/v0hmly/marketmesh/services/user/internal/adapter/out/postgressettings"
	"github.com/v0hmly/marketmesh/services/user/internal/application/addresses"
	"github.com/v0hmly/marketmesh/services/user/internal/application/getme"
	"github.com/v0hmly/marketmesh/services/user/internal/application/settings"
	"github.com/v0hmly/marketmesh/services/user/internal/application/updateme"
	grpcgo "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
)

type profileResources struct {
	database         *platformpostgres.Database
	client           *platformgrpc.Client
	server           *platformgrpc.Server
	listener         net.Listener
	verifier         *authsession.Verifier
	registration     *jetstream.Consumer
	components       []serviceruntime.Component
	addressesEnabled bool
	settingsEnabled  bool
}

func newProfileResources(ctx context.Context, c config, log *logger.Logger, pipeline *telemetry.Telemetry, listen listenFunc) (*profileResources, error) {
	if !c.profile.enabled {
		return nil, nil
	}
	p := c.profile
	serverTLS, clientTLS, err := profileTLS(p, c.environment)
	if err != nil {
		return nil, err
	}
	r := &profileResources{addressesEnabled: p.addressesEnabled, settingsEnabled: p.settingsEnabled}
	owned := true
	defer func() {
		if owned {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.shutdownTimeout)
			defer cancel()
			_ = r.close(cleanup)
		}
	}()
	r.database, err = platformpostgres.New(ctx, profilePostgresConfig(c), pipeline)
	if err != nil {
		return nil, errors.New("user profile: PostgreSQL unavailable")
	}
	r.client, err = platformgrpc.NewClient(ctx, platformgrpc.ClientConfig{
		Target: p.authTarget, Environment: c.environment, ConnectTimeout: p.connectTimeout, CallTimeout: p.authTimeout,
		KeepaliveTime: 2 * time.Minute, KeepaliveTimeout: 20 * time.Second, MaxReceiveMessageBytes: 128 * 1024, MaxSendMessageBytes: 32 * 1024,
		Security: platformgrpc.ClientSecurity{TLSConfig: clientTLS, RequireClientCertificate: true}, Logger: log, Telemetry: pipeline,
	})
	if err != nil {
		return nil, errors.New("user profile: Auth connection unavailable")
	}
	r.verifier, err = authsession.New(authv1.NewAuthInternalServiceClient(r.client.Connection()), authsession.Config{Issuer: p.issuer, MaxTTL: p.assertionTTL, Timeout: p.authTimeout, Clock: time.Now})
	if err != nil {
		return nil, err
	}
	repository, err := userpostgres.New(r.database.RW())
	if err != nil {
		return nil, err
	}
	get, err := getme.New(repository)
	if err != nil {
		return nil, err
	}
	update, err := updateme.New(repository)
	if err != nil {
		return nil, err
	}
	methods := []string{userv1.UserService_GetMe_FullMethodName, userv1.UserService_UpdateMe_FullMethodName}
	if p.addressesEnabled {
		methods = append(methods, addressMethods()...)
	}
	if p.settingsEnabled {
		methods = append(methods, settingsMethods()...)
	}
	policy, err := workloadid.NewPolicy(map[workloadid.Identity][]string{
		{TrustDomain: p.trustDomain, Environment: c.environment, Role: "gateway-out"}: methods,
	})
	if err != nil {
		return nil, err
	}
	handler, err := internalgrpc.New(get, update, r.verifier, policy, log)
	if err != nil {
		return nil, err
	}
	if p.addressesEnabled {
		repo, e := postgresaddresses.New(r.database.RW(), r.database)
		if e != nil {
			return nil, e
		}
		useCase, e := addresses.New(repo)
		if e != nil {
			return nil, e
		}
		if e = handler.EnableAddresses(useCase); e != nil {
			return nil, e
		}
	}
	if p.settingsEnabled {
		repo, e := postgressettings.New(r.database.RW())
		if e != nil {
			return nil, e
		}
		useCase, e := settings.New(repo)
		if e != nil {
			return nil, e
		}
		if e = handler.EnableSettings(useCase); e != nil {
			return nil, e
		}
	}
	maxSend := 32 * 1024
	if p.addressesEnabled {
		maxSend = 128 * 1024
	}
	publicErrors := []platformgrpc.PublicErrorInfo{
		{Method: userv1.UserService_GetMe_FullMethodName, Code: codes.NotFound, Domain: "marketmesh.user", Reason: "PROFILE_NOT_READY"},
		{Method: userv1.UserService_UpdateMe_FullMethodName, Code: codes.NotFound, Domain: "marketmesh.user", Reason: "PROFILE_NOT_READY"},
	}
	if p.addressesEnabled {
		for _, method := range addressMethods() {
			for _, detail := range []struct {
				code   codes.Code
				reason string
			}{{codes.NotFound, "PROFILE_NOT_READY"}, {codes.NotFound, "ADDRESS_NOT_FOUND"}, {codes.ResourceExhausted, "ADDRESS_LIMIT_REACHED"}} {
				publicErrors = append(publicErrors, platformgrpc.PublicErrorInfo{Method: method, Code: detail.code, Domain: "marketmesh.user", Reason: detail.reason})
			}
		}
	}
	if p.settingsEnabled {
		for _, method := range settingsMethods() {
			publicErrors = append(publicErrors, platformgrpc.PublicErrorInfo{Method: method, Code: codes.NotFound, Domain: "marketmesh.user", Reason: "PROFILE_NOT_READY"})
		}
	}
	workloadAuth := workloadid.UnaryServerInterceptor(policy)
	r.server, err = platformgrpc.NewServer(platformgrpc.ServerConfig{
		Environment: c.environment, ConnectionTimeout: p.connectTimeout, RequestTimeout: p.requestTimeout,
		KeepaliveTime: 2 * time.Minute, KeepaliveTimeout: 20 * time.Second, MaxReceiveMessageBytes: 32 * 1024, MaxSendMessageBytes: maxSend,
		Security: platformgrpc.ServerSecurity{TLSConfig: serverTLS, RequireClientCertificate: true}, Logger: log, Telemetry: pipeline,
		PublicErrorInfo: publicErrors,
		UnaryAuthentication: func(ctx context.Context, req any, info *grpcgo.UnaryServerInfo, next grpcgo.UnaryHandler) (any, error) {
			_ = grpcgo.SetHeader(ctx, metadata.Pairs("cache-control", "no-store"))
			return workloadAuth(ctx, req, info, next)
		},
		StreamAuthentication: workloadid.StreamServerInterceptor(policy),
	})
	if err != nil {
		return nil, err
	}
	userv1.RegisterUserServiceServer(r.server.GRPCServer(), handler)
	r.listener, err = listen("tcp", p.address)
	if err != nil {
		return nil, errors.New("user profile: gRPC listener unavailable")
	}
	dbComponent, err := r.database.Component("user-postgres")
	if err != nil {
		return nil, err
	}
	grpcComponent, err := r.server.Component("user-grpc", r.listener)
	if err != nil {
		return nil, err
	}
	r.components = []serviceruntime.Component{dbComponent, {
		Name: "user-auth-client", Run: func(ctx context.Context) error { <-ctx.Done(); return ctx.Err() },
		Shutdown: func(context.Context) error { return r.client.Close() },
	}, grpcComponent}
	if err := r.addRegistration(c, pipeline); err != nil {
		return nil, err
	}
	owned = false
	return r, nil
}

func (r *profileResources) dependencies() []serviceruntime.CriticalDependency {
	if r == nil {
		return nil
	}
	dependencies := r.database.ReadinessDependencies()
	if r.registration != nil {
		dependencies = append(dependencies, r.registrationDependency())
	}
	return append(dependencies, serviceruntime.CriticalDependency{Name: "user-auth-keys", Check: r.verifier.Ready},
		serviceruntime.CriticalDependency{Name: "user-profile-schema", Check: func(ctx context.Context) error {
			for _, executor := range []platformpostgres.Executor{r.database.RW(), r.database.RO()} {
				if r.settingsEnabled {
					rows, e := executor.Query(ctx, "SELECT subject_id,theme,settings_version FROM users.profiles LIMIT 0")
					if e != nil {
						return errors.New("user settings: schema unavailable")
					}
					rows.Close()
					if rows.Err() != nil {
						return errors.New("user settings: schema check failed")
					}
				}
				if r.addressesEnabled {
					rows, e := executor.Query(ctx, "SELECT p.address_book_version,a.address_id,a.subject_id,a.recipient,a.phone,a.country,a.postal_code,a.city,a.street_house,a.apartment,a.comment,a.is_default FROM users.profiles p LEFT JOIN users.addresses a ON a.subject_id=p.subject_id LIMIT 0")
					if e != nil {
						return errors.New("user address: schema unavailable")
					}
					rows.Close()
					if rows.Err() != nil {
						return errors.New("user address: schema check failed")
					}
				}

				rows, err := executor.Query(ctx, "SELECT subject_id, display_name, bio, version, created_at, updated_at, last_name, birth_date, gender, phone, city, show_age FROM users.profiles LIMIT 0")
				if err != nil {
					return errors.New("user profile: schema unavailable")
				}
				rows.Close()
				if rows.Err() != nil {
					return errors.New("user profile: schema check failed")
				}
			}
			return nil
		}})
}

func (r *profileResources) close(ctx context.Context) error {
	if r == nil {
		return nil
	}
	if r.server != nil {
		r.server.GRPCServer().Stop()
	}
	if r.listener != nil {
		_ = r.listener.Close()
	}
	var err error
	if r.registration != nil {
		err = errors.Join(err, r.registration.Close())
	}
	if r.client != nil {
		err = errors.Join(err, r.client.Close())
	}
	if r.database != nil {
		err = errors.Join(err, r.database.Close(ctx))
	}
	return err
}

func profilePostgresConfig(c config) platformpostgres.Config {
	p := c.profile
	pool := func(dsn serviceruntime.Secret) platformpostgres.PoolConfig {
		return platformpostgres.PoolConfig{
			DSN: dsn, MaxConns: p.maxConns, ConnectTimeout: p.connectTimeout, QueryTimeout: p.queryTimeout,
			MaxConnLifetime: 30 * time.Minute, MaxConnLifetimeJitter: 3 * time.Minute, MaxConnIdleTime: 5 * time.Minute, HealthCheckPeriod: 30 * time.Second, PingTimeout: 2 * time.Second,
		}
	}
	return platformpostgres.Config{ApplicationName: "user/" + c.instanceID, RW: pool(p.rwDSN), RO: pool(p.roDSN)}
}

func addressMethods() []string {
	return []string{userv1.UserService_ListAddresses_FullMethodName, userv1.UserService_CreateAddress_FullMethodName, userv1.UserService_UpdateAddress_FullMethodName, userv1.UserService_DeleteAddress_FullMethodName, userv1.UserService_SetDefaultAddress_FullMethodName}
}

func settingsMethods() []string {
	return []string{userv1.UserService_GetSettings_FullMethodName, userv1.UserService_UpdateSettings_FullMethodName}
}
