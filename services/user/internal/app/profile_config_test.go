package app

import (
	"strings"
	"testing"
	"time"

	serviceruntime "github.com/v0hmly/marketmesh/platform/runtime"
)

func profileEnvironment() map[string]string {
	v := validEnvironment()
	for k, value := range map[string]string{
		"USER_PROFILE_ENABLED": "true", "USER_TRUST_DOMAIN": "marketmesh.test",
		"USER_TLS_CERT_FILE": "/run/user.crt", "USER_TLS_KEY_FILE": "/run/user.key", "USER_TLS_CLIENT_CA_FILE": "/run/ca.crt",
		"USER_AUTH_TARGET": "auth:9091", "USER_AUTH_SERVER_NAME": "auth", "USER_AUTH_CA_FILE": "/run/ca.crt", "USER_AUTH_ISSUER": "marketmesh-auth",
		"POSTGRES_RW_DSN": "postgres://user_rw:rw-secret@localhost/users?sslmode=disable", "POSTGRES_RO_DSN": "postgres://user_ro:ro-secret@localhost/users?sslmode=disable",
	} {
		v[k] = value
	}
	return v
}

func TestProfileConfigIsExplicitAndSeparatesDatabaseRoles(t *testing.T) {
	c, err := loadConfig(serviceruntime.MapEnv(validEnvironment()))
	if err != nil || c.profile.enabled {
		t.Fatal("legacy health-only configuration changed", err)
	}
	c, err = loadConfig(serviceruntime.MapEnv(profileEnvironment()))
	if err != nil || !c.profile.enabled || c.profile.assertionTTL != 30*time.Second || c.profile.maxConns != 10 {
		t.Fatal("profile configuration", err)
	}
	for _, name := range []string{"USER_TRUST_DOMAIN", "USER_TLS_CERT_FILE", "USER_TLS_KEY_FILE", "USER_TLS_CLIENT_CA_FILE", "USER_AUTH_TARGET", "USER_AUTH_SERVER_NAME", "USER_AUTH_CA_FILE", "USER_AUTH_ISSUER", "POSTGRES_RW_DSN", "POSTGRES_RO_DSN"} {
		t.Run(name, func(t *testing.T) {
			v := profileEnvironment()
			delete(v, name)
			if _, err := loadConfig(serviceruntime.MapEnv(v)); err == nil {
				t.Fatal("missing required configuration accepted")
			}
		})
	}
}

func TestProfileConfigRejectsUnsafeValuesWithoutSecrets(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"USER_PROFILE_ENABLED", "sometimes"}, {"USER_TRUST_DOMAIN", "bad domain"}, {"USER_TLS_KEY_FILE", "relative.key"}, {"USER_GRPC_ADDRESS", "invalid"},
		{"USER_ASSERTION_MAX_TTL", "1.5s"}, {"USER_ASSERTION_MAX_TTL", "10m"}, {"USER_AUTH_TIMEOUT", "0s"},
		{"POSTGRES_MAX_CONNS", "101"}, {"POSTGRES_MAX_CONNS", "0"}, {"POSTGRES_QUERY_TIMEOUT", "-1s"},
		{"POSTGRES_RO_DSN", "postgres://user_rw:ro-secret@localhost/users?sslmode=disable"},
		{"POSTGRES_RO_DSN", "postgres://user_ro:ro-secret@localhost/auth?sslmode=disable"},
		{"POSTGRES_RW_DSN", "invalid-rw-secret"},
	} {
		t.Run(tc.key+"/"+tc.value, func(t *testing.T) {
			v := profileEnvironment()
			v[tc.key] = tc.value
			_, err := loadConfig(serviceruntime.MapEnv(v))
			if err == nil {
				t.Fatal("invalid profile configuration accepted")
			}
			if strings.Contains(err.Error(), "rw-secret") || strings.Contains(err.Error(), "ro-secret") {
				t.Fatal("secret leaked")
			}
		})
	}
}

func TestAddressesRequireProfileAndExplicitFlag(t *testing.T) {
	v := validEnvironment()
	v["USER_ADDRESSES_ENABLED"] = "true"
	if _, e := loadConfig(serviceruntime.MapEnv(v)); e == nil {
		t.Fatal("addresses enabled without profile")
	}
	v = profileEnvironment()
	c, e := loadConfig(serviceruntime.MapEnv(v))
	if e != nil || c.profile.addressesEnabled {
		t.Fatal(e)
	}
	v["USER_ADDRESSES_ENABLED"] = "true"
	c, e = loadConfig(serviceruntime.MapEnv(v))
	if e != nil || !c.profile.addressesEnabled {
		t.Fatal(e)
	}
	v["USER_ADDRESSES_ENABLED"] = "sometimes"
	if _, e = loadConfig(serviceruntime.MapEnv(v)); e == nil {
		t.Fatal("invalid flag accepted")
	}
}

func TestSettingsFlagRequiresOnlyProfile(t *testing.T) {
	v := validEnvironment()
	v["USER_SETTINGS_ENABLED"] = "true"
	if _, e := loadConfig(serviceruntime.MapEnv(v)); e == nil {
		t.Fatal("settings without profile")
	}
	v = profileEnvironment()
	c, e := loadConfig(serviceruntime.MapEnv(v))
	if e != nil || c.profile.settingsEnabled {
		t.Fatal("default enabled", e)
	}
	v["USER_SETTINGS_ENABLED"] = "true"
	c, e = loadConfig(serviceruntime.MapEnv(v))
	if e != nil || !c.profile.settingsEnabled || c.profile.addressesEnabled {
		t.Fatal("settings require addresses", e)
	}
	v["USER_SETTINGS_ENABLED"] = "invalid"
	if _, e = loadConfig(serviceruntime.MapEnv(v)); e == nil {
		t.Fatal("invalid flag accepted")
	}
}
