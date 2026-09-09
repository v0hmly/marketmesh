package app

import (
	"errors"
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"time"

	serviceruntime "github.com/v0hmly/marketmesh/platform/runtime"
	"github.com/v0hmly/marketmesh/platform/workloadid"
)

type registrationConfig struct {
	enabled, publish                                                                    bool
	url, serverName, trustDomain, certificateFile, keyFile, caFile                      string
	connectTimeout, publishTimeout, pollInterval, leaseDuration, retryInitial, retryMax time.Duration
	batchSize                                                                           int
}

func loadRegistrationConfig(env serviceruntime.Env, environment string, queryTimeout time.Duration) (registrationConfig, error) {
	var c registrationConfig
	var err error
	if c.enabled, err = env.Bool("AUTH_REGISTRATION_EVENTS_ENABLED", false); err != nil {
		return c, err
	}
	if c.publish, err = env.Bool("AUTH_REGISTRATION_PUBLISH_ENABLED", false); err != nil {
		return c, err
	}
	if c.publish && !c.enabled {
		return c, errors.New("auth registration: publication requires event capture")
	}
	if !c.publish {
		return c, nil
	}
	for _, item := range []struct {
		name   string
		target *string
	}{
		{"AUTH_NATS_URL", &c.url}, {"AUTH_NATS_SERVER_NAME", &c.serverName}, {"AUTH_EVENTS_TRUST_DOMAIN", &c.trustDomain},
		{"AUTH_NATS_TLS_CERT_FILE", &c.certificateFile}, {"AUTH_NATS_TLS_KEY_FILE", &c.keyFile}, {"AUTH_NATS_TLS_CA_FILE", &c.caFile},
	} {
		if *item.target, err = env.RequiredString(item.name); err != nil {
			return c, err
		}
	}
	u, err := url.Parse(c.url)
	if err != nil || u.Scheme != "tls" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return c, errors.New("AUTH_NATS_URL must be a single TLS endpoint without credentials or parameters")
	}
	host, port, err := net.SplitHostPort(u.Host)
	if err != nil || host == "" {
		return c, errors.New("AUTH_NATS_URL must contain host and port")
	}
	p, err := strconv.Atoi(port)
	if err != nil || p < 1 || p > 65535 {
		return c, errors.New("AUTH_NATS_URL has an invalid port")
	}
	for _, p := range []string{c.certificateFile, c.keyFile, c.caFile} {
		if !filepath.IsAbs(p) {
			return c, errors.New("auth registration: TLS file paths must be absolute")
		}
	}
	if err := (workloadid.Identity{TrustDomain: c.trustDomain, Environment: environment, Role: "auth"}).Validate(); err != nil {
		return c, errors.New("auth registration: invalid workload identity")
	}
	for _, item := range []struct {
		name     string
		target   *time.Duration
		fallback time.Duration
	}{
		{"AUTH_EVENTS_CONNECT_TIMEOUT", &c.connectTimeout, 2 * time.Second}, {"AUTH_EVENTS_PUBLISH_TIMEOUT", &c.publishTimeout, 5 * time.Second},
		{"AUTH_EVENTS_POLL_INTERVAL", &c.pollInterval, time.Second}, {"AUTH_EVENTS_LEASE_DURATION", &c.leaseDuration, 30 * time.Second},
		{"AUTH_EVENTS_RETRY_INITIAL", &c.retryInitial, time.Second}, {"AUTH_EVENTS_RETRY_MAX", &c.retryMax, time.Minute},
	} {
		if *item.target, err = env.PositiveDuration(item.name, item.fallback); err != nil {
			return c, err
		}
	}
	if c.batchSize, err = env.PositiveInt("AUTH_EVENTS_BATCH_SIZE", 32); err != nil {
		return c, err
	}
	if c.batchSize > 256 || c.connectTimeout > c.publishTimeout || c.publishTimeout > 30*time.Second || c.pollInterval < 10*time.Millisecond || c.pollInterval > time.Minute || c.leaseDuration > 5*time.Minute || c.retryInitial < time.Millisecond || c.retryInitial > c.retryMax || c.retryMax > 24*time.Hour {
		return c, errors.New("auth registration: invalid bounded worker configuration or lease budget")
	}
	// Subtraction after the small duration bounds avoids overflowing a user-supplied DB timeout.
	if queryTimeout <= 0 || c.leaseDuration <= c.publishTimeout+time.Second || queryTimeout >= (c.leaseDuration-c.publishTimeout-time.Second)/2 {
		return c, errors.New("auth registration: insufficient lease budget for publication and database queries")
	}
	return c, nil
}
