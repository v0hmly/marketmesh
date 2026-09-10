package app

import (
	"errors"
	"fmt"
	"path/filepath"
	"time"

	serviceruntime "github.com/v0hmly/marketmesh/platform/runtime"
)

type registrationConfig struct {
	enabled                                                    bool
	url, serverName, certificateFile, keyFile, caFile          string
	connectTimeout, operationTimeout, fetchTimeout, retryDelay time.Duration
}

func loadRegistrationConfig(env serviceruntime.Env, profile profileConfig) (registrationConfig, error) {
	var c registrationConfig
	var err error
	if c.enabled, err = env.Bool("USER_REGISTRATION_CONSUME_ENABLED", false); err != nil || !c.enabled {
		return c, err
	}
	if !profile.enabled {
		return c, errors.New("user registration: consumer requires USER_PROFILE_ENABLED")
	}
	for _, item := range []struct {
		name   string
		target *string
	}{
		{"USER_NATS_URL", &c.url}, {"USER_NATS_SERVER_NAME", &c.serverName},
		{"USER_NATS_TLS_CERT_FILE", &c.certificateFile}, {"USER_NATS_TLS_KEY_FILE", &c.keyFile}, {"USER_NATS_TLS_CA_FILE", &c.caFile},
	} {
		if *item.target, err = env.RequiredString(item.name); err != nil {
			return c, err
		}
	}
	for _, path := range []string{c.certificateFile, c.keyFile, c.caFile} {
		if !filepath.IsAbs(path) {
			return c, errors.New("user registration: TLS files must use absolute paths")
		}
	}
	for _, item := range []struct {
		name               string
		target             *time.Duration
		fallback, min, max time.Duration
	}{
		{"USER_EVENTS_CONNECT_TIMEOUT", &c.connectTimeout, 2 * time.Second, time.Millisecond, 30 * time.Second},
		{"USER_EVENTS_OPERATION_TIMEOUT", &c.operationTimeout, 5 * time.Second, time.Millisecond, 30 * time.Second},
		{"USER_EVENTS_FETCH_TIMEOUT", &c.fetchTimeout, time.Second, 10 * time.Millisecond, 5 * time.Second},
		{"USER_EVENTS_RETRY_DELAY", &c.retryDelay, time.Second, time.Second, time.Minute},
	} {
		if *item.target, err = env.PositiveDuration(item.name, item.fallback); err != nil {
			return c, err
		}
		if *item.target < item.min || *item.target > item.max {
			return c, fmt.Errorf("user registration: %s outside bounds", item.name)
		}
	}
	if c.connectTimeout > c.operationTimeout || profile.queryTimeout <= 0 || profile.queryTimeout > c.operationTimeout {
		return c, errors.New("user registration: operation timeout must cover connection and database query timeouts")
	}
	return c, nil
}
