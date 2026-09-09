package app

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"os"
	"time"

	authv1 "github.com/v0hmly/marketmesh/api/gen/go/auth/v1"
	platformgrpc "github.com/v0hmly/marketmesh/platform/grpc"
	"github.com/v0hmly/marketmesh/platform/logger"
	platformpostgres "github.com/v0hmly/marketmesh/platform/postgres"
	platformredis "github.com/v0hmly/marketmesh/platform/redis"
	serviceruntime "github.com/v0hmly/marketmesh/platform/runtime"
	"github.com/v0hmly/marketmesh/platform/telemetry"
	"github.com/v0hmly/marketmesh/platform/workloadid"
	connectadapter "github.com/v0hmly/marketmesh/services/auth/internal/adapter/in/connectrpc"
	"github.com/v0hmly/marketmesh/services/auth/internal/adapter/in/internalgrpc"
	"github.com/v0hmly/marketmesh/services/auth/internal/adapter/out/postgressession"
	"github.com/v0hmly/marketmesh/services/auth/internal/adapter/out/redissession"
	"github.com/v0hmly/marketmesh/services/auth/internal/adapter/out/sessionkeys"
	sessions "github.com/v0hmly/marketmesh/services/auth/internal/application/session"
)

type sessionResources struct {
	service    *sessions.Service
	redis      *platformredis.Client
	keys       *sessionkeys.Manager
	server     *platformgrpc.Server
	listener   net.Listener
	components []serviceruntime.Component
}

func newSessionResources(ctx context.Context, config config, log *logger.Logger, pipeline *telemetry.Telemetry, database *platformpostgres.Database, listen listenFunc) (*sessionResources, error) {
	if !config.sessions.enabled {
		return nil, nil
	}
	cfg := config.sessions
	keys, err := sessionkeys.New(sessionkeys.Config{Path: cfg.keysFile, Issuer: cfg.issuer, MaxTTL: cfg.lifetimes.AssertionTTL, Clock: time.Now, Audiences: cfg.audiences})
	if err != nil {
		return nil, errors.New("auth sessions: signing keys are unavailable")
	}
	security, err := sessionTLS(cfg, config.environment)
	if err != nil {
		return nil, err
	}
	redisConfig, err := sessionRedisConfig(cfg)
	if err != nil {
		return nil, err
	}
	client, err := platformredis.New(ctx, redisConfig, pipeline)
	if err != nil {
		return nil, errors.New("auth sessions: Redis is unavailable")
	}
	resources := &sessionResources{redis: client, keys: keys}
	owned := true
	defer func() {
		if owned {
			cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), config.shutdownTimeout)
			defer cancel()
			_ = resources.close(cleanup)
		}
	}()
	store, err := postgressession.New(database)
	if err != nil {
		return nil, err
	}
	access, err := redissession.New(client)
	if err != nil {
		return nil, err
	}
	resources.service, err = sessions.New(store, access, keys, cfg.lifetimes)
	if err != nil {
		return nil, err
	}
	handler, err := internalgrpc.New(resources.service, keys, internalgrpc.Config{TrustDomain: cfg.trustDomain, Environment: config.environment, Issuer: cfg.issuer, AssertionTTL: cfg.lifetimes.AssertionTTL, Clock: time.Now, Audiences: cfg.audiences})
	if err != nil {
		return nil, err
	}
	resources.server, err = platformgrpc.NewServer(platformgrpc.ServerConfig{
		Environment: config.environment, ConnectionTimeout: 3 * time.Second, RequestTimeout: config.httpRequestTimeout,
		KeepaliveTime: 2 * time.Minute, KeepaliveTimeout: 20 * time.Second, MaxReceiveMessageBytes: 16 * 1024, MaxSendMessageBytes: 64 * 1024,
		Security: platformgrpc.ServerSecurity{TLSConfig: security, RequireClientCertificate: true}, Logger: log, Telemetry: pipeline,
		UnaryAuthentication: workloadid.UnaryServerInterceptor(handler.Policy()), StreamAuthentication: workloadid.StreamServerInterceptor(handler.Policy()),
	})
	if err != nil {
		return nil, err
	}
	authv1.RegisterAuthInternalServiceServer(resources.server.GRPCServer(), handler)
	resources.listener, err = listen("tcp", cfg.internalAddress)
	if err != nil {
		return nil, errors.New("auth sessions: internal listener unavailable")
	}
	redisComponent, err := client.Component("redis-auth")
	if err != nil {
		return nil, err
	}
	grpcComponent, err := resources.server.Component("auth-internal", resources.listener)
	if err != nil {
		return nil, err
	}
	resources.components = []serviceruntime.Component{redisComponent, grpcComponent}
	owned = false
	return resources, nil
}

func (resources *sessionResources) close(ctx context.Context) error {
	if resources == nil {
		return nil
	}
	if resources.server != nil {
		resources.server.GRPCServer().Stop()
	}
	if resources.listener != nil {
		_ = resources.listener.Close()
	}
	if resources.redis != nil {
		return resources.redis.Close(ctx)
	}
	return nil
}
func (resources *sessionResources) connectOptions(config config) []connectadapter.Option {
	if resources == nil {
		return nil
	}
	return []connectadapter.Option{connectadapter.WithSessions(resources.service, connectadapter.SessionConfig{AllowedOrigins: config.sessions.allowedOrigins, Clock: time.Now})}
}
func (resources *sessionResources) dependencies() []serviceruntime.CriticalDependency {
	if resources == nil {
		return nil
	}
	return append(resources.redis.ReadinessDependencies(), serviceruntime.CriticalDependency{Name: "session-signing-keys", Check: func(ctx context.Context) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		_, err := resources.keys.PublicKeys()
		return err
	}})
}

func sessionTLS(config sessionConfig, environment string) (*tls.Config, error) {
	certificate, err := tls.LoadX509KeyPair(config.certificateFile, config.privateKeyFile)
	if err != nil || len(certificate.Certificate) == 0 {
		return nil, errors.New("auth sessions: invalid internal TLS key pair")
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		return nil, errors.New("auth sessions: invalid internal TLS certificate")
	}
	identity, err := workloadid.IdentityFromCertificate(leaf)
	if err != nil || identity.TrustDomain != config.trustDomain || identity.Environment != environment || identity.Role != "auth" {
		return nil, errors.New("auth sessions: internal TLS identity does not match Auth")
	}
	pool, err := loadSessionCA(config.clientCAFile)
	if err != nil {
		return nil, err
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{certificate}, ClientCAs: pool, ClientAuth: tls.RequireAndVerifyClientCert}, nil
}
func loadSessionCA(path string) (*x509.CertPool, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("auth sessions: CA file unavailable")
	}
	defer func() { _ = file.Close() }()
	pem, err := io.ReadAll(io.LimitReader(file, 1024*1024+1))
	if err != nil || len(pem) > 1024*1024 {
		return nil, errors.New("auth sessions: invalid CA file")
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, errors.New("auth sessions: invalid CA certificates")
	}
	return pool, nil
}
func sessionRedisConfig(config sessionConfig) (platformredis.Config, error) {
	transport := platformredis.TransportConfig{}
	if config.redisPlaintextReason != "" {
		transport.PlaintextException = &platformredis.PlaintextException{Reason: config.redisPlaintextReason}
	} else {
		transport.TLS = &platformredis.TLSConfig{ServerName: config.redisServerName, MinVersion: tls.VersionTLS13}
		if config.redisCAFile != "" {
			pool, err := loadSessionCA(config.redisCAFile)
			if err != nil {
				return platformredis.Config{}, err
			}
			transport.TLS.RootCAs = pool
		}
	}
	return platformredis.Config{Role: platformredis.RoleAuth, Address: config.redisAddress, Authentication: platformredis.AuthenticationConfig{Username: config.redisUsername, Password: config.redisPassword}, Transport: transport,
		Pool:     platformredis.PoolConfig{Size: 10, MinIdleConns: 1, MaxIdleConns: 10, MaxActiveConns: 10, MaxConcurrentDials: 2, ConnMaxIdleTime: 5 * time.Minute, ConnMaxLifetime: 30 * time.Minute},
		Timeouts: platformredis.TimeoutConfig{Connect: time.Second, Command: 3 * time.Second, Pool: time.Second, Read: 2 * time.Second, Write: 2 * time.Second, Readiness: 2 * time.Second, Shutdown: 5 * time.Second},
	}, nil
}
