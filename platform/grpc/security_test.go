package grpc

import (
	"crypto/tls"
	"crypto/x509"
	"strings"
	"testing"
)

func TestPlaintextPolicy(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		environment string
		mode        PlaintextMode
		wantError   string
	}{
		{
			name:        "local development",
			environment: "local",
			mode:        PlaintextLocal,
		},
		{
			name:        "production trusted mesh",
			environment: "production",
			mode:        PlaintextTrustedMesh,
		},
		{
			name:        "production local exception",
			environment: "production",
			mode:        PlaintextLocal,
			wantError:   "forbidden in production",
		},
		{
			name:        "implicit plaintext",
			environment: "test",
			mode:        PlaintextForbidden,
			wantError:   "TLS config is required",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := plaintextCredentials(test.environment, test.mode)
			if test.wantError == "" && err != nil {
				t.Fatalf("plaintextCredentials() error = %v", err)
			}
			if test.wantError != "" && (err == nil || !strings.Contains(err.Error(), test.wantError)) {
				t.Fatalf("plaintextCredentials() error = %v, want %q", err, test.wantError)
			}
		})
	}
}

func TestSecureTLSConfigClonesAndHardens(t *testing.T) {
	t.Parallel()

	original := &tls.Config{ServerName: "service.internal"}
	secured, err := secureTLSConfig(original)
	if err != nil {
		t.Fatalf("secureTLSConfig() error = %v", err)
	}
	if secured == original {
		t.Fatal("secureTLSConfig() returned original pointer")
	}
	if secured.MinVersion != tls.VersionTLS12 {
		t.Fatalf("MinVersion = %d, want TLS 1.2", secured.MinVersion)
	}
	if original.MinVersion != 0 {
		t.Fatal("secureTLSConfig() mutated caller config")
	}
}

func TestSecureTLSConfigRejectsUnsafeSettings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		config *tls.Config
	}{
		{
			name: "skip verification",
			config: &tls.Config{
				InsecureSkipVerify: true, //nolint:gosec // проверяется запрет настройки
			},
		},
		{
			name: "old TLS",
			config: &tls.Config{
				MinVersion: tls.VersionTLS11,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if _, err := secureTLSConfig(test.config); err == nil {
				t.Fatal("secureTLSConfig() error = nil, want validation error")
			}
		})
	}
}

func TestMutualTLSRequiresPeerMaterial(t *testing.T) {
	t.Parallel()

	_, serverErr := serverTransportCredentials("test", ServerSecurity{
		TLSConfig:                &tls.Config{Certificates: []tls.Certificate{{}}},
		RequireClientCertificate: true,
	})
	if serverErr == nil || !strings.Contains(serverErr.Error(), "client CA pool") {
		t.Fatalf("serverTransportCredentials() error = %v, want client CA error", serverErr)
	}

	_, clientErr := clientTransportCredentials("test", ClientSecurity{
		TLSConfig: &tls.Config{
			RootCAs: x509.NewCertPool(),
		},
		RequireClientCertificate: true,
	})
	if clientErr == nil || !strings.Contains(clientErr.Error(), "client certificate") {
		t.Fatalf("clientTransportCredentials() error = %v, want client certificate error", clientErr)
	}
}
