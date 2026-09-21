package app

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"time"

	serviceruntime "github.com/v0hmly/marketmesh/platform/runtime"
	"github.com/v0hmly/marketmesh/platform/workloadid"
	sessions "github.com/v0hmly/marketmesh/services/auth/internal/application/session"
)

type sessionConfig struct {
	files                                                       filesSessionConfig
	enabled                                                     bool
	lifetimes                                                   sessions.Config
	issuer, keysFile, trustDomain, internalAddress              string
	allowedOrigins                                              []string
	audiences                                                   map[string][]string
	certificateFile, privateKeyFile, clientCAFile               string
	redisAddress, redisUsername, redisPassword, redisServerName serviceruntime.Secret
	redisCAFile, redisPlaintextReason                           string
}

func loadSessionConfig(env serviceruntime.Env, environment string) (sessionConfig, error) {
	var result sessionConfig
	var err error
	if result.enabled, err = env.Bool("AUTH_SESSIONS_ENABLED", false); err != nil || !result.enabled {
		return result, err
	}
	for _, item := range []struct {
		name  string
		value *string
	}{
		{"AUTH_SESSION_ISSUER", &result.issuer}, {"AUTH_SESSION_KEYS_FILE", &result.keysFile},
		{"AUTH_TRUST_DOMAIN", &result.trustDomain}, {"AUTH_INTERNAL_TLS_CERT_FILE", &result.certificateFile},
		{"AUTH_INTERNAL_TLS_KEY_FILE", &result.privateKeyFile}, {"AUTH_INTERNAL_CLIENT_CA_FILE", &result.clientCAFile},
	} {
		if *item.value, err = env.RequiredString(item.name); err != nil {
			return sessionConfig{}, err
		}
	}
	for _, path := range []string{result.keysFile, result.certificateFile, result.privateKeyFile, result.clientCAFile} {
		if !filepath.IsAbs(path) {
			return sessionConfig{}, errors.New("auth sessions: secret file paths must be absolute")
		}
	}
	if result.internalAddress, err = env.String("AUTH_INTERNAL_ADDRESS", "127.0.0.1:9091"); err != nil {
		return sessionConfig{}, err
	}
	var origins, audiences string
	if origins, err = env.RequiredString("AUTH_ALLOWED_ORIGINS"); err != nil {
		return sessionConfig{}, err
	}
	result.allowedOrigins = strings.Split(origins, ",")
	for i := range result.allowedOrigins {
		result.allowedOrigins[i] = strings.TrimSpace(result.allowedOrigins[i])
	}
	if audiences, err = env.RequiredString("AUTH_SESSION_AUDIENCES"); err != nil {
		return sessionConfig{}, err
	}
	if len(audiences) > 8192 || json.Unmarshal([]byte(audiences), &result.audiences) != nil || len(result.audiences) == 0 {
		return sessionConfig{}, errors.New("auth sessions: AUTH_SESSION_AUDIENCES must be a nonempty JSON audience-to-scopes map")
	}
	for role := range result.audiences {
		id := workloadid.Identity{TrustDomain: result.trustDomain, Environment: environment, Role: role}
		if id.Validate() != nil || role == "gateway-out" || role == "auth" {
			return sessionConfig{}, errors.New("auth sessions: invalid domain-service audience")
		}
	}
	for _, item := range []struct {
		name     string
		target   *time.Duration
		fallback time.Duration
	}{
		{"AUTH_ACCESS_TTL", &result.lifetimes.AccessTTL, 10 * time.Minute},
		{"AUTH_REFRESH_IDLE_TTL", &result.lifetimes.IdleTTL, 7 * 24 * time.Hour},
		{"AUTH_SESSION_ABSOLUTE_TTL", &result.lifetimes.AbsoluteTTL, 30 * 24 * time.Hour},
		{"AUTH_ASSERTION_TTL", &result.lifetimes.AssertionTTL, 30 * time.Second},
	} {
		if *item.target, err = env.PositiveDuration(item.name, item.fallback); err != nil {
			return sessionConfig{}, err
		}
		if *item.target%time.Second != 0 {
			return sessionConfig{}, errors.New("auth sessions: lifetimes must use whole seconds")
		}
	}
	if result.lifetimes.AssertionTTL > 5*time.Minute || result.lifetimes.AssertionTTL > result.lifetimes.AccessTTL || result.lifetimes.AccessTTL > result.lifetimes.IdleTTL || result.lifetimes.IdleTTL > result.lifetimes.AbsoluteTTL || result.lifetimes.AbsoluteTTL > 90*24*time.Hour {
		return sessionConfig{}, errors.New("auth sessions: inconsistent or excessive lifetimes")
	}
	if result.redisAddress, err = env.Secret("AUTH_REDIS_ADDRESS", true); err != nil {
		return sessionConfig{}, err
	}
	if result.redisPassword, err = env.Secret("AUTH_REDIS_PASSWORD", true); err != nil {
		return sessionConfig{}, err
	}
	if result.redisUsername, err = env.Secret("AUTH_REDIS_USERNAME", false); err != nil {
		return sessionConfig{}, err
	}
	if result.redisServerName, err = env.Secret("AUTH_REDIS_TLS_SERVER_NAME", false); err != nil {
		return sessionConfig{}, err
	}
	if result.redisCAFile, err = env.String("AUTH_REDIS_CA_FILE", ""); err != nil {
		return sessionConfig{}, err
	}
	if result.redisPlaintextReason, err = env.String("AUTH_REDIS_PLAINTEXT_REASON", ""); err != nil {
		return sessionConfig{}, err
	}
	if result.redisPlaintextReason != "" {
		if strings.EqualFold(environment, "production") || strings.EqualFold(environment, "prod") || result.redisServerName.Present() || result.redisCAFile != "" {
			return sessionConfig{}, errors.New("auth sessions: plaintext Redis is forbidden in production and cannot be combined with TLS")
		}
	} else if !result.redisServerName.Present() {
		return sessionConfig{}, errors.New("auth sessions: verified Redis TLS is required")
	}
	result.files, err = loadFilesSessionConfig(env, environment, result.trustDomain)
	if err != nil {
		return sessionConfig{}, err
	}
	return result, nil
}
