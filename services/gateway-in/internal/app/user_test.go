package app

import (
	"context"
	"errors"
	contractv1 "github.com/v0hmly/marketmesh/api/gen/go/tunnel/v1"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/v0hmly/marketmesh/platform/httpserver"
	"github.com/v0hmly/marketmesh/platform/logger"
	serviceruntime "github.com/v0hmly/marketmesh/platform/runtime"
	"github.com/v0hmly/marketmesh/platform/telemetry"
	"github.com/v0hmly/marketmesh/services/gateway-in/internal/tunnel"
)

func TestUserBrowserConfig(t *testing.T) {
	for _, test := range []struct {
		name, flag               string
		tls, snapshot, wantError bool
	}{
		{name: "default"}, {name: "disabled", flag: "false"},
		{name: "invalid", flag: "maybe", wantError: true},
		{name: "TLS required", flag: "true", wantError: true},
		{name: "enabled", flag: "true", tls: true},
		{name: "snapshot incompatible", flag: "true", tls: true, snapshot: true, wantError: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			env := validEnvironment()
			if test.flag != "" {
				env["USER_BROWSER_ENABLED"] = test.flag
			}
			if test.tls {
				env["PUBLIC_TLS_CERT_FILE"] = "/public.crt"
				env["PUBLIC_TLS_KEY_FILE"] = "/public.key"
			}
			if test.snapshot {
				env["E2E_ROUTING_SNAPSHOT_ENABLED"] = "true"
			}
			cfg, err := loadConfig(serviceruntime.MapEnv(env))
			if (err != nil) != test.wantError {
				t.Fatal(err)
			}
			if err == nil && cfg.userBrowserEnabled != (test.flag == "true") {
				t.Fatal("flag mismatch")
			}
		})
	}
	if _, err := loadPublicTLS(config{userBrowserEnabled: true}); err == nil {
		t.Fatal("missing TLS accepted")
	}
}

func TestUserNoStoreSurroundsPlatformRejections(t *testing.T) {
	log, err := logger.New(logger.Config{Service: "gateway-in", Version: "test", Environment: "test", Output: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	server, err := httpserver.New(httpserver.Config{Handler: http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("oversized request reached handler") }), ReadHeaderTimeout: time.Second, ReadTimeout: time.Second, WriteTimeout: time.Second, IdleTimeout: time.Second, RequestTimeout: time.Second, MaxHeaderBytes: 1024, MaxBodyBytes: 64 * 1024, Logger: log, Telemetry: telemetry.NewNoop()})
	if err != nil {
		t.Fatal(err)
	}
	handler := protectAuthResponses(config{userBrowserEnabled: true}, server.Handler)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest("POST", "https://shop.test/user.v1.UserService/UpdateMe", strings.NewReader(strings.Repeat("x", 65537))))
	if response.Code != 413 || response.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("cacheable body rejection")
	}
}

func TestUserRuntimeReplacesFakeRoutesAndRequiresReadiness(t *testing.T) {
	log, err := logger.New(logger.Config{Service: "gateway-in", Version: "test", Environment: "test", Output: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []bool{false, true} {
		cfg := config{userBrowserEnabled: enabled, instanceID: "test", expectedGatewayOutURI: "spiffe://test/gateway-out", dataCenter: "dc-a", requestTimeout: time.Second, healthTimeout: time.Second}
		server, err := tunnel.New(tunnelConfig(cfg, log, telemetry.NewNoop()))
		if err != nil {
			t.Fatal(err)
		}
		health, err := newHealth(cfg, server.Registry())
		if err != nil {
			t.Fatal(err)
		}
		health.MarkReady()
		if err := health.Ready(context.Background()); err == nil {
			t.Fatal("ready without User routes")
		}
		handler, err := publicHandler(cfg, health, server.Registry())
		if err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{"/user.v1.UserService/GetMe", "/marketmesh_test.v1.FakeInternalService/Read", "/gateway.v1.UserBrowserService/BrowserGetMe"} {
			request := httptest.NewRequest("POST", "https://shop.test"+path, strings.NewReader("{}"))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("Connect-Protocol-Version", "1")
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if path == "/gateway.v1.UserBrowserService/BrowserGetMe" && response.Code != 404 {
				t.Fatal("private method exposed")
			}
			if strings.HasPrefix(path, "/user.") {
				if enabled && response.Code != 503 {
					t.Fatal("User route did not fail unavailable", response.Code, response.Body.String())
				}
				if !enabled && response.Code != 404 {
					t.Fatal("User enabled by default")
				}
			}
			if strings.Contains(path, "FakeInternal") && enabled && response.Code != 404 {
				t.Fatal("fake route exposed")
			}
		}
	}
}

type readyRouteSet map[contractv1.RouteId]bool

func (routes readyRouteSet) IsRouteReady(route contractv1.RouteId) bool { return routes[route] }

func TestMixedProfileCodecsFailClosedInBothDirections(t *testing.T) {
	log, err := logger.New(logger.Config{Service: "gateway-in", Version: "test", Environment: "test", Output: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	for _, production := range []bool{false, true} {
		cfg := config{userBrowserEnabled: production, instanceID: "test", expectedGatewayOutURI: "spiffe://test/gateway-out", dataCenter: "dc-a", requestTimeout: time.Second, healthTimeout: time.Second}
		server, err := tunnel.New(tunnelConfig(cfg, log, telemetry.NewNoop()))
		if err != nil {
			t.Fatal(err)
		}
		expected := profileRouteIDs(cfg)
		wrong := profileRouteIDs(config{userBrowserEnabled: !production})
		for _, route := range expected {
			if _, allowed := server.Registry().RoutePolicy(route); !allowed {
				t.Fatal("expected route denied", route)
			}
		}
		for _, route := range wrong {
			if _, allowed := server.Registry().RoutePolicy(route); allowed {
				t.Fatal("wrong codec allowed", route)
			}
			if _, err := server.Registry().Invoke(context.Background(), tunnel.Call{Route: route, Payload: []byte("private browser context")}); !errors.Is(err, tunnel.ErrRouteNotAllowed) {
				t.Fatal("wrong codec was not rejected before routing", route, err)
			}
		}
		available := readyRouteSet{wrong[0]: true, wrong[1]: true}
		if profileRoutesReady(cfg, available) {
			t.Fatal("opposite codec satisfies readiness")
		}
		available[expected[0]] = true
		if profileRoutesReady(cfg, available) {
			t.Fatal("partial expected route set satisfies readiness")
		}
		available[expected[1]] = true
		if !profileRoutesReady(cfg, available) {
			t.Fatal("matching routes do not satisfy readiness")
		}
	}
}

func TestAddressFlagPoliciesAndReadiness(t *testing.T) {
	log, err := logger.New(logger.Config{Service: "gateway-in", Version: "test", Environment: "test", Output: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []bool{false, true} {
		cfg := config{userBrowserEnabled: true, userAddressesBrowserEnabled: enabled, instanceID: "test", expectedGatewayOutURI: "spiffe://test/gateway-out", dataCenter: "dc-a", requestTimeout: time.Second, healthTimeout: time.Second}
		config := tunnelConfig(cfg, log, telemetry.NewNoop())
		server, err := tunnel.New(config)
		if err != nil {
			t.Fatal(err)
		}
		ready := readyRouteSet{}
		for _, id := range profileRouteIDs(cfg) {
			ready[id] = true
		}
		if profileRoutesReady(cfg, ready) == enabled {
			t.Fatal("partial route readiness accepted")
		}
		for _, id := range addressRouteIDs() {
			policy, allowed := server.Registry().RoutePolicy(id)
			if allowed != enabled {
				t.Fatal("address gate mismatch")
			}
			if allowed && (policy.MaxRequestBytes != 16*1024 || policy.MaxResponseBytes != 128*1024) {
				t.Fatal("address limits mismatch")
			}
			ready[id] = true
		}
		if !profileRoutesReady(cfg, ready) {
			t.Fatal("complete route set unavailable")
		}
	}
	for _, value := range []string{"true", "bad", "false"} {
		env := validEnvironment()
		env["USER_ADDRESSES_BROWSER_ENABLED"] = value
		cfg, err := loadConfig(serviceruntime.MapEnv(env))
		if (err != nil) != (value != "false") {
			t.Fatal("invalid dependency accepted", cfg, err)
		}
	}
}

func TestSettingsFlagPoliciesAndReadiness(t *testing.T) {
	log, err := logger.New(logger.Config{Service: "gateway-in", Version: "test", Environment: "test", Output: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	for _, enabled := range []bool{false, true} {
		cfg := config{userBrowserEnabled: true, userSettingsBrowserEnabled: enabled, instanceID: "test", expectedGatewayOutURI: "spiffe://test/gateway-out", dataCenter: "dc-a", requestTimeout: time.Second, healthTimeout: time.Second}
		config := tunnelConfig(cfg, log, telemetry.NewNoop())
		server, err := tunnel.New(config)
		if err != nil {
			t.Fatal(err)
		}
		ready := readyRouteSet{}
		for _, id := range profileRouteIDs(cfg) {
			ready[id] = true
		}
		if profileRoutesReady(cfg, ready) == enabled {
			t.Fatal("partial route readiness accepted")
		}
		for _, id := range settingsRouteIDs() {
			policy, allowed := server.Registry().RoutePolicy(id)
			if allowed != enabled {
				t.Fatal("address gate mismatch")
			}
			if allowed && (policy.MaxRequestBytes != 16*1024 || policy.MaxResponseBytes != 16*1024) {
				t.Fatal("address limits mismatch")
			}
			ready[id] = true
		}
		if !profileRoutesReady(cfg, ready) {
			t.Fatal("complete route set unavailable")
		}
	}
	for _, value := range []string{"true", "bad", "false"} {
		env := validEnvironment()
		env["USER_SETTINGS_BROWSER_ENABLED"] = value
		cfg, err := loadConfig(serviceruntime.MapEnv(env))
		if (err != nil) != (value != "false") {
			t.Fatal("invalid dependency accepted", cfg, err)
		}
	}
}
