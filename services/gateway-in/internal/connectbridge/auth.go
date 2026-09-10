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

// NewAuthHandler exposes only the five fixed public browser Auth procedures.
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
		func(payload []byte) (*authv1.RegisterCredentialsResponse, []string, error) {
			response := new(authv1.BrowserRegisterCredentialsResponse)
			err := proto.Unmarshal(payload, response)
			return response.GetResponse(), response.GetSetCookie(), err
		}, options); err != nil {
		return nil, err
	}
	if err := mountAuth(mux, invoker, authv1connect.AuthServiceLoginProcedure, contractv1.RouteId_ROUTE_ID_AUTH_LOGIN,
		func(request *authv1.LoginRequest, browser *authv1.BrowserContext) proto.Message {
			return &authv1.BrowserLoginRequest{Request: request, Context: browser}
		},
		func(payload []byte) (*authv1.LoginResponse, []string, error) {
			response := new(authv1.BrowserLoginResponse)
			err := proto.Unmarshal(payload, response)
			return response.GetResponse(), response.GetSetCookie(), err
		}, options); err != nil {
		return nil, err
	}
	if err := mountAuth(mux, invoker, authv1connect.AuthServiceRefreshSessionProcedure, contractv1.RouteId_ROUTE_ID_AUTH_REFRESH_SESSION,
		func(request *authv1.RefreshSessionRequest, browser *authv1.BrowserContext) proto.Message {
			return &authv1.BrowserRefreshSessionRequest{Request: request, Context: browser}
		},
		func(payload []byte) (*authv1.RefreshSessionResponse, []string, error) {
			response := new(authv1.BrowserRefreshSessionResponse)
			err := proto.Unmarshal(payload, response)
			return response.GetResponse(), response.GetSetCookie(), err
		}, options); err != nil {
		return nil, err
	}
	if err := mountAuth(mux, invoker, authv1connect.AuthServiceLogoutProcedure, contractv1.RouteId_ROUTE_ID_AUTH_REVOKE_SESSION,
		func(request *authv1.LogoutRequest, browser *authv1.BrowserContext) proto.Message {
			return &authv1.BrowserLogoutRequest{Request: request, Context: browser}
		},
		func(payload []byte) (*authv1.LogoutResponse, []string, error) {
			response := new(authv1.BrowserLogoutResponse)
			err := proto.Unmarshal(payload, response)
			return response.GetResponse(), response.GetSetCookie(), err
		}, options); err != nil {
		return nil, err
	}
	if err := mountAuth(mux, invoker, authv1connect.AuthServiceLogoutAllProcedure, contractv1.RouteId_ROUTE_ID_AUTH_LOGOUT_ALL,
		func(request *authv1.LogoutAllRequest, browser *authv1.BrowserContext) proto.Message {
			return &authv1.BrowserLogoutAllRequest{Request: request, Context: browser}
		},
		func(payload []byte) (*authv1.LogoutAllResponse, []string, error) {
			response := new(authv1.BrowserLogoutAllResponse)
			err := proto.Unmarshal(payload, response)
			return response.GetResponse(), response.GetSetCookie(), err
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
	unwrap func([]byte) (*Response, []string, error), options []connect.HandlerOption) error {
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
		message, cookies, err := unwrap(result.Payload)
		if err != nil || message == nil || !validBrowserValues(cookies, 8192) {
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
