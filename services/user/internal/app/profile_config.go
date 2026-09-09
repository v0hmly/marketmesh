package app

import (
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"time"

	"github.com/jackc/pgx/v5"
	serviceruntime "github.com/v0hmly/marketmesh/platform/runtime"
	"github.com/v0hmly/marketmesh/platform/workloadid"
)

type profileConfig struct {
	enabled                                        bool
	address, trustDomain                           string
	certificateFile, keyFile, clientCAFile         string
	authTarget, authServerName, authCAFile, issuer string
	authTimeout, assertionTTL, requestTimeout      time.Duration
	rwDSN, roDSN                                   serviceruntime.Secret
	maxConns                                       int32
	connectTimeout, queryTimeout                   time.Duration
}

func loadProfileConfig(env serviceruntime.Env, environment string) (profileConfig, error) {
	var c profileConfig
	var err error
	c.enabled, err = env.Bool("USER_PROFILE_ENABLED", false)
	if err != nil || !c.enabled {
		return c, err
	}
	for _, item := range []struct {
		name   string
		target *string
	}{
		{"USER_TRUST_DOMAIN", &c.trustDomain}, {"USER_TLS_CERT_FILE", &c.certificateFile},
		{"USER_TLS_KEY_FILE", &c.keyFile}, {"USER_TLS_CLIENT_CA_FILE", &c.clientCAFile},
		{"USER_AUTH_TARGET", &c.authTarget}, {"USER_AUTH_SERVER_NAME", &c.authServerName},
		{"USER_AUTH_CA_FILE", &c.authCAFile}, {"USER_AUTH_ISSUER", &c.issuer},
	} {
		if *item.target, err = env.RequiredString(item.name); err != nil {
			return profileConfig{}, err
		}
	}
	for _, item := range []struct{ name, path string }{
		{"USER_TLS_CERT_FILE", c.certificateFile}, {"USER_TLS_KEY_FILE", c.keyFile},
		{"USER_TLS_CLIENT_CA_FILE", c.clientCAFile}, {"USER_AUTH_CA_FILE", c.authCAFile},
	} {
		if !filepath.IsAbs(item.path) {
			return profileConfig{}, fmt.Errorf("environment variable %s must be an absolute path", item.name)
		}
	}
	if err := (workloadid.Identity{TrustDomain: c.trustDomain, Environment: environment, Role: "user"}).Validate(); err != nil {
		return profileConfig{}, errors.New("user profile: invalid workload identity configuration")
	}
	if c.address, err = env.String("USER_GRPC_ADDRESS", "127.0.0.1:9092"); err != nil {
		return profileConfig{}, err
	}
	if _, _, err = net.SplitHostPort(c.address); err != nil {
		return profileConfig{}, errors.New("USER_GRPC_ADDRESS must contain host and port")
	}
	if c.rwDSN, err = env.Secret("POSTGRES_RW_DSN", true); err != nil {
		return profileConfig{}, err
	}
	if c.roDSN, err = env.Secret("POSTGRES_RO_DSN", true); err != nil {
		return profileConfig{}, err
	}
	rw, err := pgx.ParseConfig(c.rwDSN.Reveal())
	if err != nil {
		return profileConfig{}, errors.New("user profile: invalid PostgreSQL RW configuration")
	}
	ro, err := pgx.ParseConfig(c.roDSN.Reveal())
	if err != nil {
		return profileConfig{}, errors.New("user profile: invalid PostgreSQL RO configuration")
	}
	if rw.User == ro.User || rw.Database != ro.Database {
		return profileConfig{}, errors.New("user profile: PostgreSQL requires distinct RW/RO roles for the same User database")
	}
	for _, item := range []struct {
		name     string
		target   *time.Duration
		fallback time.Duration
	}{
		{"USER_AUTH_TIMEOUT", &c.authTimeout, 2 * time.Second}, {"USER_ASSERTION_MAX_TTL", &c.assertionTTL, 30 * time.Second},
		{"USER_GRPC_REQUEST_TIMEOUT", &c.requestTimeout, 10 * time.Second}, {"POSTGRES_CONNECT_TIMEOUT", &c.connectTimeout, 5 * time.Second},
		{"POSTGRES_QUERY_TIMEOUT", &c.queryTimeout, 3 * time.Second},
	} {
		if *item.target, err = env.PositiveDuration(item.name, item.fallback); err != nil {
			return profileConfig{}, err
		}
	}
	if c.assertionTTL < time.Second || c.assertionTTL > 5*time.Minute || c.assertionTTL%time.Second != 0 {
		return profileConfig{}, errors.New("USER_ASSERTION_MAX_TTL must be whole seconds between 1s and 5m")
	}
	maxConns, err := env.PositiveInt("POSTGRES_MAX_CONNS", 10)
	if err != nil {
		return profileConfig{}, err
	}
	if maxConns > 100 {
		return profileConfig{}, errors.New("POSTGRES_MAX_CONNS must not exceed 100")
	}
	c.maxConns = int32(maxConns)
	return c, nil
}
