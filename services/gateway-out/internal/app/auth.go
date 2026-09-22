package app

import (
	"context"
	"fmt"
	"time"

	authv1 "github.com/v0hmly/marketmesh/api/gen/go/auth/v1"
	contractv1 "github.com/v0hmly/marketmesh/api/gen/go/tunnel/v1"
	platformgrpc "github.com/v0hmly/marketmesh/platform/grpc"
	"github.com/v0hmly/marketmesh/platform/logger"
	"github.com/v0hmly/marketmesh/platform/telemetry"
	"github.com/v0hmly/marketmesh/services/gateway-out/internal/tunnel"
	"google.golang.org/protobuf/proto"
)

func newAuthClient(ctx context.Context, cfg config, log *logger.Logger, pipeline *telemetry.Telemetry) (*platformgrpc.Client, error) {
	if !cfg.authBrowserEnabled && !cfg.userBrowserEnabled && !cfg.filesBrowserEnabled {
		return nil, nil
	}
	tlsConfig, err := loadClientTLS(cfg.authCertificate, cfg.authPrivateKey, cfg.authRootCA, cfg.authServerName, cfg.expectedAuthURI)
	if err != nil {
		return nil, err
	}
	client, err := platformgrpc.NewClient(ctx, platformgrpc.ClientConfig{
		Target: cfg.authTarget, Environment: cfg.environment, DisableRetries: true,
		ConnectTimeout: cfg.connectTimeout, CallTimeout: cfg.callTimeout,
		KeepaliveTime: 30 * time.Second, KeepaliveTimeout: 5 * time.Second,
		MaxReceiveMessageBytes: 16 * 1024, MaxSendMessageBytes: 16 * 1024,
		Security: platformgrpc.ClientSecurity{TLSConfig: tlsConfig, RequireClientCertificate: true},
		Logger:   log, Telemetry: pipeline,
	})
	if err != nil {
		return nil, fmt.Errorf("creating Auth gRPC client: %w", err)
	}
	return client, nil
}

func authRoutes(timeout time.Duration) []tunnel.RouteSpec {
	specs := []tunnel.RouteSpec{
		{
			ID:          contractv1.RouteId_ROUTE_ID_AUTH_REGISTER_CREDENTIALS,
			Method:      authv1.AuthBrowserService_BrowserRegisterCredentials_FullMethodName,
			NewRequest:  func() proto.Message { return new(authv1.BrowserRegisterCredentialsRequest) },
			NewResponse: func() proto.Message { return new(authv1.BrowserRegisterCredentialsResponse) },
		},
		{
			ID:          contractv1.RouteId_ROUTE_ID_AUTH_LOGIN,
			Method:      authv1.AuthBrowserService_BrowserLogin_FullMethodName,
			NewRequest:  func() proto.Message { return new(authv1.BrowserLoginRequest) },
			NewResponse: func() proto.Message { return new(authv1.BrowserLoginResponse) },
		},
		{
			ID:          contractv1.RouteId_ROUTE_ID_AUTH_REFRESH_SESSION,
			Method:      authv1.AuthBrowserService_BrowserRefreshSession_FullMethodName,
			NewRequest:  func() proto.Message { return new(authv1.BrowserRefreshSessionRequest) },
			NewResponse: func() proto.Message { return new(authv1.BrowserRefreshSessionResponse) },
		},
		{
			ID:          contractv1.RouteId_ROUTE_ID_AUTH_REVOKE_SESSION,
			Method:      authv1.AuthBrowserService_BrowserLogout_FullMethodName,
			NewRequest:  func() proto.Message { return new(authv1.BrowserLogoutRequest) },
			NewResponse: func() proto.Message { return new(authv1.BrowserLogoutResponse) },
		},
		{
			ID:          contractv1.RouteId_ROUTE_ID_AUTH_LOGOUT_ALL,
			Method:      authv1.AuthBrowserService_BrowserLogoutAll_FullMethodName,
			NewRequest:  func() proto.Message { return new(authv1.BrowserLogoutAllRequest) },
			NewResponse: func() proto.Message { return new(authv1.BrowserLogoutAllResponse) },
		},
	}
	for index := range specs {
		specs[index].TrafficClass = contractv1.TrafficClass_TRAFFIC_CLASS_CONTROL_AUTH
		specs[index].MaxRequestBytes = 16 * 1024
		specs[index].MaxResponseBytes = 16 * 1024
		specs[index].MaxDeadline = timeout
		specs[index].Mutating = true
		// Session rotation and credential writes are never retried automatically.
	}
	return specs
}
