package app

import (
	"crypto/tls"
	"crypto/x509"
	"math"
	"testing"
	"time"

	serviceruntime "github.com/v0hmly/marketmesh/platform/runtime"
)

func registrationEnvironment() map[string]string {
	env := profileEnvironment()
	for k, v := range map[string]string{
		"USER_REGISTRATION_CONSUME_ENABLED": "true", "USER_NATS_URL": "tls://nats:4222", "USER_NATS_SERVER_NAME": "nats",
		"USER_NATS_TLS_CERT_FILE": "/run/nats-client.pem", "USER_NATS_TLS_KEY_FILE": "/run/nats-client-key.pem", "USER_NATS_TLS_CA_FILE": "/run/nats-ca.pem",
	} {
		env[k] = v
	}
	return env
}

func TestRegistrationConfigurationRequiresExplicitProfileAndBounds(t *testing.T) {
	if c, err := loadConfig(serviceruntime.MapEnv(profileEnvironment())); err != nil || c.registration.enabled {
		t.Fatal("consumer not opt-in", err)
	}
	if c, err := loadConfig(serviceruntime.MapEnv(registrationEnvironment())); err != nil || !c.registration.enabled {
		t.Fatal("configuration rejected", err)
	}
	for _, tc := range []struct{ key, value string }{
		{"USER_PROFILE_ENABLED", "false"}, {"USER_REGISTRATION_CONSUME_ENABLED", "yes"}, {"USER_NATS_TLS_KEY_FILE", "relative"},
		{"USER_EVENTS_CONNECT_TIMEOUT", "6s"}, {"USER_EVENTS_OPERATION_TIMEOUT", "2s"}, {"USER_EVENTS_OPERATION_TIMEOUT", "31s"},
		{"USER_EVENTS_FETCH_TIMEOUT", "6s"}, {"USER_EVENTS_RETRY_DELAY", "1ms"}, {"POSTGRES_QUERY_TIMEOUT", time.Duration(math.MaxInt64).String()},
	} {
		t.Run(tc.key+tc.value, func(t *testing.T) {
			env := registrationEnvironment()
			env[tc.key] = tc.value
			if _, err := loadConfig(serviceruntime.MapEnv(env)); err == nil {
				t.Fatal("unsafe consumer config accepted")
			}
		})
	}
	for _, key := range []string{"USER_NATS_URL", "USER_NATS_SERVER_NAME", "USER_NATS_TLS_CERT_FILE", "USER_NATS_TLS_KEY_FILE", "USER_NATS_TLS_CA_FILE"} {
		env := registrationEnvironment()
		delete(env, key)
		if _, err := loadConfig(serviceruntime.MapEnv(env)); err == nil {
			t.Fatal("missing config accepted", key)
		}
	}
}

func TestRegistrationTLSIdentifiesUserWithClientUsage(t *testing.T) {
	pki := newProfilePKI(t)
	for _, tc := range []struct {
		identity string
		usages   []x509.ExtKeyUsage
		valid    bool
	}{
		{"spiffe://marketmesh.test/test/user", []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, true},
		{"spiffe://marketmesh.test/test/auth", []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, false},
		{"spiffe://marketmesh.test/prod/user", []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, false},
		{"spiffe://other.test/test/user", []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, false},
		{"spiffe://marketmesh.test/test/user", []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, false},
	} {
		p := pki.config(t, tc.identity, tc.usages)
		c := registrationConfig{certificateFile: p.certificateFile, keyFile: p.keyFile, caFile: p.clientCAFile, serverName: "nats"}
		got, err := registrationTLS(c, "marketmesh.test", "test")
		if (err == nil) != tc.valid {
			t.Fatalf("%s: %v", tc.identity, err)
		}
		if tc.valid && (got.MinVersion != tls.VersionTLS13 || got.InsecureSkipVerify || got.RootCAs == nil || got.ServerName != "nats") {
			t.Fatal("TLS verification weakened")
		}
	}
}
