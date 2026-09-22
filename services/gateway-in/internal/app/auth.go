package app

import (
	"crypto/tls"
	"errors"
	"net/http"
	"strings"

	authv1connect "github.com/v0hmly/marketmesh/api/gen/go/auth/v1/authv1connect"
	contractv1 "github.com/v0hmly/marketmesh/api/gen/go/tunnel/v1"
	userv1connect "github.com/v0hmly/marketmesh/api/gen/go/user/v1/userv1connect"
	"github.com/v0hmly/marketmesh/services/gateway-in/internal/connectbridge"
	"github.com/v0hmly/marketmesh/services/gateway-in/internal/tunnel"
)

var browserAuthRoutes = [...]contractv1.RouteId{
	contractv1.RouteId_ROUTE_ID_AUTH_REGISTER_CREDENTIALS,
	contractv1.RouteId_ROUTE_ID_AUTH_LOGIN,
	contractv1.RouteId_ROUTE_ID_AUTH_REFRESH_SESSION,
	contractv1.RouteId_ROUTE_ID_AUTH_REVOKE_SESSION,
	contractv1.RouteId_ROUTE_ID_AUTH_LOGOUT_ALL,
	contractv1.RouteId_ROUTE_ID_AUTH_START_LOGIN_CODE_CHANGE,
	contractv1.RouteId_ROUTE_ID_AUTH_COMPLETE_LOGIN_CODE_CHANGE,
	contractv1.RouteId_ROUTE_ID_AUTH_CHANGE_PASSWORD,
	contractv1.RouteId_ROUTE_ID_AUTH_START_LOGIN,
	contractv1.RouteId_ROUTE_ID_AUTH_COMPLETE_LOGIN,
	contractv1.RouteId_ROUTE_ID_AUTH_RESEND_LOGIN_CODE,
	contractv1.RouteId_ROUTE_ID_AUTH_REQUEST_EMAIL_VERIFICATION,
	contractv1.RouteId_ROUTE_ID_AUTH_CONFIRM_EMAIL,
	contractv1.RouteId_ROUTE_ID_AUTH_REQUEST_PASSWORD_RESET,
	contractv1.RouteId_ROUTE_ID_AUTH_CONFIRM_PASSWORD_RESET,
	contractv1.RouteId_ROUTE_ID_AUTH_GET_CREDENTIALS,
	contractv1.RouteId_ROUTE_ID_AUTH_START_EMAIL_CHANGE,
	contractv1.RouteId_ROUTE_ID_AUTH_CONFIRM_EMAIL_CHANGE,
	contractv1.RouteId_ROUTE_ID_AUTH_CANCEL_EMAIL_CHANGE,
	contractv1.RouteId_ROUTE_ID_AUTH_LIST_SESSIONS,
	contractv1.RouteId_ROUTE_ID_AUTH_REVOKE_OWNED_SESSION,
	contractv1.RouteId_ROUTE_ID_AUTH_REQUEST_ACCOUNT_DELETION,
	contractv1.RouteId_ROUTE_ID_AUTH_CANCEL_ACCOUNT_DELETION,
}

func addAuthPolicies(cfg config, routes map[contractv1.RouteId]tunnel.RoutePolicy) {
	if !cfg.authBrowserEnabled {
		return
	}
	for _, route := range browserAuthRoutes {
		routes[route] = tunnel.RoutePolicy{
			TrafficClass:    contractv1.TrafficClass_TRAFFIC_CLASS_CONTROL_AUTH,
			MaxRequestBytes: 16 * 1024, MaxResponseBytes: 16 * 1024,
			MaxDeadline: cfg.requestTimeout, MaxInFlight: 4,
		}
	}
}

func authRoutesReady(cfg config, registry *tunnel.Registry) bool {
	if cfg.authBrowserEnabled {
		for _, route := range browserAuthRoutes {
			if !registry.IsRouteReady(route) {
				return false
			}
		}
	}
	return true
}

func registerAuthHandler(mux *http.ServeMux, cfg config, registry *tunnel.Registry) error {
	if !cfg.authBrowserEnabled {
		return nil
	}
	handler, err := connectbridge.NewAuthHandler(registry)
	if err != nil {
		return err
	}
	mux.Handle("/"+authv1connect.AuthServiceName+"/", handler)
	return nil
}

func loadPublicTLS(cfg config) (*tls.Config, error) {
	if !cfg.authBrowserEnabled && !cfg.userBrowserEnabled {
		return nil, nil
	}
	certificate, err := tls.LoadX509KeyPair(cfg.publicTLSCertificate, cfg.publicTLSPrivateKey)
	if err != nil {
		return nil, errors.New("gateway-in: loading public TLS key pair")
	}
	// TLS ends at gateway-in. Forwarded headers never establish a secure browser origin.
	return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{certificate}}, nil
}

func protectAuthResponses(cfg config, next http.Handler) http.Handler {
	if !cfg.authBrowserEnabled && !cfg.userBrowserEnabled {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if (cfg.authBrowserEnabled && strings.HasPrefix(r.URL.Path, "/"+authv1connect.AuthServiceName+"/")) ||
			(cfg.userBrowserEnabled && strings.HasPrefix(r.URL.Path, "/"+userv1connect.UserServiceName+"/")) {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}
