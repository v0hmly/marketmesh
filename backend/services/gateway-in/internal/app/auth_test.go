package app

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	authv1connect "github.com/v0hmly/marketmesh/api/gen/go/auth/v1/authv1connect"
	contractv1 "github.com/v0hmly/marketmesh/api/gen/go/tunnel/v1"
	"github.com/v0hmly/marketmesh/platform/httpserver"
	"github.com/v0hmly/marketmesh/platform/logger"
	serviceruntime "github.com/v0hmly/marketmesh/platform/runtime"
	"github.com/v0hmly/marketmesh/platform/telemetry"
	"github.com/v0hmly/marketmesh/services/gateway-in/internal/tunnel"
)

func TestBrowserAuthRequiresExplicitFlagAndPublicTLS(t *testing.T) {
	for _, test := range []struct {
		name, flag string
		cert, key  bool
		wantError  bool
	}{
		{name: "default"}, {name: "disabled", flag: "false"},
		{name: "invalid flag", flag: "maybe", wantError: true},
		{name: "missing TLS", flag: "true", wantError: true},
		{name: "missing key", flag: "true", cert: true, wantError: true},
		{name: "missing certificate", flag: "true", key: true, wantError: true},
		{name: "configured", flag: "true", cert: true, key: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			env := validEnvironment()
			if test.flag != "" {
				env["AUTH_BROWSER_ENABLED"] = test.flag
			}
			if test.cert {
				env["PUBLIC_TLS_CERT_FILE"] = "/tls/public.crt"
			}
			if test.key {
				env["PUBLIC_TLS_KEY_FILE"] = "/tls/public.key"
			}
			cfg, err := loadConfig(serviceruntime.MapEnv(env))
			if (err != nil) != test.wantError {
				t.Fatalf("configuration error = %v", err)
			}
			if err == nil && cfg.authBrowserEnabled != (test.flag == "true") {
				t.Fatal("unexpected Auth flag")
			}
		})
	}
	if _, err := loadPublicTLS(config{authBrowserEnabled: true}); err == nil {
		t.Fatal("missing TLS materials accepted")
	}
	if tlsConfig, err := loadPublicTLS(config{}); err != nil || tlsConfig != nil {
		t.Fatal("disabled Auth requires TLS")
	}
}

func TestBrowserAuthPoliciesAreFiniteAndDisabledByDefault(t *testing.T) {
	routes := make(map[contractv1.RouteId]tunnel.RoutePolicy)
	addAuthPolicies(config{}, routes)
	if len(routes) != 0 {
		t.Fatal("Auth routes enabled by default")
	}
	addAuthPolicies(config{authBrowserEnabled: true, requestTimeout: time.Second}, routes)
	if len(routes) != 23 {
		t.Fatalf("got %d routes", len(routes))
	}
	if _, exists := routes[contractv1.RouteId_ROUTE_ID_AUTH_SESSION_ASSERTION]; exists {
		t.Fatal("assertion exposed publicly")
	}
	for _, policy := range routes {
		if policy.TrafficClass != contractv1.TrafficClass_TRAFFIC_CLASS_CONTROL_AUTH || policy.MaxRequestBytes != 16*1024 || policy.MaxResponseBytes != 16*1024 || policy.MaxDeadline != time.Second || policy.MaxInFlight != 4 {
			t.Fatal("unbounded or non-Auth route")
		}
	}
}

func TestAuthNoStoreSurroundsPlatformBodyRejection(t *testing.T) {
	log, err := logger.New(logger.Config{Service: "gateway-in", Version: "test", Environment: "test", Output: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	server, err := httpserver.New(httpserver.Config{
		Handler:           http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("oversized request reached handler") }),
		ReadHeaderTimeout: time.Second, ReadTimeout: time.Second, WriteTimeout: time.Second, IdleTimeout: time.Second,
		RequestTimeout: time.Second, MaxHeaderBytes: 1024, MaxBodyBytes: 64 * 1024, Logger: log, Telemetry: telemetry.NewNoop(),
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := protectAuthResponses(config{authBrowserEnabled: true}, server.Handler)
	request := httptest.NewRequest(http.MethodPost, "https://shop.test"+authv1connect.AuthServiceLoginProcedure, strings.NewReader(strings.Repeat("x", 64*1024+1)))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	if response.Code != http.StatusRequestEntityTooLarge || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("body limit error is cacheable")
	}
}
