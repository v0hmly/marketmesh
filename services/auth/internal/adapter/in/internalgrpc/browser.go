package internalgrpc

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"connectrpc.com/connect"
	authv1 "github.com/v0hmly/marketmesh/api/gen/go/auth/v1"
	"github.com/v0hmly/marketmesh/platform/workloadid"
	public "github.com/v0hmly/marketmesh/services/auth/internal/adapter/in/connectrpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// BrowserHandler delegates private browser requests to the standalone Auth handler.
type BrowserHandler struct {
	authv1.UnimplementedAuthBrowserServiceServer
	public *public.Handler
	policy *workloadid.Policy
}

// NewBrowser creates a bridge with the listener's shared workload authorization policy.
func NewBrowser(handler *public.Handler, policy *workloadid.Policy) (*BrowserHandler, error) {
	if handler == nil || policy == nil {
		return nil, errors.New("auth browser: missing dependencies")
	}
	return &BrowserHandler{public: handler, policy: policy}, nil
}

func browserMethod(name string) string { return "/auth.v1.AuthBrowserService/Browser" + name }
func browserMethods() []string {
	return []string{
		browserMethod("RegisterCredentials"),
		browserMethod("Login"),
		browserMethod("RefreshSession"),
		browserMethod("Logout"),
		browserMethod("LogoutAll"),
		browserMethod("StartLoginCodeChange"),
		browserMethod("CompleteLoginCodeChange"),
		browserMethod("ChangePassword"),
		browserMethod("StartLogin"),
		browserMethod("CompleteLogin"),
		browserMethod("ResendLoginCode"),
		browserMethod("RequestEmailVerification"),
		browserMethod("ConfirmEmail"),
		browserMethod("RequestPasswordReset"),
		browserMethod("ConfirmPasswordReset"),
		browserMethod("GetCredentials"),
		browserMethod("StartEmailChange"),
		browserMethod("ConfirmEmailChange"),
		browserMethod("CancelEmailChange"),
		browserMethod("ListSessions"),
		browserMethod("RevokeSession"),
		browserMethod("RequestAccountDeletion"),
		browserMethod("CancelAccountDeletion"),
	}
}
func (h *BrowserHandler) authorize(ctx context.Context, method string) error {
	identity, _, err := workloadid.FromContext(ctx)
	if err != nil || identity.Role != "gateway-out" || !h.policy.Allow(identity, browserMethod(method)) {
		return denied()
	}
	return nil
}

func browserHeader(value *authv1.BrowserContext) (http.Header, error) {
	if value == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid browser context")
	}
	header := make(http.Header)
	for _, field := range []struct {
		name   string
		values []string
		limit  int
	}{
		{"Cookie", value.GetCookie(), 8192}, {"Origin", value.GetOrigin(), 2048}, {"Sec-Fetch-Site", value.GetSecFetchSite(), 256},
	} {
		total := 0
		if len(field.values) > 16 {
			return nil, status.Error(codes.InvalidArgument, "invalid browser context")
		}
		for _, line := range field.values {
			total += len(line)
			if total > field.limit || strings.ContainsAny(line, "\r\n\x00") {
				return nil, status.Error(codes.InvalidArgument, "invalid browser context")
			}
			header.Add(field.name, line)
		}
	}
	return header, nil
}

func browserFailure(err error) error {
	switch connect.CodeOf(err) {
	case connect.CodeInvalidArgument:
		return status.Error(codes.InvalidArgument, "invalid credential input")
	case connect.CodeUnauthenticated:
		return denied()
	case connect.CodeUnimplemented:
		return status.Error(codes.Unimplemented, "operation unavailable")
	case connect.CodeUnavailable:
		return unavailable()
	case connect.CodeCanceled:
		return status.Error(codes.Canceled, "request canceled")
	case connect.CodeDeadlineExceeded:
		return status.Error(codes.DeadlineExceeded, "request deadline exceeded")
	default:
		return status.Error(codes.Internal, "internal error")
	}
}

// BrowserRegisterCredentials preserves Auth's browser security checks and response cookie lines.
func (h *BrowserHandler) BrowserRegisterCredentials(ctx context.Context, request *authv1.BrowserRegisterCredentialsRequest) (*authv1.BrowserRegisterCredentialsResponse, error) {
	if err := h.authorize(ctx, "RegisterCredentials"); err != nil {
		return nil, err
	}
	if request == nil || request.GetRequest() == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	header, err := browserHeader(request.GetContext())
	if err != nil {
		return nil, err
	}
	inner := connect.NewRequest(request.GetRequest())
	for name, values := range header {
		inner.Header()[name] = values
	}
	response, err := h.public.RegisterCredentials(ctx, inner)
	if err != nil {
		if failure := browserAuthFailure(err); failure != 0 {
			return &authv1.BrowserRegisterCredentialsResponse{Failure: failure}, nil
		}
		return nil, browserFailure(err)
	}
	return &authv1.BrowserRegisterCredentialsResponse{Response: response.Msg, SetCookie: append([]string(nil), response.Header().Values("Set-Cookie")...)}, nil
}

// BrowserLogin preserves Auth's browser security checks and response cookie lines.
func (h *BrowserHandler) BrowserLogin(ctx context.Context, request *authv1.BrowserLoginRequest) (*authv1.BrowserLoginResponse, error) {
	if err := h.authorize(ctx, "Login"); err != nil {
		return nil, err
	}
	if request == nil || request.GetRequest() == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	header, err := browserHeader(request.GetContext())
	if err != nil {
		return nil, err
	}
	inner := connect.NewRequest(request.GetRequest())
	for name, values := range header {
		inner.Header()[name] = values
	}
	response, err := h.public.Login(ctx, inner)
	if err != nil {
		if failure := browserAuthFailure(err); failure != 0 {
			return &authv1.BrowserLoginResponse{Failure: failure}, nil
		}
		return nil, browserFailure(err)
	}
	return &authv1.BrowserLoginResponse{Response: response.Msg, SetCookie: append([]string(nil), response.Header().Values("Set-Cookie")...)}, nil
}

// BrowserRefreshSession preserves Auth's browser security checks and response cookie lines.
func (h *BrowserHandler) BrowserRefreshSession(ctx context.Context, request *authv1.BrowserRefreshSessionRequest) (*authv1.BrowserRefreshSessionResponse, error) {
	if err := h.authorize(ctx, "RefreshSession"); err != nil {
		return nil, err
	}
	if request == nil || request.GetRequest() == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	header, err := browserHeader(request.GetContext())
	if err != nil {
		return nil, err
	}
	inner := connect.NewRequest(request.GetRequest())
	for name, values := range header {
		inner.Header()[name] = values
	}
	response, err := h.public.RefreshSession(ctx, inner)
	if err != nil {
		if failure := browserAuthFailure(err); failure != 0 {
			return &authv1.BrowserRefreshSessionResponse{Failure: failure}, nil
		}
		return nil, browserFailure(err)
	}
	return &authv1.BrowserRefreshSessionResponse{Response: response.Msg, SetCookie: append([]string(nil), response.Header().Values("Set-Cookie")...)}, nil
}

// BrowserLogout preserves Auth's browser security checks and response cookie lines.
func (h *BrowserHandler) BrowserLogout(ctx context.Context, request *authv1.BrowserLogoutRequest) (*authv1.BrowserLogoutResponse, error) {
	if err := h.authorize(ctx, "Logout"); err != nil {
		return nil, err
	}
	if request == nil || request.GetRequest() == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	header, err := browserHeader(request.GetContext())
	if err != nil {
		return nil, err
	}
	inner := connect.NewRequest(request.GetRequest())
	for name, values := range header {
		inner.Header()[name] = values
	}
	response, err := h.public.Logout(ctx, inner)
	if err != nil {
		if failure := browserAuthFailure(err); failure != 0 {
			return &authv1.BrowserLogoutResponse{Failure: failure}, nil
		}
		return nil, browserFailure(err)
	}
	return &authv1.BrowserLogoutResponse{Response: response.Msg, SetCookie: append([]string(nil), response.Header().Values("Set-Cookie")...)}, nil
}

// BrowserLogoutAll preserves Auth's browser security checks and response cookie lines.
func (h *BrowserHandler) BrowserLogoutAll(ctx context.Context, request *authv1.BrowserLogoutAllRequest) (*authv1.BrowserLogoutAllResponse, error) {
	if err := h.authorize(ctx, "LogoutAll"); err != nil {
		return nil, err
	}
	if request == nil || request.GetRequest() == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	header, err := browserHeader(request.GetContext())
	if err != nil {
		return nil, err
	}
	inner := connect.NewRequest(request.GetRequest())
	for name, values := range header {
		inner.Header()[name] = values
	}
	response, err := h.public.LogoutAll(ctx, inner)
	if err != nil {
		if failure := browserAuthFailure(err); failure != 0 {
			return &authv1.BrowserLogoutAllResponse{Failure: failure}, nil
		}
		return nil, browserFailure(err)
	}
	return &authv1.BrowserLogoutAllResponse{Response: response.Msg, SetCookie: append([]string(nil), response.Header().Values("Set-Cookie")...)}, nil
}

// BrowserStartLoginCodeChange preserves Auth's browser security checks and response cookie lines.
func (h *BrowserHandler) BrowserStartLoginCodeChange(ctx context.Context, request *authv1.BrowserStartLoginCodeChangeRequest) (*authv1.BrowserStartLoginCodeChangeResponse, error) {
	if err := h.authorize(ctx, "StartLoginCodeChange"); err != nil {
		return nil, err
	}
	if request == nil || request.GetRequest() == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	header, err := browserHeader(request.GetContext())
	if err != nil {
		return nil, err
	}
	inner := connect.NewRequest(request.GetRequest())
	for name, values := range header {
		inner.Header()[name] = values
	}
	response, err := h.public.StartLoginCodeChange(ctx, inner)
	if err != nil {
		if failure := browserAuthFailure(err); failure != 0 {
			return &authv1.BrowserStartLoginCodeChangeResponse{Failure: failure}, nil
		}
		return nil, browserFailure(err)
	}
	return &authv1.BrowserStartLoginCodeChangeResponse{Response: response.Msg, SetCookie: append([]string(nil), response.Header().Values("Set-Cookie")...)}, nil
}

// BrowserCompleteLoginCodeChange preserves Auth's browser security checks and response cookie lines.
func (h *BrowserHandler) BrowserCompleteLoginCodeChange(ctx context.Context, request *authv1.BrowserCompleteLoginCodeChangeRequest) (*authv1.BrowserCompleteLoginCodeChangeResponse, error) {
	if err := h.authorize(ctx, "CompleteLoginCodeChange"); err != nil {
		return nil, err
	}
	if request == nil || request.GetRequest() == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	header, err := browserHeader(request.GetContext())
	if err != nil {
		return nil, err
	}
	inner := connect.NewRequest(request.GetRequest())
	for name, values := range header {
		inner.Header()[name] = values
	}
	response, err := h.public.CompleteLoginCodeChange(ctx, inner)
	if err != nil {
		if failure := browserAuthFailure(err); failure != 0 {
			return &authv1.BrowserCompleteLoginCodeChangeResponse{Failure: failure}, nil
		}
		return nil, browserFailure(err)
	}
	return &authv1.BrowserCompleteLoginCodeChangeResponse{Response: response.Msg, SetCookie: append([]string(nil), response.Header().Values("Set-Cookie")...)}, nil
}

// BrowserChangePassword preserves Auth's browser security checks and response cookie lines.
func (h *BrowserHandler) BrowserChangePassword(ctx context.Context, request *authv1.BrowserChangePasswordRequest) (*authv1.BrowserChangePasswordResponse, error) {
	if err := h.authorize(ctx, "ChangePassword"); err != nil {
		return nil, err
	}
	if request == nil || request.GetRequest() == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	header, err := browserHeader(request.GetContext())
	if err != nil {
		return nil, err
	}
	inner := connect.NewRequest(request.GetRequest())
	for name, values := range header {
		inner.Header()[name] = values
	}
	response, err := h.public.ChangePassword(ctx, inner)
	if err != nil {
		if failure := browserAuthFailure(err); failure != 0 {
			return &authv1.BrowserChangePasswordResponse{Failure: failure}, nil
		}
		return nil, browserFailure(err)
	}
	return &authv1.BrowserChangePasswordResponse{Response: response.Msg, SetCookie: append([]string(nil), response.Header().Values("Set-Cookie")...)}, nil
}

// BrowserStartLogin preserves Auth's browser security checks and response cookie lines.
func (h *BrowserHandler) BrowserStartLogin(ctx context.Context, request *authv1.BrowserStartLoginRequest) (*authv1.BrowserStartLoginResponse, error) {
	if err := h.authorize(ctx, "StartLogin"); err != nil {
		return nil, err
	}
	if request == nil || request.GetRequest() == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	header, err := browserHeader(request.GetContext())
	if err != nil {
		return nil, err
	}
	inner := connect.NewRequest(request.GetRequest())
	for name, values := range header {
		inner.Header()[name] = values
	}
	response, err := h.public.StartLogin(ctx, inner)
	if err != nil {
		if failure := browserAuthFailure(err); failure != 0 {
			return &authv1.BrowserStartLoginResponse{Failure: failure}, nil
		}
		return nil, browserFailure(err)
	}
	return &authv1.BrowserStartLoginResponse{Response: response.Msg, SetCookie: append([]string(nil), response.Header().Values("Set-Cookie")...)}, nil
}

// BrowserCompleteLogin preserves Auth's browser security checks and response cookie lines.
func (h *BrowserHandler) BrowserCompleteLogin(ctx context.Context, request *authv1.BrowserCompleteLoginRequest) (*authv1.BrowserCompleteLoginResponse, error) {
	if err := h.authorize(ctx, "CompleteLogin"); err != nil {
		return nil, err
	}
	if request == nil || request.GetRequest() == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	header, err := browserHeader(request.GetContext())
	if err != nil {
		return nil, err
	}
	inner := connect.NewRequest(request.GetRequest())
	for name, values := range header {
		inner.Header()[name] = values
	}
	response, err := h.public.CompleteLogin(ctx, inner)
	if err != nil {
		if failure := browserAuthFailure(err); failure != 0 {
			return &authv1.BrowserCompleteLoginResponse{Failure: failure}, nil
		}
		return nil, browserFailure(err)
	}
	return &authv1.BrowserCompleteLoginResponse{Response: response.Msg, SetCookie: append([]string(nil), response.Header().Values("Set-Cookie")...)}, nil
}

// BrowserResendLoginCode preserves Auth's browser security checks and response cookie lines.
func (h *BrowserHandler) BrowserResendLoginCode(ctx context.Context, request *authv1.BrowserResendLoginCodeRequest) (*authv1.BrowserResendLoginCodeResponse, error) {
	if err := h.authorize(ctx, "ResendLoginCode"); err != nil {
		return nil, err
	}
	if request == nil || request.GetRequest() == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	header, err := browserHeader(request.GetContext())
	if err != nil {
		return nil, err
	}
	inner := connect.NewRequest(request.GetRequest())
	for name, values := range header {
		inner.Header()[name] = values
	}
	response, err := h.public.ResendLoginCode(ctx, inner)
	if err != nil {
		if failure := browserAuthFailure(err); failure != 0 {
			return &authv1.BrowserResendLoginCodeResponse{Failure: failure}, nil
		}
		return nil, browserFailure(err)
	}
	return &authv1.BrowserResendLoginCodeResponse{Response: response.Msg, SetCookie: append([]string(nil), response.Header().Values("Set-Cookie")...)}, nil
}

// BrowserRequestEmailVerification preserves Auth's browser security checks and response cookie lines.
func (h *BrowserHandler) BrowserRequestEmailVerification(ctx context.Context, request *authv1.BrowserRequestEmailVerificationRequest) (*authv1.BrowserRequestEmailVerificationResponse, error) {
	if err := h.authorize(ctx, "RequestEmailVerification"); err != nil {
		return nil, err
	}
	if request == nil || request.GetRequest() == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	header, err := browserHeader(request.GetContext())
	if err != nil {
		return nil, err
	}
	inner := connect.NewRequest(request.GetRequest())
	for name, values := range header {
		inner.Header()[name] = values
	}
	response, err := h.public.RequestEmailVerification(ctx, inner)
	if err != nil {
		if failure := browserAuthFailure(err); failure != 0 {
			return &authv1.BrowserRequestEmailVerificationResponse{Failure: failure}, nil
		}
		return nil, browserFailure(err)
	}
	return &authv1.BrowserRequestEmailVerificationResponse{Response: response.Msg, SetCookie: append([]string(nil), response.Header().Values("Set-Cookie")...)}, nil
}

// BrowserConfirmEmail preserves Auth's browser security checks and response cookie lines.
func (h *BrowserHandler) BrowserConfirmEmail(ctx context.Context, request *authv1.BrowserConfirmEmailRequest) (*authv1.BrowserConfirmEmailResponse, error) {
	if err := h.authorize(ctx, "ConfirmEmail"); err != nil {
		return nil, err
	}
	if request == nil || request.GetRequest() == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	header, err := browserHeader(request.GetContext())
	if err != nil {
		return nil, err
	}
	inner := connect.NewRequest(request.GetRequest())
	for name, values := range header {
		inner.Header()[name] = values
	}
	response, err := h.public.ConfirmEmail(ctx, inner)
	if err != nil {
		if failure := browserAuthFailure(err); failure != 0 {
			return &authv1.BrowserConfirmEmailResponse{Failure: failure}, nil
		}
		return nil, browserFailure(err)
	}
	return &authv1.BrowserConfirmEmailResponse{Response: response.Msg, SetCookie: append([]string(nil), response.Header().Values("Set-Cookie")...)}, nil
}

// BrowserRequestPasswordReset preserves Auth's browser security checks and response cookie lines.
func (h *BrowserHandler) BrowserRequestPasswordReset(ctx context.Context, request *authv1.BrowserRequestPasswordResetRequest) (*authv1.BrowserRequestPasswordResetResponse, error) {
	if err := h.authorize(ctx, "RequestPasswordReset"); err != nil {
		return nil, err
	}
	if request == nil || request.GetRequest() == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	header, err := browserHeader(request.GetContext())
	if err != nil {
		return nil, err
	}
	inner := connect.NewRequest(request.GetRequest())
	for name, values := range header {
		inner.Header()[name] = values
	}
	response, err := h.public.RequestPasswordReset(ctx, inner)
	if err != nil {
		if failure := browserAuthFailure(err); failure != 0 {
			return &authv1.BrowserRequestPasswordResetResponse{Failure: failure}, nil
		}
		return nil, browserFailure(err)
	}
	return &authv1.BrowserRequestPasswordResetResponse{Response: response.Msg, SetCookie: append([]string(nil), response.Header().Values("Set-Cookie")...)}, nil
}

// BrowserConfirmPasswordReset preserves Auth's browser security checks and response cookie lines.
func (h *BrowserHandler) BrowserConfirmPasswordReset(ctx context.Context, request *authv1.BrowserConfirmPasswordResetRequest) (*authv1.BrowserConfirmPasswordResetResponse, error) {
	if err := h.authorize(ctx, "ConfirmPasswordReset"); err != nil {
		return nil, err
	}
	if request == nil || request.GetRequest() == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	header, err := browserHeader(request.GetContext())
	if err != nil {
		return nil, err
	}
	inner := connect.NewRequest(request.GetRequest())
	for name, values := range header {
		inner.Header()[name] = values
	}
	response, err := h.public.ConfirmPasswordReset(ctx, inner)
	if err != nil {
		if failure := browserAuthFailure(err); failure != 0 {
			return &authv1.BrowserConfirmPasswordResetResponse{Failure: failure}, nil
		}
		return nil, browserFailure(err)
	}
	return &authv1.BrowserConfirmPasswordResetResponse{Response: response.Msg, SetCookie: append([]string(nil), response.Header().Values("Set-Cookie")...)}, nil
}

// BrowserGetCredentials preserves Auth's browser security checks and response cookie lines.
func (h *BrowserHandler) BrowserGetCredentials(ctx context.Context, request *authv1.BrowserGetCredentialsRequest) (*authv1.BrowserGetCredentialsResponse, error) {
	if err := h.authorize(ctx, "GetCredentials"); err != nil {
		return nil, err
	}
	if request == nil || request.GetRequest() == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	header, err := browserHeader(request.GetContext())
	if err != nil {
		return nil, err
	}
	inner := connect.NewRequest(request.GetRequest())
	for name, values := range header {
		inner.Header()[name] = values
	}
	response, err := h.public.GetCredentials(ctx, inner)
	if err != nil {
		if failure := browserAuthFailure(err); failure != 0 {
			return &authv1.BrowserGetCredentialsResponse{Failure: failure}, nil
		}
		return nil, browserFailure(err)
	}
	return &authv1.BrowserGetCredentialsResponse{Response: response.Msg, SetCookie: append([]string(nil), response.Header().Values("Set-Cookie")...)}, nil
}

// BrowserStartEmailChange preserves Auth's browser security checks and response cookie lines.
func (h *BrowserHandler) BrowserStartEmailChange(ctx context.Context, request *authv1.BrowserStartEmailChangeRequest) (*authv1.BrowserStartEmailChangeResponse, error) {
	if err := h.authorize(ctx, "StartEmailChange"); err != nil {
		return nil, err
	}
	if request == nil || request.GetRequest() == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	header, err := browserHeader(request.GetContext())
	if err != nil {
		return nil, err
	}
	inner := connect.NewRequest(request.GetRequest())
	for name, values := range header {
		inner.Header()[name] = values
	}
	response, err := h.public.StartEmailChange(ctx, inner)
	if err != nil {
		if failure := browserAuthFailure(err); failure != 0 {
			return &authv1.BrowserStartEmailChangeResponse{Failure: failure}, nil
		}
		return nil, browserFailure(err)
	}
	return &authv1.BrowserStartEmailChangeResponse{Response: response.Msg, SetCookie: append([]string(nil), response.Header().Values("Set-Cookie")...)}, nil
}

// BrowserConfirmEmailChange preserves Auth's browser security checks and response cookie lines.
func (h *BrowserHandler) BrowserConfirmEmailChange(ctx context.Context, request *authv1.BrowserConfirmEmailChangeRequest) (*authv1.BrowserConfirmEmailChangeResponse, error) {
	if err := h.authorize(ctx, "ConfirmEmailChange"); err != nil {
		return nil, err
	}
	if request == nil || request.GetRequest() == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	header, err := browserHeader(request.GetContext())
	if err != nil {
		return nil, err
	}
	inner := connect.NewRequest(request.GetRequest())
	for name, values := range header {
		inner.Header()[name] = values
	}
	response, err := h.public.ConfirmEmailChange(ctx, inner)
	if err != nil {
		if failure := browserAuthFailure(err); failure != 0 {
			return &authv1.BrowserConfirmEmailChangeResponse{Failure: failure}, nil
		}
		return nil, browserFailure(err)
	}
	return &authv1.BrowserConfirmEmailChangeResponse{Response: response.Msg, SetCookie: append([]string(nil), response.Header().Values("Set-Cookie")...)}, nil
}

// BrowserCancelEmailChange preserves Auth's browser security checks and response cookie lines.
func (h *BrowserHandler) BrowserCancelEmailChange(ctx context.Context, request *authv1.BrowserCancelEmailChangeRequest) (*authv1.BrowserCancelEmailChangeResponse, error) {
	if err := h.authorize(ctx, "CancelEmailChange"); err != nil {
		return nil, err
	}
	if request == nil || request.GetRequest() == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	header, err := browserHeader(request.GetContext())
	if err != nil {
		return nil, err
	}
	inner := connect.NewRequest(request.GetRequest())
	for name, values := range header {
		inner.Header()[name] = values
	}
	response, err := h.public.CancelEmailChange(ctx, inner)
	if err != nil {
		if failure := browserAuthFailure(err); failure != 0 {
			return &authv1.BrowserCancelEmailChangeResponse{Failure: failure}, nil
		}
		return nil, browserFailure(err)
	}
	return &authv1.BrowserCancelEmailChangeResponse{Response: response.Msg, SetCookie: append([]string(nil), response.Header().Values("Set-Cookie")...)}, nil
}

// BrowserListSessions preserves Auth's browser security checks and response cookie lines.
func (h *BrowserHandler) BrowserListSessions(ctx context.Context, request *authv1.BrowserListSessionsRequest) (*authv1.BrowserListSessionsResponse, error) {
	if err := h.authorize(ctx, "ListSessions"); err != nil {
		return nil, err
	}
	if request == nil || request.GetRequest() == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	header, err := browserHeader(request.GetContext())
	if err != nil {
		return nil, err
	}
	inner := connect.NewRequest(request.GetRequest())
	for name, values := range header {
		inner.Header()[name] = values
	}
	response, err := h.public.ListSessions(ctx, inner)
	if err != nil {
		if failure := browserAuthFailure(err); failure != 0 {
			return &authv1.BrowserListSessionsResponse{Failure: failure}, nil
		}
		return nil, browserFailure(err)
	}
	return &authv1.BrowserListSessionsResponse{Response: response.Msg, SetCookie: append([]string(nil), response.Header().Values("Set-Cookie")...)}, nil
}

// BrowserRevokeSession preserves Auth's browser security checks and response cookie lines.
func (h *BrowserHandler) BrowserRevokeSession(ctx context.Context, request *authv1.BrowserRevokeSessionRequest) (*authv1.BrowserRevokeSessionResponse, error) {
	if err := h.authorize(ctx, "RevokeSession"); err != nil {
		return nil, err
	}
	if request == nil || request.GetRequest() == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	header, err := browserHeader(request.GetContext())
	if err != nil {
		return nil, err
	}
	inner := connect.NewRequest(request.GetRequest())
	for name, values := range header {
		inner.Header()[name] = values
	}
	response, err := h.public.RevokeSession(ctx, inner)
	if err != nil {
		if failure := browserAuthFailure(err); failure != 0 {
			return &authv1.BrowserRevokeSessionResponse{Failure: failure}, nil
		}
		return nil, browserFailure(err)
	}
	return &authv1.BrowserRevokeSessionResponse{Response: response.Msg, SetCookie: append([]string(nil), response.Header().Values("Set-Cookie")...)}, nil
}

// BrowserRequestAccountDeletion preserves Auth's browser security checks and response cookie lines.
func (h *BrowserHandler) BrowserRequestAccountDeletion(ctx context.Context, request *authv1.BrowserRequestAccountDeletionRequest) (*authv1.BrowserRequestAccountDeletionResponse, error) {
	if err := h.authorize(ctx, "RequestAccountDeletion"); err != nil {
		return nil, err
	}
	if request == nil || request.GetRequest() == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	header, err := browserHeader(request.GetContext())
	if err != nil {
		return nil, err
	}
	inner := connect.NewRequest(request.GetRequest())
	for name, values := range header {
		inner.Header()[name] = values
	}
	response, err := h.public.RequestAccountDeletion(ctx, inner)
	if err != nil {
		if failure := browserAuthFailure(err); failure != 0 {
			return &authv1.BrowserRequestAccountDeletionResponse{Failure: failure}, nil
		}
		return nil, browserFailure(err)
	}
	return &authv1.BrowserRequestAccountDeletionResponse{Response: response.Msg, SetCookie: append([]string(nil), response.Header().Values("Set-Cookie")...)}, nil
}

// BrowserCancelAccountDeletion preserves Auth's browser security checks and response cookie lines.
func (h *BrowserHandler) BrowserCancelAccountDeletion(ctx context.Context, request *authv1.BrowserCancelAccountDeletionRequest) (*authv1.BrowserCancelAccountDeletionResponse, error) {
	if err := h.authorize(ctx, "CancelAccountDeletion"); err != nil {
		return nil, err
	}
	if request == nil || request.GetRequest() == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	header, err := browserHeader(request.GetContext())
	if err != nil {
		return nil, err
	}
	inner := connect.NewRequest(request.GetRequest())
	for name, values := range header {
		inner.Header()[name] = values
	}
	response, err := h.public.CancelAccountDeletion(ctx, inner)
	if err != nil {
		if failure := browserAuthFailure(err); failure != 0 {
			return &authv1.BrowserCancelAccountDeletionResponse{Failure: failure}, nil
		}
		return nil, browserFailure(err)
	}
	return &authv1.BrowserCancelAccountDeletionResponse{Response: response.Msg, SetCookie: append([]string(nil), response.Header().Values("Set-Cookie")...)}, nil
}

var _ authv1.AuthBrowserServiceServer = (*BrowserHandler)(nil)
