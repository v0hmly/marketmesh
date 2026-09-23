package connectbridge

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"

	"connectrpc.com/connect"
	authv1 "github.com/v0hmly/marketmesh/api/gen/go/auth/v1"
	authv1connect "github.com/v0hmly/marketmesh/api/gen/go/auth/v1/authv1connect"
	contractv1 "github.com/v0hmly/marketmesh/api/gen/go/tunnel/v1"
	"github.com/v0hmly/marketmesh/services/gateway-in/internal/tunnel"
	"google.golang.org/protobuf/proto"
)

// NewAuthHandler exposes only the fixed public browser Auth procedures.
// Browser context remains private tunnel data; Auth alone interprets cookies.
func NewAuthHandler(invoker Invoker, options ...connect.HandlerOption) (http.Handler, error) {
	if isNilInvoker(invoker) {
		return nil, errors.New("connect auth bridge: invoker is required")
	}
	mux := http.NewServeMux()
	if err := mountAuth(mux, invoker, authv1connect.AuthServiceRegisterCredentialsProcedure, contractv1.RouteId_ROUTE_ID_AUTH_REGISTER_CREDENTIALS,
		func(request *authv1.RegisterCredentialsRequest, browser *authv1.BrowserContext) proto.Message {
			return &authv1.BrowserRegisterCredentialsRequest{Request: request, Context: browser}
		},
		func(payload []byte) (*authv1.RegisterCredentialsResponse, []string, authv1.AuthBrowserFailure, error) {
			response := new(authv1.BrowserRegisterCredentialsResponse)
			err := proto.Unmarshal(payload, response)
			return response.GetResponse(), response.GetSetCookie(), response.GetFailure(), err
		}, options); err != nil {
		return nil, err
	}
	if err := mountAuth(mux, invoker, authv1connect.AuthServiceLoginProcedure, contractv1.RouteId_ROUTE_ID_AUTH_LOGIN,
		func(request *authv1.LoginRequest, browser *authv1.BrowserContext) proto.Message {
			return &authv1.BrowserLoginRequest{Request: request, Context: browser}
		},
		func(payload []byte) (*authv1.LoginResponse, []string, authv1.AuthBrowserFailure, error) {
			response := new(authv1.BrowserLoginResponse)
			err := proto.Unmarshal(payload, response)
			return response.GetResponse(), response.GetSetCookie(), response.GetFailure(), err
		}, options); err != nil {
		return nil, err
	}
	if err := mountAuth(mux, invoker, authv1connect.AuthServiceRefreshSessionProcedure, contractv1.RouteId_ROUTE_ID_AUTH_REFRESH_SESSION,
		func(request *authv1.RefreshSessionRequest, browser *authv1.BrowserContext) proto.Message {
			return &authv1.BrowserRefreshSessionRequest{Request: request, Context: browser}
		},
		func(payload []byte) (*authv1.RefreshSessionResponse, []string, authv1.AuthBrowserFailure, error) {
			response := new(authv1.BrowserRefreshSessionResponse)
			err := proto.Unmarshal(payload, response)
			return response.GetResponse(), response.GetSetCookie(), response.GetFailure(), err
		}, options); err != nil {
		return nil, err
	}
	if err := mountAuth(mux, invoker, authv1connect.AuthServiceLogoutProcedure, contractv1.RouteId_ROUTE_ID_AUTH_REVOKE_SESSION,
		func(request *authv1.LogoutRequest, browser *authv1.BrowserContext) proto.Message {
			return &authv1.BrowserLogoutRequest{Request: request, Context: browser}
		},
		func(payload []byte) (*authv1.LogoutResponse, []string, authv1.AuthBrowserFailure, error) {
			response := new(authv1.BrowserLogoutResponse)
			err := proto.Unmarshal(payload, response)
			return response.GetResponse(), response.GetSetCookie(), response.GetFailure(), err
		}, options); err != nil {
		return nil, err
	}
	if err := mountAuth(mux, invoker, authv1connect.AuthServiceLogoutAllProcedure, contractv1.RouteId_ROUTE_ID_AUTH_LOGOUT_ALL,
		func(request *authv1.LogoutAllRequest, browser *authv1.BrowserContext) proto.Message {
			return &authv1.BrowserLogoutAllRequest{Request: request, Context: browser}
		},
		func(payload []byte) (*authv1.LogoutAllResponse, []string, authv1.AuthBrowserFailure, error) {
			response := new(authv1.BrowserLogoutAllResponse)
			err := proto.Unmarshal(payload, response)
			return response.GetResponse(), response.GetSetCookie(), response.GetFailure(), err
		}, options); err != nil {
		return nil, err
	}
	if err := mountAuth(mux, invoker, authv1connect.AuthServiceStartLoginCodeChangeProcedure, contractv1.RouteId_ROUTE_ID_AUTH_START_LOGIN_CODE_CHANGE,
		func(request *authv1.StartLoginCodeChangeRequest, browser *authv1.BrowserContext) proto.Message {
			return &authv1.BrowserStartLoginCodeChangeRequest{Request: request, Context: browser}
		},
		func(payload []byte) (*authv1.StartLoginCodeChangeResponse, []string, authv1.AuthBrowserFailure, error) {
			response := new(authv1.BrowserStartLoginCodeChangeResponse)
			err := proto.Unmarshal(payload, response)
			return response.GetResponse(), response.GetSetCookie(), response.GetFailure(), err
		}, options); err != nil {
		return nil, err
	}
	if err := mountAuth(mux, invoker, authv1connect.AuthServiceStartRecoveryCodesProcedure, contractv1.RouteId_ROUTE_ID_AUTH_START_RECOVERY_CODES,
		func(request *authv1.StartRecoveryCodesRequest, browser *authv1.BrowserContext) proto.Message {
			return &authv1.BrowserStartRecoveryCodesRequest{Request: request, Context: browser}
		},
		func(payload []byte) (*authv1.StartRecoveryCodesResponse, []string, authv1.AuthBrowserFailure, error) {
			response := new(authv1.BrowserStartRecoveryCodesResponse)
			err := proto.Unmarshal(payload, response)
			return response.GetResponse(), response.GetSetCookie(), response.GetFailure(), err
		}, options); err != nil {
		return nil, err
	}
	if err := mountAuth(mux, invoker, authv1connect.AuthServiceCompleteLoginCodeChangeProcedure, contractv1.RouteId_ROUTE_ID_AUTH_COMPLETE_LOGIN_CODE_CHANGE,
		func(request *authv1.CompleteLoginCodeChangeRequest, browser *authv1.BrowserContext) proto.Message {
			return &authv1.BrowserCompleteLoginCodeChangeRequest{Request: request, Context: browser}
		},
		func(payload []byte) (*authv1.CompleteLoginCodeChangeResponse, []string, authv1.AuthBrowserFailure, error) {
			response := new(authv1.BrowserCompleteLoginCodeChangeResponse)
			err := proto.Unmarshal(payload, response)
			return response.GetResponse(), response.GetSetCookie(), response.GetFailure(), err
		}, options); err != nil {
		return nil, err
	}
	if err := mountAuth(mux, invoker, authv1connect.AuthServiceCompleteRecoveryCodesProcedure, contractv1.RouteId_ROUTE_ID_AUTH_COMPLETE_RECOVERY_CODES,
		func(request *authv1.CompleteRecoveryCodesRequest, browser *authv1.BrowserContext) proto.Message {
			return &authv1.BrowserCompleteRecoveryCodesRequest{Request: request, Context: browser}
		},
		func(payload []byte) (*authv1.CompleteRecoveryCodesResponse, []string, authv1.AuthBrowserFailure, error) {
			response := new(authv1.BrowserCompleteRecoveryCodesResponse)
			err := proto.Unmarshal(payload, response)
			return response.GetResponse(), response.GetSetCookie(), response.GetFailure(), err
		}, options); err != nil {
		return nil, err
	}
	if err := mountAuth(mux, invoker, authv1connect.AuthServiceChangePasswordProcedure, contractv1.RouteId_ROUTE_ID_AUTH_CHANGE_PASSWORD,
		func(request *authv1.ChangePasswordRequest, browser *authv1.BrowserContext) proto.Message {
			return &authv1.BrowserChangePasswordRequest{Request: request, Context: browser}
		},
		func(payload []byte) (*authv1.ChangePasswordResponse, []string, authv1.AuthBrowserFailure, error) {
			response := new(authv1.BrowserChangePasswordResponse)
			err := proto.Unmarshal(payload, response)
			return response.GetResponse(), response.GetSetCookie(), response.GetFailure(), err
		}, options); err != nil {
		return nil, err
	}
	if err := mountAuth(mux, invoker, authv1connect.AuthServiceStartLoginProcedure, contractv1.RouteId_ROUTE_ID_AUTH_START_LOGIN,
		func(request *authv1.StartLoginRequest, browser *authv1.BrowserContext) proto.Message {
			return &authv1.BrowserStartLoginRequest{Request: request, Context: browser}
		},
		func(payload []byte) (*authv1.StartLoginResponse, []string, authv1.AuthBrowserFailure, error) {
			response := new(authv1.BrowserStartLoginResponse)
			err := proto.Unmarshal(payload, response)
			return response.GetResponse(), response.GetSetCookie(), response.GetFailure(), err
		}, options); err != nil {
		return nil, err
	}
	if err := mountAuth(mux, invoker, authv1connect.AuthServiceCompleteLoginProcedure, contractv1.RouteId_ROUTE_ID_AUTH_COMPLETE_LOGIN,
		func(request *authv1.CompleteLoginRequest, browser *authv1.BrowserContext) proto.Message {
			return &authv1.BrowserCompleteLoginRequest{Request: request, Context: browser}
		},
		func(payload []byte) (*authv1.CompleteLoginResponse, []string, authv1.AuthBrowserFailure, error) {
			response := new(authv1.BrowserCompleteLoginResponse)
			err := proto.Unmarshal(payload, response)
			return response.GetResponse(), response.GetSetCookie(), response.GetFailure(), err
		}, options); err != nil {
		return nil, err
	}
	if err := mountAuth(mux, invoker, authv1connect.AuthServiceResendLoginCodeProcedure, contractv1.RouteId_ROUTE_ID_AUTH_RESEND_LOGIN_CODE,
		func(request *authv1.ResendLoginCodeRequest, browser *authv1.BrowserContext) proto.Message {
			return &authv1.BrowserResendLoginCodeRequest{Request: request, Context: browser}
		},
		func(payload []byte) (*authv1.ResendLoginCodeResponse, []string, authv1.AuthBrowserFailure, error) {
			response := new(authv1.BrowserResendLoginCodeResponse)
			err := proto.Unmarshal(payload, response)
			return response.GetResponse(), response.GetSetCookie(), response.GetFailure(), err
		}, options); err != nil {
		return nil, err
	}
	if err := mountAuth(mux, invoker, authv1connect.AuthServiceRequestEmailVerificationProcedure, contractv1.RouteId_ROUTE_ID_AUTH_REQUEST_EMAIL_VERIFICATION,
		func(request *authv1.RequestEmailVerificationRequest, browser *authv1.BrowserContext) proto.Message {
			return &authv1.BrowserRequestEmailVerificationRequest{Request: request, Context: browser}
		},
		func(payload []byte) (*authv1.RequestEmailVerificationResponse, []string, authv1.AuthBrowserFailure, error) {
			response := new(authv1.BrowserRequestEmailVerificationResponse)
			err := proto.Unmarshal(payload, response)
			return response.GetResponse(), response.GetSetCookie(), response.GetFailure(), err
		}, options); err != nil {
		return nil, err
	}
	if err := mountAuth(mux, invoker, authv1connect.AuthServiceConfirmEmailProcedure, contractv1.RouteId_ROUTE_ID_AUTH_CONFIRM_EMAIL,
		func(request *authv1.ConfirmEmailRequest, browser *authv1.BrowserContext) proto.Message {
			return &authv1.BrowserConfirmEmailRequest{Request: request, Context: browser}
		},
		func(payload []byte) (*authv1.ConfirmEmailResponse, []string, authv1.AuthBrowserFailure, error) {
			response := new(authv1.BrowserConfirmEmailResponse)
			err := proto.Unmarshal(payload, response)
			return response.GetResponse(), response.GetSetCookie(), response.GetFailure(), err
		}, options); err != nil {
		return nil, err
	}
	if err := mountAuth(mux, invoker, authv1connect.AuthServiceRequestPasswordResetProcedure, contractv1.RouteId_ROUTE_ID_AUTH_REQUEST_PASSWORD_RESET,
		func(request *authv1.RequestPasswordResetRequest, browser *authv1.BrowserContext) proto.Message {
			return &authv1.BrowserRequestPasswordResetRequest{Request: request, Context: browser}
		},
		func(payload []byte) (*authv1.RequestPasswordResetResponse, []string, authv1.AuthBrowserFailure, error) {
			response := new(authv1.BrowserRequestPasswordResetResponse)
			err := proto.Unmarshal(payload, response)
			return response.GetResponse(), response.GetSetCookie(), response.GetFailure(), err
		}, options); err != nil {
		return nil, err
	}
	if err := mountAuth(mux, invoker, authv1connect.AuthServiceConfirmPasswordResetProcedure, contractv1.RouteId_ROUTE_ID_AUTH_CONFIRM_PASSWORD_RESET,
		func(request *authv1.ConfirmPasswordResetRequest, browser *authv1.BrowserContext) proto.Message {
			return &authv1.BrowserConfirmPasswordResetRequest{Request: request, Context: browser}
		},
		func(payload []byte) (*authv1.ConfirmPasswordResetResponse, []string, authv1.AuthBrowserFailure, error) {
			response := new(authv1.BrowserConfirmPasswordResetResponse)
			err := proto.Unmarshal(payload, response)
			return response.GetResponse(), response.GetSetCookie(), response.GetFailure(), err
		}, options); err != nil {
		return nil, err
	}
	if err := mountAuth(mux, invoker, authv1connect.AuthServiceGetCredentialsProcedure, contractv1.RouteId_ROUTE_ID_AUTH_GET_CREDENTIALS,
		func(request *authv1.GetCredentialsRequest, browser *authv1.BrowserContext) proto.Message {
			return &authv1.BrowserGetCredentialsRequest{Request: request, Context: browser}
		},
		func(payload []byte) (*authv1.GetCredentialsResponse, []string, authv1.AuthBrowserFailure, error) {
			response := new(authv1.BrowserGetCredentialsResponse)
			err := proto.Unmarshal(payload, response)
			return response.GetResponse(), response.GetSetCookie(), response.GetFailure(), err
		}, options); err != nil {
		return nil, err
	}
	if err := mountAuth(mux, invoker, authv1connect.AuthServiceStartEmailChangeProcedure, contractv1.RouteId_ROUTE_ID_AUTH_START_EMAIL_CHANGE,
		func(request *authv1.StartEmailChangeRequest, browser *authv1.BrowserContext) proto.Message {
			return &authv1.BrowserStartEmailChangeRequest{Request: request, Context: browser}
		},
		func(payload []byte) (*authv1.StartEmailChangeResponse, []string, authv1.AuthBrowserFailure, error) {
			response := new(authv1.BrowserStartEmailChangeResponse)
			err := proto.Unmarshal(payload, response)
			return response.GetResponse(), response.GetSetCookie(), response.GetFailure(), err
		}, options); err != nil {
		return nil, err
	}
	if err := mountAuth(mux, invoker, authv1connect.AuthServiceConfirmEmailChangeProcedure, contractv1.RouteId_ROUTE_ID_AUTH_CONFIRM_EMAIL_CHANGE,
		func(request *authv1.ConfirmEmailChangeRequest, browser *authv1.BrowserContext) proto.Message {
			return &authv1.BrowserConfirmEmailChangeRequest{Request: request, Context: browser}
		},
		func(payload []byte) (*authv1.ConfirmEmailChangeResponse, []string, authv1.AuthBrowserFailure, error) {
			response := new(authv1.BrowserConfirmEmailChangeResponse)
			err := proto.Unmarshal(payload, response)
			return response.GetResponse(), response.GetSetCookie(), response.GetFailure(), err
		}, options); err != nil {
		return nil, err
	}
	if err := mountAuth(mux, invoker, authv1connect.AuthServiceCancelEmailChangeProcedure, contractv1.RouteId_ROUTE_ID_AUTH_CANCEL_EMAIL_CHANGE,
		func(request *authv1.CancelEmailChangeRequest, browser *authv1.BrowserContext) proto.Message {
			return &authv1.BrowserCancelEmailChangeRequest{Request: request, Context: browser}
		},
		func(payload []byte) (*authv1.CancelEmailChangeResponse, []string, authv1.AuthBrowserFailure, error) {
			response := new(authv1.BrowserCancelEmailChangeResponse)
			err := proto.Unmarshal(payload, response)
			return response.GetResponse(), response.GetSetCookie(), response.GetFailure(), err
		}, options); err != nil {
		return nil, err
	}
	if err := mountAuth(mux, invoker, authv1connect.AuthServiceListSessionsProcedure, contractv1.RouteId_ROUTE_ID_AUTH_LIST_SESSIONS,
		func(request *authv1.ListSessionsRequest, browser *authv1.BrowserContext) proto.Message {
			return &authv1.BrowserListSessionsRequest{Request: request, Context: browser}
		},
		func(payload []byte) (*authv1.ListSessionsResponse, []string, authv1.AuthBrowserFailure, error) {
			response := new(authv1.BrowserListSessionsResponse)
			err := proto.Unmarshal(payload, response)
			return response.GetResponse(), response.GetSetCookie(), response.GetFailure(), err
		}, options); err != nil {
		return nil, err
	}
	if err := mountAuth(mux, invoker, authv1connect.AuthServiceRevokeSessionProcedure, contractv1.RouteId_ROUTE_ID_AUTH_REVOKE_OWNED_SESSION,
		func(request *authv1.RevokeSessionRequest, browser *authv1.BrowserContext) proto.Message {
			return &authv1.BrowserRevokeSessionRequest{Request: request, Context: browser}
		},
		func(payload []byte) (*authv1.RevokeSessionResponse, []string, authv1.AuthBrowserFailure, error) {
			response := new(authv1.BrowserRevokeSessionResponse)
			err := proto.Unmarshal(payload, response)
			return response.GetResponse(), response.GetSetCookie(), response.GetFailure(), err
		}, options); err != nil {
		return nil, err
	}
	if err := mountAuth(mux, invoker, authv1connect.AuthServiceRequestAccountDeletionProcedure, contractv1.RouteId_ROUTE_ID_AUTH_REQUEST_ACCOUNT_DELETION,
		func(request *authv1.RequestAccountDeletionRequest, browser *authv1.BrowserContext) proto.Message {
			return &authv1.BrowserRequestAccountDeletionRequest{Request: request, Context: browser}
		},
		func(payload []byte) (*authv1.RequestAccountDeletionResponse, []string, authv1.AuthBrowserFailure, error) {
			response := new(authv1.BrowserRequestAccountDeletionResponse)
			err := proto.Unmarshal(payload, response)
			return response.GetResponse(), response.GetSetCookie(), response.GetFailure(), err
		}, options); err != nil {
		return nil, err
	}
	if err := mountAuth(mux, invoker, authv1connect.AuthServiceCancelAccountDeletionProcedure, contractv1.RouteId_ROUTE_ID_AUTH_CANCEL_ACCOUNT_DELETION,
		func(request *authv1.CancelAccountDeletionRequest, browser *authv1.BrowserContext) proto.Message {
			return &authv1.BrowserCancelAccountDeletionRequest{Request: request, Context: browser}
		},
		func(payload []byte) (*authv1.CancelAccountDeletionResponse, []string, authv1.AuthBrowserFailure, error) {
			response := new(authv1.BrowserCancelAccountDeletionResponse)
			err := proto.Unmarshal(payload, response)
			return response.GetResponse(), response.GetSetCookie(), response.GetFailure(), err
		}, options); err != nil {
		return nil, err
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.TLS == nil {
			http.Error(w, "HTTPS required", http.StatusForbidden)
			return
		}
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", http.MethodPost)
			http.Error(w, "POST required", http.StatusMethodNotAllowed)
			return
		}
		mux.ServeHTTP(w, r)
	}), nil
}

func mountAuth[Request, Response any](mux *http.ServeMux, invoker Invoker, procedure string, route contractv1.RouteId,
	wrap func(*Request, *authv1.BrowserContext) proto.Message,
	unwrap func([]byte) (*Response, []string, authv1.AuthBrowserFailure, error), options []connect.HandlerOption) error {
	policy, allowed := invoker.RoutePolicy(route)
	if !allowed || policy.MaxRequestBytes == 0 || policy.MaxResponseBytes == 0 {
		return errors.New("connect auth bridge: bounded route policy is required")
	}
	opts := append(slices.Clone(options), connect.WithReadMaxBytes(int(policy.MaxRequestBytes)))
	handler := connect.NewUnaryHandler(procedure, func(ctx context.Context, request *connect.Request[Request]) (*connect.Response[Response], error) {
		if _, err := requestIdempotencyKey(request.Header(), false); err != nil {
			return nil, publicError(connect.CodeInvalidArgument)
		}
		browser, err := browserContext(request.Header())
		if err != nil {
			return nil, publicError(connect.CodeInvalidArgument)
		}
		payload, err := proto.Marshal(wrap(request.Msg, browser))
		if err != nil {
			return nil, publicError(connect.CodeInvalidArgument)
		}
		if uint64(len(payload)) > uint64(policy.MaxRequestBytes) {
			return nil, publicError(connect.CodeResourceExhausted)
		}
		result, err := invoker.Invoke(ctx, tunnel.Call{Route: route, Payload: payload})
		if err != nil {
			return nil, mapTunnelError(err)
		}
		if uint64(len(result.Payload)) > uint64(policy.MaxResponseBytes) {
			return nil, publicError(connect.CodeResourceExhausted)
		}
		message, cookies, failure, err := unwrap(result.Payload)
		if err != nil || !validBrowserValues(cookies, 8192) || (failure != 0 && (message != nil || len(cookies) != 0)) {
			return nil, publicError(connect.CodeInternal)
		}
		if failure != 0 {
			return nil, authFailure(failure)
		}
		if message == nil {
			return nil, publicError(connect.CodeInternal)
		}
		response := connect.NewResponse(message)
		for _, cookie := range cookies {
			response.Header().Add("Set-Cookie", cookie)
		}
		return response, nil
	}, opts...)
	mux.Handle(procedure, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, int64(policy.MaxRequestBytes))
		handler.ServeHTTP(w, r)
	}))
	return nil
}

func browserContext(header http.Header) (*authv1.BrowserContext, error) {
	browser := &authv1.BrowserContext{
		Cookie:       slices.Clone(header.Values("Cookie")),
		Origin:       slices.Clone(header.Values("Origin")),
		SecFetchSite: slices.Clone(header.Values("Sec-Fetch-Site")),
	}
	if !validBrowserValues(browser.Cookie, 8192) || !validBrowserValues(browser.Origin, 2048) || !validBrowserValues(browser.SecFetchSite, 256) {
		return nil, errors.New("connect auth bridge: invalid browser context")
	}
	return browser, nil
}

func validBrowserValues(values []string, maxBytes int) bool {
	if len(values) > 16 {
		return false
	}
	size := 0
	for _, value := range values {
		size += len(value)
		if size > maxBytes || strings.ContainsAny(value, "\r\n\x00") {
			return false
		}
	}
	return true
}
