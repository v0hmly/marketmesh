package redis

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestNormalizeConfigAcceptsPlaintextExceptionAndTLS(t *testing.T) {
	t.Parallel()

	plaintext := validConfig(t)
	settings, err := normalizeConfig(plaintext)
	if err != nil {
		t.Fatalf("normalizeConfig(plaintext) error = %v", err)
	}
	if settings.role != RoleEdge || settings.transport.tls != nil {
		t.Fatalf("plaintext settings = %+v", settings)
	}

	secured := validConfig(t)
	secured.Transport.PlaintextException = nil
	secured.Transport.TLS = &TLSConfig{
		ServerName: testSecret(t, "redis.internal"),
	}
	settings, err = normalizeConfig(secured)
	if err != nil {
		t.Fatalf("normalizeConfig(TLS) error = %v", err)
	}
	if settings.transport.tls == nil ||
		settings.transport.tls.MinVersion != tls.VersionTLS12 ||
		settings.transport.tls.ServerName != "redis.internal" ||
		settings.transport.tls.InsecureSkipVerify {
		t.Fatalf("TLS settings = %+v", settings.transport.tls)
	}
}

func TestNormalizeConfigRejectsUnsafeOrUnboundedConfiguration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{"unknown role", func(config *Config) { config.Role = "shared" }},
		{"missing address", func(config *Config) { config.Address = testSecret(t, "") }},
		{"malformed address", func(config *Config) { config.Address = testSecret(t, "redis") }},
		{"invalid address port", func(config *Config) { config.Address = testSecret(t, "redis:port") }},
		{"missing password", func(config *Config) { config.Authentication.Password = testSecret(t, "") }},
		{"negative database", func(config *Config) { config.Database = -1 }},
		{"missing transport", func(config *Config) { config.Transport = TransportConfig{} }},
		{"ambiguous transport", func(config *Config) {
			config.Transport.TLS = &TLSConfig{ServerName: testSecret(t, "redis.internal")}
		}},
		{"blank exception", func(config *Config) {
			config.Transport.PlaintextException.Reason = " "
		}},
		{"old TLS", func(config *Config) {
			config.Transport.PlaintextException = nil
			config.Transport.TLS = &TLSConfig{
				ServerName: testSecret(t, "redis.internal"),
				MinVersion: tls.VersionTLS11,
			}
		}},
		{"unbounded active pool", func(config *Config) { config.Pool.MaxActiveConns = 0 }},
		{"unbounded lifetime", func(config *Config) { config.Pool.ConnMaxLifetime = 0 }},
		{"zero command timeout", func(config *Config) { config.Timeouts.Command = 0 }},
		{"pool exceeds command", func(config *Config) {
			config.Timeouts.Pool = config.Timeouts.Command + time.Second
		}},
		{"too many retries", func(config *Config) { config.Retry.MaxAttempts = 6 }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			config := validConfig(t)
			test.mutate(&config)
			_, err := normalizeConfig(config)
			if !errors.Is(err, ErrInvalidConfig) {
				t.Fatalf("normalizeConfig() error = %v, want ErrInvalidConfig", err)
			}
			if strings.Contains(err.Error(), "private-password") ||
				strings.Contains(err.Error(), "edge-state") {
				t.Fatalf("configuration error exposed sensitive data: %v", err)
			}
		})
	}
}

func TestNormalizeConfigCopiesTLSRootCAs(t *testing.T) {
	t.Parallel()

	config := validConfig(t)
	config.Transport.PlaintextException = nil
	config.Transport.TLS = &TLSConfig{
		ServerName: testSecret(t, "redis.internal"),
		RootCAs:    x509.NewCertPool(),
	}
	settings, err := normalizeConfig(config)
	if err != nil {
		t.Fatalf("normalizeConfig() error = %v", err)
	}
	if settings.transport.tls.RootCAs == config.Transport.TLS.RootCAs {
		t.Fatal("normalizeConfig() retained mutable RootCAs")
	}
}
