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
		return nil, browserFailure(err)
	}
	return &authv1.BrowserLogoutAllResponse{Response: response.Msg, SetCookie: append([]string(nil), response.Header().Values("Set-Cookie")...)}, nil
}

var _ authv1.AuthBrowserServiceServer = (*BrowserHandler)(nil)
