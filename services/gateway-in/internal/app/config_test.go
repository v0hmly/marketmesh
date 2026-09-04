package app

import (
	"io"
	"testing"
	"time"

	"github.com/v0hmly/marketmesh/platform/logger"
	serviceruntime "github.com/v0hmly/marketmesh/platform/runtime"
	"github.com/v0hmly/marketmesh/platform/telemetry"
)

func TestLoadConfigRequiresBoundedDataCenter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		dataCenter string
		wantError  bool
	}{
		{name: "dc a", dataCenter: "dc-a"},
		{name: "dc b", dataCenter: "dc-b"},
		{name: "missing", wantError: true},
		{name: "unbounded", dataCenter: "customer-dc", wantError: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			environment := validEnvironment()
			if test.name == "missing" {
				delete(environment, "DATA_CENTER")
			} else {
				environment["DATA_CENTER"] = test.dataCenter
			}

			cfg, err := loadConfig(serviceruntime.MapEnv(environment))
			if (err != nil) != test.wantError {
				t.Fatalf("loadConfig() error = %v, wantError = %v", err, test.wantError)
			}
			if err == nil && cfg.dataCenter != test.dataCenter {
				t.Fatalf("dataCenter = %q, want %q", cfg.dataCenter, test.dataCenter)
			}
		})
	}
}

func TestLoadConfigKeepsE2ESnapshotDisabledByDefault(t *testing.T) {
	t.Parallel()

	cfg, err := loadConfig(serviceruntime.MapEnv(validEnvironment()))
	if err != nil {
		t.Fatalf("loadConfig() error = %v", err)
	}
	if cfg.e2eRoutingSnapshot {
		t.Fatal("e2eRoutingSnapshot = true, want false")
	}
}

func TestLoadConfigValidatesE2ESnapshotSwitch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		enabled   string
		wantValue bool
		wantError bool
	}{
		{name: "invalid", enabled: "sometimes", wantError: true},
		{name: "disabled", enabled: "false"},
		{name: "enabled", enabled: "true", wantValue: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			environment := validEnvironment()
			environment["E2E_ROUTING_SNAPSHOT_ENABLED"] = test.enabled
			cfg, err := loadConfig(serviceruntime.MapEnv(environment))
			if (err != nil) != test.wantError {
				t.Fatalf("loadConfig() error = %v, wantError = %v", err, test.wantError)
			}
			if err == nil && cfg.e2eRoutingSnapshot != test.wantValue {
				t.Fatalf("e2eRoutingSnapshot = %t, want %t", cfg.e2eRoutingSnapshot, test.wantValue)
			}
		})
	}
}

func TestTunnelConfigPinsAuthenticatedPeerToConfiguredDataCenter(t *testing.T) {
	t.Parallel()

	serviceLog, err := logger.New(logger.Config{
		Service: "gateway-in", Version: "test", Environment: "test", Output: io.Discard,
	})
	if err != nil {
		t.Fatalf("logger.New() error = %v", err)
	}
	configuration := config{
		instanceID:            "gateway-in-0",
		dataCenter:            "dc-b",
		expectedGatewayOutURI: "spiffe://marketmesh.test/gateway-out",
		requestTimeout:        time.Second,
	}

	tunnelConfiguration := tunnelConfig(configuration, serviceLog, telemetry.NewNoop())
	if len(tunnelConfiguration.Peer.AllowedURIs) != 1 ||
		tunnelConfiguration.Peer.AllowedURIs[0] != configuration.expectedGatewayOutURI {
		t.Fatalf("AllowedURIs = %v, want exact configured URI", tunnelConfiguration.Peer.AllowedURIs)
	}
	if got := tunnelConfiguration.Peer.DataCenterByURI[configuration.expectedGatewayOutURI]; got != configuration.dataCenter {
		t.Fatalf("DataCenterByURI = %q, want %q", got, configuration.dataCenter)
	}
}

func validEnvironment() map[string]string {
	return map[string]string{
		"SERVICE_VERSION":          "test",
		"ENVIRONMENT":              "e2e",
		"SERVICE_INSTANCE_ID":      "gateway-in-0",
		"DATA_CENTER":              "dc-a",
		"TLS_CERT_FILE":            "/tls/tls.crt",
		"TLS_KEY_FILE":             "/tls/tls.key",
		"TLS_CLIENT_CA_FILE":       "/tls/ca.crt",
		"EXPECTED_GATEWAY_OUT_URI": "spiffe://marketmesh.test/gateway-out",
	}
}
