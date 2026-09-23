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
		{
			ID:          contractv1.RouteId_ROUTE_ID_AUTH_START_LOGIN_CODE_CHANGE,
			Method:      authv1.AuthBrowserService_BrowserStartLoginCodeChange_FullMethodName,
			NewRequest:  func() proto.Message { return new(authv1.BrowserStartLoginCodeChangeRequest) },
			NewResponse: func() proto.Message { return new(authv1.BrowserStartLoginCodeChangeResponse) },
		},
		{
			ID:          contractv1.RouteId_ROUTE_ID_AUTH_START_RECOVERY_CODES,
			Method:      authv1.AuthBrowserService_BrowserStartRecoveryCodes_FullMethodName,
			NewRequest:  func() proto.Message { return new(authv1.BrowserStartRecoveryCodesRequest) },
			NewResponse: func() proto.Message { return new(authv1.BrowserStartRecoveryCodesResponse) },
		},
		{
			ID:          contractv1.RouteId_ROUTE_ID_AUTH_COMPLETE_LOGIN_CODE_CHANGE,
			Method:      authv1.AuthBrowserService_BrowserCompleteLoginCodeChange_FullMethodName,
			NewRequest:  func() proto.Message { return new(authv1.BrowserCompleteLoginCodeChangeRequest) },
			NewResponse: func() proto.Message { return new(authv1.BrowserCompleteLoginCodeChangeResponse) },
		},
		{
			ID:          contractv1.RouteId_ROUTE_ID_AUTH_COMPLETE_RECOVERY_CODES,
			Method:      authv1.AuthBrowserService_BrowserCompleteRecoveryCodes_FullMethodName,
			NewRequest:  func() proto.Message { return new(authv1.BrowserCompleteRecoveryCodesRequest) },
			NewResponse: func() proto.Message { return new(authv1.BrowserCompleteRecoveryCodesResponse) },
		},
		{
			ID:          contractv1.RouteId_ROUTE_ID_AUTH_CHANGE_PASSWORD,
			Method:      authv1.AuthBrowserService_BrowserChangePassword_FullMethodName,
			NewRequest:  func() proto.Message { return new(authv1.BrowserChangePasswordRequest) },
			NewResponse: func() proto.Message { return new(authv1.BrowserChangePasswordResponse) },
		},
		{
			ID:          contractv1.RouteId_ROUTE_ID_AUTH_START_LOGIN,
			Method:      authv1.AuthBrowserService_BrowserStartLogin_FullMethodName,
			NewRequest:  func() proto.Message { return new(authv1.BrowserStartLoginRequest) },
			NewResponse: func() proto.Message { return new(authv1.BrowserStartLoginResponse) },
		},
		{
			ID:          contractv1.RouteId_ROUTE_ID_AUTH_COMPLETE_LOGIN,
			Method:      authv1.AuthBrowserService_BrowserCompleteLogin_FullMethodName,
			NewRequest:  func() proto.Message { return new(authv1.BrowserCompleteLoginRequest) },
			NewResponse: func() proto.Message { return new(authv1.BrowserCompleteLoginResponse) },
		},
		{
			ID:          contractv1.RouteId_ROUTE_ID_AUTH_RESEND_LOGIN_CODE,
			Method:      authv1.AuthBrowserService_BrowserResendLoginCode_FullMethodName,
			NewRequest:  func() proto.Message { return new(authv1.BrowserResendLoginCodeRequest) },
			NewResponse: func() proto.Message { return new(authv1.BrowserResendLoginCodeResponse) },
		},
		{
			ID:          contractv1.RouteId_ROUTE_ID_AUTH_REQUEST_EMAIL_VERIFICATION,
			Method:      authv1.AuthBrowserService_BrowserRequestEmailVerification_FullMethodName,
			NewRequest:  func() proto.Message { return new(authv1.BrowserRequestEmailVerificationRequest) },
			NewResponse: func() proto.Message { return new(authv1.BrowserRequestEmailVerificationResponse) },
		},
		{
			ID:          contractv1.RouteId_ROUTE_ID_AUTH_CONFIRM_EMAIL,
			Method:      authv1.AuthBrowserService_BrowserConfirmEmail_FullMethodName,
			NewRequest:  func() proto.Message { return new(authv1.BrowserConfirmEmailRequest) },
			NewResponse: func() proto.Message { return new(authv1.BrowserConfirmEmailResponse) },
		},
		{
			ID:          contractv1.RouteId_ROUTE_ID_AUTH_REQUEST_PASSWORD_RESET,
			Method:      authv1.AuthBrowserService_BrowserRequestPasswordReset_FullMethodName,
			NewRequest:  func() proto.Message { return new(authv1.BrowserRequestPasswordResetRequest) },
			NewResponse: func() proto.Message { return new(authv1.BrowserRequestPasswordResetResponse) },
		},
		{
			ID:          contractv1.RouteId_ROUTE_ID_AUTH_CONFIRM_PASSWORD_RESET,
			Method:      authv1.AuthBrowserService_BrowserConfirmPasswordReset_FullMethodName,
			NewRequest:  func() proto.Message { return new(authv1.BrowserConfirmPasswordResetRequest) },
			NewResponse: func() proto.Message { return new(authv1.BrowserConfirmPasswordResetResponse) },
		},
		{
			ID:          contractv1.RouteId_ROUTE_ID_AUTH_GET_CREDENTIALS,
			Method:      authv1.AuthBrowserService_BrowserGetCredentials_FullMethodName,
			NewRequest:  func() proto.Message { return new(authv1.BrowserGetCredentialsRequest) },
			NewResponse: func() proto.Message { return new(authv1.BrowserGetCredentialsResponse) },
		},
		{
			ID:          contractv1.RouteId_ROUTE_ID_AUTH_START_EMAIL_CHANGE,
			Method:      authv1.AuthBrowserService_BrowserStartEmailChange_FullMethodName,
			NewRequest:  func() proto.Message { return new(authv1.BrowserStartEmailChangeRequest) },
			NewResponse: func() proto.Message { return new(authv1.BrowserStartEmailChangeResponse) },
		},
		{
			ID:          contractv1.RouteId_ROUTE_ID_AUTH_CONFIRM_EMAIL_CHANGE,
			Method:      authv1.AuthBrowserService_BrowserConfirmEmailChange_FullMethodName,
			NewRequest:  func() proto.Message { return new(authv1.BrowserConfirmEmailChangeRequest) },
			NewResponse: func() proto.Message { return new(authv1.BrowserConfirmEmailChangeResponse) },
		},
		{
			ID:          contractv1.RouteId_ROUTE_ID_AUTH_CANCEL_EMAIL_CHANGE,
			Method:      authv1.AuthBrowserService_BrowserCancelEmailChange_FullMethodName,
			NewRequest:  func() proto.Message { return new(authv1.BrowserCancelEmailChangeRequest) },
			NewResponse: func() proto.Message { return new(authv1.BrowserCancelEmailChangeResponse) },
		},
		{
			ID:          contractv1.RouteId_ROUTE_ID_AUTH_LIST_SESSIONS,
			Method:      authv1.AuthBrowserService_BrowserListSessions_FullMethodName,
			NewRequest:  func() proto.Message { return new(authv1.BrowserListSessionsRequest) },
			NewResponse: func() proto.Message { return new(authv1.BrowserListSessionsResponse) },
		},
		{
			ID:          contractv1.RouteId_ROUTE_ID_AUTH_REVOKE_OWNED_SESSION,
			Method:      authv1.AuthBrowserService_BrowserRevokeSession_FullMethodName,
			NewRequest:  func() proto.Message { return new(authv1.BrowserRevokeSessionRequest) },
			NewResponse: func() proto.Message { return new(authv1.BrowserRevokeSessionResponse) },
		},
		{
			ID:          contractv1.RouteId_ROUTE_ID_AUTH_REQUEST_ACCOUNT_DELETION,
			Method:      authv1.AuthBrowserService_BrowserRequestAccountDeletion_FullMethodName,
			NewRequest:  func() proto.Message { return new(authv1.BrowserRequestAccountDeletionRequest) },
			NewResponse: func() proto.Message { return new(authv1.BrowserRequestAccountDeletionResponse) },
		},
		{
			ID:          contractv1.RouteId_ROUTE_ID_AUTH_CANCEL_ACCOUNT_DELETION,
			Method:      authv1.AuthBrowserService_BrowserCancelAccountDeletion_FullMethodName,
			NewRequest:  func() proto.Message { return new(authv1.BrowserCancelAccountDeletionRequest) },
			NewResponse: func() proto.Message { return new(authv1.BrowserCancelAccountDeletionResponse) },
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
