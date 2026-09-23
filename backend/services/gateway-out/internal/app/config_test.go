package app

import (
	"testing"

	serviceruntime "github.com/v0hmly/marketmesh/platform/runtime"
)

func TestPeriodicRediscoveryConfiguration(t *testing.T) {
	for _, environment := range []string{"test", "dev", "production", "preprod", "local", "staging", "TEST", "unknown"} {
		for _, flag := range []string{"unset", "true", "false", "invalid"} {
			t.Run(environment+"/"+flag, func(t *testing.T) {
				values := map[string]string{
					"SERVICE_VERSION": "test", "ENVIRONMENT": environment, "SERVICE_INSTANCE_ID": "gateway-out",
					"GATEWAY_IN_TARGET": "gateway-in:8443", "GATEWAY_IN_SERVER_NAME": "gateway-in", "EXPECTED_GATEWAY_IN_URI": "spiffe://marketmesh.test/test/gateway-in",
					"INTERNAL_TARGET": "internal:9090", "INTERNAL_SERVER_NAME": "internal", "EXPECTED_INTERNAL_URI": "spiffe://marketmesh.test/test/internal",
					"TUNNEL_TLS_CERT_FILE": "/tls/tunnel.crt", "TUNNEL_TLS_KEY_FILE": "/tls/tunnel.key", "TUNNEL_TLS_ROOT_CA_FILE": "/tls/ca.crt",
					"INTERNAL_TLS_CERT_FILE": "/tls/internal.crt", "INTERNAL_TLS_KEY_FILE": "/tls/internal.key", "INTERNAL_TLS_ROOT_CA_FILE": "/tls/ca.crt",
				}
				if flag != "unset" {
					values["TUNNEL_PERIODIC_REDISCOVERY_ENABLED"] = flag
				}
				cfg, err := loadConfig(serviceruntime.MapEnv(values))
				rejected := flag == "invalid" || (flag == "false" && environment != "test")
				if rejected {
					if err == nil {
						t.Fatal("unsafe or invalid configuration accepted")
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				if cfg.periodicRediscoveryEnabled != (flag != "false") {
					t.Fatal("incorrect rediscovery setting")
				}
			})
		}
	}
}
