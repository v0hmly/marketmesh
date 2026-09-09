package app

import (
	"math"
	"strings"
	"testing"
	"time"

	serviceruntime "github.com/v0hmly/marketmesh/platform/runtime"
)

func registrationEnvironment() map[string]string {
	return map[string]string{"AUTH_REGISTRATION_EVENTS_ENABLED": "true", "AUTH_REGISTRATION_PUBLISH_ENABLED": "true", "AUTH_NATS_URL": "tls://nats:4222", "AUTH_NATS_SERVER_NAME": "nats", "AUTH_EVENTS_TRUST_DOMAIN": "marketmesh.test", "AUTH_NATS_TLS_CERT_FILE": "/fixture/auth-cert.pem", "AUTH_NATS_TLS_KEY_FILE": "/fixture/auth-key.pem", "AUTH_NATS_TLS_CA_FILE": "/fixture/ca.pem"}
}
func TestRegistrationRolloutAndBounds(t *testing.T) {
	for _, timeout := range []time.Duration{time.Duration(math.MaxInt64), 0, -time.Second, 12 * time.Second} {
		if _, err := loadRegistrationConfig(serviceruntime.MapEnv(registrationEnvironment()), "test", timeout); err == nil {
			t.Fatal("invalid or overflowing DB timeout accepted", timeout)
		}
	}
	c, err := loadRegistrationConfig(serviceruntime.MapEnv(nil), "test", 3*time.Second)
	if err != nil || c.enabled || c.publish {
		t.Fatal(c, err)
	}
	c, err = loadRegistrationConfig(serviceruntime.MapEnv(map[string]string{"AUTH_REGISTRATION_EVENTS_ENABLED": "true"}), "test", 3*time.Second)
	if err != nil || !c.enabled || c.publish {
		t.Fatal(c, err)
	}
	c, err = loadRegistrationConfig(serviceruntime.MapEnv(registrationEnvironment()), "test", 3*time.Second)
	if err != nil || !c.publish {
		t.Fatal(c, err)
	}
	for key, value := range map[string]string{
		"AUTH_REGISTRATION_EVENTS_ENABLED": "false", "AUTH_NATS_URL": "tls://hidden-secret:password@nats:4222", "AUTH_NATS_TLS_KEY_FILE": "relative/key",
		"AUTH_EVENTS_LEASE_DURATION": "10s", "AUTH_EVENTS_PUBLISH_TIMEOUT": "31s", "AUTH_EVENTS_POLL_INTERVAL": "1ns", "AUTH_EVENTS_BATCH_SIZE": "257", "AUTH_EVENTS_RETRY_INITIAL": "1ns", "AUTH_EVENTS_TRUST_DOMAIN": "bad/domain",
	} {
		values := registrationEnvironment()
		values[key] = value
		_, err := loadRegistrationConfig(serviceruntime.MapEnv(values), "test", 3*time.Second)
		if err == nil {
			t.Fatal("invalid setting accepted", key)
		}
		if strings.Contains(err.Error(), "hidden-secret") {
			t.Fatal("secret leaked")
		}
	}
	for _, endpoint := range []string{"nats://nats:4222", "tls://nats:0", "tls://nats:65536", "tls://nats:4222?password=secret", "tls://nats:4222/", "tls://nats"} {
		values := registrationEnvironment()
		values["AUTH_NATS_URL"] = endpoint
		if _, err := loadRegistrationConfig(serviceruntime.MapEnv(values), "test", 3*time.Second); err == nil {
			t.Fatal("invalid NATS endpoint accepted")
		}
	}
}
