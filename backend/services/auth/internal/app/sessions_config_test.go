package app

import (
	"context"
	"crypto/tls"
	"net"
	"strings"
	"testing"
	"time"

	platformredis "github.com/v0hmly/marketmesh/platform/redis"
	serviceruntime "github.com/v0hmly/marketmesh/platform/runtime"
)

func sessionEnvironment() map[string]string {
	return map[string]string{
		"AUTH_SESSIONS_ENABLED":        "true",
		"AUTH_SESSION_ISSUER":          "marketmesh-auth",
		"AUTH_SESSION_KEYS_FILE":       "/run/secrets/auth-signing.json",
		"AUTH_TRUST_DOMAIN":            "marketmesh.test",
		"AUTH_INTERNAL_TLS_CERT_FILE":  "/run/secrets/auth.crt",
		"AUTH_INTERNAL_TLS_KEY_FILE":   "/run/secrets/auth.key",
		"AUTH_INTERNAL_CLIENT_CA_FILE": "/run/secrets/ca.crt",
		"AUTH_ALLOWED_ORIGINS":         "https://marketmesh.test, https://app.marketmesh.test",
		"AUTH_SESSION_AUDIENCES":       `{"orders":["orders:read"]}`,
		"AUTH_REDIS_ADDRESS":           "redis-auth:6379",
		"AUTH_REDIS_PASSWORD":          "never-print-this-password",
		"AUTH_REDIS_TLS_SERVER_NAME":   "redis-auth.marketmesh.test",
	}
}

func TestDisabledSessionsNeedNoSecretsOrAdditionalListener(t *testing.T) {
	t.Parallel()
	cfg, err := loadSessionConfig(serviceruntime.MapEnv(map[string]string{"AUTH_REDIS_PASSWORD": "unused"}), "production")
	if err != nil || cfg.enabled {
		t.Fatalf("disabled configuration = %v, %v", cfg.enabled, err)
	}
	resources, err := newSessionResources(context.Background(), config{sessions: cfg}, nil, nil, nil, func(string, string) (net.Listener, error) {
		t.Fatal("disabled sessions opened listener")
		return nil, nil
	})
	if err != nil || resources != nil {
		t.Fatalf("disabled resources = %v, %v", resources, err)
	}
	if len(resources.dependencies()) != 0 || len(resources.connectOptions(config{})) != 0 || resources.close(context.Background()) != nil {
		t.Fatal("disabled sessions have lifecycle side effects")
	}
}

func TestSessionConfigurationBoundsAndAuthRedisIsolation(t *testing.T) {
	t.Parallel()
	cfg, err := loadSessionConfig(serviceruntime.MapEnv(sessionEnvironment()), "production")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.lifetimes.AccessTTL != 10*time.Minute || cfg.lifetimes.IdleTTL != 7*24*time.Hour || cfg.lifetimes.AbsoluteTTL != 30*24*time.Hour || cfg.lifetimes.AssertionTTL != 30*time.Second {
		t.Fatal("incorrect bounded TTL defaults")
	}
	if cfg.internalAddress != "127.0.0.1:9091" || len(cfg.allowedOrigins) != 2 || cfg.allowedOrigins[1] != "https://app.marketmesh.test" {
		t.Fatal("incorrect listener/origin defaults")
	}
	redis, err := sessionRedisConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if redis.Role != platformredis.RoleAuth || redis.Transport.TLS == nil || redis.Transport.TLS.MinVersion != tls.VersionTLS13 || redis.Transport.PlaintextException != nil || !redis.Authentication.Password.Present() {
		t.Fatal("Auth Redis isolation or TLS missing")
	}
	if redis.Timeouts.Connect != time.Second {
		t.Fatal("Redis default connect timeout changed")
	}
	values := sessionEnvironment()
	values["AUTH_REDIS_CONNECT_TIMEOUT"] = "5s"
	cfg, err = loadSessionConfig(serviceruntime.MapEnv(values), "production")
	if err != nil {
		t.Fatal(err)
	}
	redis, err = sessionRedisConfig(cfg)
	if err != nil || redis.Timeouts.Connect != 5*time.Second {
		t.Fatal("configured Redis connect timeout not propagated")
	}
}

func TestSessionConfigurationRejectsUnsafeRollout(t *testing.T) {
	t.Parallel()
	cases := []struct{ name, key, value, environment string }{
		{"bad flag", "AUTH_SESSIONS_ENABLED", "sometimes", "dev"},
		{"missing keys", "AUTH_SESSION_KEYS_FILE", "", "dev"},
		{"relative keys", "AUTH_SESSION_KEYS_FILE", "keys.json", "dev"},
		{"missing origins", "AUTH_ALLOWED_ORIGINS", "", "dev"},
		{"empty audiences", "AUTH_SESSION_AUDIENCES", "{}", "dev"},
		{"null audiences", "AUTH_SESSION_AUDIENCES", "null", "dev"},
		{"gateway audience", "AUTH_SESSION_AUDIENCES", `{"gateway-out":[]}`, "dev"},
		{"auth audience", "AUTH_SESSION_AUDIENCES", `{"auth":[]}`, "dev"},
		{"bad role", "AUTH_SESSION_AUDIENCES", `{"bad/role":[]}`, "dev"},
		{"bad trust domain", "AUTH_TRUST_DOMAIN", "bad/domain", "dev"},
		{"missing Redis password", "AUTH_REDIS_PASSWORD", "", "dev"},
		{"unverified Redis", "AUTH_REDIS_TLS_SERVER_NAME", "", "dev"},
		{"zero Redis connect timeout", "AUTH_REDIS_CONNECT_TIMEOUT", "0s", "dev"},
		{"excessive Redis connect timeout", "AUTH_REDIS_CONNECT_TIMEOUT", "11s", "dev"},
		{"TLS and plaintext", "AUTH_REDIS_PLAINTEXT_REASON", "test exception", "dev"},
		{"zero access", "AUTH_ACCESS_TTL", "0s", "dev"},
		{"subsecond assertion", "AUTH_ASSERTION_TTL", "500ms", "dev"},
		{"long assertion", "AUTH_ASSERTION_TTL", "6m", "dev"},
		{"assertion longer than access", "AUTH_ACCESS_TTL", "10s", "dev"},
		{"access longer than idle", "AUTH_ACCESS_TTL", "8d", "dev"},
		{"idle longer than absolute", "AUTH_REFRESH_IDLE_TTL", "800h", "dev"},
		{"excessive absolute", "AUTH_SESSION_ABSOLUTE_TTL", "3000h", "dev"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			values := sessionEnvironment()
			values[test.key] = test.value
			_, err := loadSessionConfig(serviceruntime.MapEnv(values), test.environment)
			if err == nil {
				t.Fatal("unsafe configuration accepted")
			}
			if strings.Contains(err.Error(), values["AUTH_REDIS_PASSWORD"]) && values["AUTH_REDIS_PASSWORD"] != "" {
				t.Fatal("configuration error exposed secret")
			}
		})
	}
	for _, environment := range []string{"prod", "production", "PRODUCTION"} {
		t.Run(environment, func(t *testing.T) {
			values := sessionEnvironment()
			delete(values, "AUTH_REDIS_TLS_SERVER_NAME")
			values["AUTH_REDIS_PLAINTEXT_REASON"] = "local isolated test"
			if _, err := loadSessionConfig(serviceruntime.MapEnv(values), environment); err == nil {
				t.Fatal("production accepted plaintext")
			}
		})
	}
	values := sessionEnvironment()
	delete(values, "AUTH_REDIS_TLS_SERVER_NAME")
	values["AUTH_REDIS_PLAINTEXT_REASON"] = "local isolated test"
	cfg, err := loadSessionConfig(serviceruntime.MapEnv(values), "test")
	if err != nil {
		t.Fatal(err)
	}
	redis, err := sessionRedisConfig(cfg)
	if err != nil || redis.Transport.TLS != nil || redis.Transport.PlaintextException == nil {
		t.Fatal("explicit nonproduction plaintext exception rejected")
	}
}
