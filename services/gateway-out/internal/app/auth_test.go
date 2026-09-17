package app

import (
	"testing"
	"time"

	authv1 "github.com/v0hmly/marketmesh/api/gen/go/auth/v1"
	contractv1 "github.com/v0hmly/marketmesh/api/gen/go/tunnel/v1"
	serviceruntime "github.com/v0hmly/marketmesh/platform/runtime"
	"github.com/v0hmly/marketmesh/services/gateway-out/internal/tunnel"
)

func TestAuthConfigFailClosed(t *testing.T) {
	base := map[string]string{
		"SERVICE_VERSION": "test", "ENVIRONMENT": "test", "SERVICE_INSTANCE_ID": "gateway-out",
		"GATEWAY_IN_TARGET": "gateway-in:8443", "GATEWAY_IN_SERVER_NAME": "gateway-in", "EXPECTED_GATEWAY_IN_URI": "spiffe://marketmesh.test/test/gateway-in",
		"INTERNAL_TARGET": "internal:9090", "INTERNAL_SERVER_NAME": "internal", "EXPECTED_INTERNAL_URI": "spiffe://marketmesh.test/test/internal",
		"TUNNEL_TLS_CERT_FILE": "/tls/tunnel.crt", "TUNNEL_TLS_KEY_FILE": "/tls/tunnel.key", "TUNNEL_TLS_ROOT_CA_FILE": "/tls/ca.crt",
		"INTERNAL_TLS_CERT_FILE": "/tls/internal.crt", "INTERNAL_TLS_KEY_FILE": "/tls/internal.key", "INTERNAL_TLS_ROOT_CA_FILE": "/tls/ca.crt",
	}
	cfg, err := loadConfig(serviceruntime.MapEnv(base))
	if err != nil || cfg.authBrowserEnabled || cfg.userBrowserEnabled {
		t.Fatalf("default Auth configuration: %v", err)
	}
	required := map[string]string{
		"AUTH_TARGET": "auth:9091", "AUTH_SERVER_NAME": "auth", "EXPECTED_AUTH_URI": "spiffe://marketmesh.test/test/auth",
		"AUTH_TLS_CERT_FILE": "/tls/auth.crt", "AUTH_TLS_KEY_FILE": "/tls/auth.key", "AUTH_TLS_ROOT_CA_FILE": "/tls/ca.crt",
	}
	base["AUTH_BROWSER_ENABLED"] = "true"
	for key, value := range required {
		base[key] = value
	}
	for missing, value := range required {
		delete(base, missing)
		if _, err := loadConfig(serviceruntime.MapEnv(base)); err == nil {
			t.Fatalf("accepted missing %s", missing)
		}
		base[missing] = value
	}
	if cfg, err := loadConfig(serviceruntime.MapEnv(base)); err != nil || !cfg.authBrowserEnabled || cfg.authTarget != required["AUTH_TARGET"] {
		t.Fatalf("configured Auth: %v", err)
	}
	base["AUTH_BROWSER_ENABLED"] = "false"
	base["USER_BROWSER_ENABLED"] = "true"
	for missing, value := range required {
		delete(base, missing)
		if _, err := loadConfig(serviceruntime.MapEnv(base)); err == nil {
			t.Fatalf("User browser accepted missing %s", missing)
		}
		base[missing] = value
	}
	if cfg, err := loadConfig(serviceruntime.MapEnv(base)); err != nil || !cfg.userBrowserEnabled || cfg.authBrowserEnabled {
		t.Fatalf("User-only configuration: %v", err)
	}
	base["USER_BROWSER_ENABLED"] = "sometimes"
	if _, err := loadConfig(serviceruntime.MapEnv(base)); err == nil {
		t.Fatal("accepted invalid User flag")
	}
	base["USER_BROWSER_ENABLED"] = "false"
	base["AUTH_BROWSER_ENABLED"] = "sometimes"
	if _, err := loadConfig(serviceruntime.MapEnv(base)); err == nil {
		t.Fatal("accepted invalid Auth flag")
	}
}

func TestAuthRoutesUseOnlyPrivateTypedMutations(t *testing.T) {
	specs := authRoutes(time.Second)
	want := map[contractv1.RouteId]string{
		contractv1.RouteId_ROUTE_ID_AUTH_REGISTER_CREDENTIALS: authv1.AuthBrowserService_BrowserRegisterCredentials_FullMethodName,
		contractv1.RouteId_ROUTE_ID_AUTH_LOGIN:                authv1.AuthBrowserService_BrowserLogin_FullMethodName,
		contractv1.RouteId_ROUTE_ID_AUTH_REFRESH_SESSION:      authv1.AuthBrowserService_BrowserRefreshSession_FullMethodName,
		contractv1.RouteId_ROUTE_ID_AUTH_REVOKE_SESSION:       authv1.AuthBrowserService_BrowserLogout_FullMethodName,
		contractv1.RouteId_ROUTE_ID_AUTH_LOGOUT_ALL:           authv1.AuthBrowserService_BrowserLogoutAll_FullMethodName,
	}
	if len(specs) != len(want) {
		t.Fatal("unexpected route set")
	}
	for _, spec := range specs {
		if want[spec.ID] != spec.Method || !spec.Mutating || spec.RequireIdempotencyKey || spec.TrafficClass != contractv1.TrafficClass_TRAFFIC_CLASS_CONTROL_AUTH || spec.MaxDeadline != time.Second || spec.MaxRequestBytes != 16*1024 || spec.MaxResponseBytes != 16*1024 {
			t.Fatal("incorrect Auth route mapping")
		}
		delete(want, spec.ID)
		// A missing ControlAuth client cannot fall back to the regular internal service.
		if _, err := tunnel.NewRegistry(tunnel.ClassClients{}, spec); err == nil {
			t.Fatal("Auth route accepted without dedicated client")
		}
	}
	if len(want) != 0 {
		t.Fatal("duplicate route replaced a required method")
	}
}
