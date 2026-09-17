package connectbridge

import (
	"context"
	"errors"
	"net/http"
	"slices"

	"connectrpc.com/connect"
	authv1 "github.com/v0hmly/marketmesh/api/gen/go/auth/v1"
	gatewayv1 "github.com/v0hmly/marketmesh/api/gen/go/gateway/v1"
	contractv1 "github.com/v0hmly/marketmesh/api/gen/go/tunnel/v1"
	userv1 "github.com/v0hmly/marketmesh/api/gen/go/user/v1"
	userv1connect "github.com/v0hmly/marketmesh/api/gen/go/user/v1/userv1connect"
	"github.com/v0hmly/marketmesh/services/gateway-in/internal/tunnel"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/protobuf/proto"
)

// NewUserHandler exposes caller-scoped profile methods, never the private browser service.
func NewUserHandler(invoker Invoker, options ...connect.HandlerOption) (http.Handler, error) {
	return NewAccountHandler(invoker, false, options...)
}

// NewAccountHandler enables only the explicitly configured account capabilities.
func NewAccountHandler(invoker Invoker, addresses bool, options ...connect.HandlerOption) (http.Handler, error) {
	if isNilInvoker(invoker) {
		return nil, errors.New("connect user bridge: invoker is required")
	}
	mux := http.NewServeMux()
	if err := mountUser(mux, invoker, userv1connect.UserServiceGetMeProcedure, contractv1.RouteId_ROUTE_ID_USER_BROWSER_GET_ME,
		func(request *userv1.GetMeRequest, browser *authv1.BrowserContext) proto.Message {
			return &gatewayv1.BrowserGetMeRequest{Request: request, Context: browser}
		},
		func(payload []byte) (*userv1.GetMeResponse, gatewayv1.UserBrowserFailure, error) {
			response := new(gatewayv1.BrowserGetMeResponse)
			err := proto.Unmarshal(payload, response)
			return response.GetResponse(), response.GetFailure(), err
		}, options); err != nil {
		return nil, err
	}
	if err := mountUser(mux, invoker, userv1connect.UserServiceUpdateMeProcedure, contractv1.RouteId_ROUTE_ID_USER_BROWSER_UPDATE_ME,
		func(request *userv1.UpdateMeRequest, browser *authv1.BrowserContext) proto.Message {
			return &gatewayv1.BrowserUpdateMeRequest{Request: request, Context: browser}
		},
		func(payload []byte) (*userv1.UpdateMeResponse, gatewayv1.UserBrowserFailure, error) {
			response := new(gatewayv1.BrowserUpdateMeResponse)
			err := proto.Unmarshal(payload, response)
			return response.GetResponse(), response.GetFailure(), err
		}, options); err != nil {
		return nil, err
	}
	if addresses {
		if err := mountAddresses(mux, invoker, options); err != nil {
			return nil, err
		}
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

func mountUser[Request, Response any](mux *http.ServeMux, invoker Invoker, procedure string, route contractv1.RouteId,
	wrap func(*Request, *authv1.BrowserContext) proto.Message,
	unwrap func([]byte) (*Response, gatewayv1.UserBrowserFailure, error), options []connect.HandlerOption) error {
	policy, allowed := invoker.RoutePolicy(route)
	if !allowed || policy.MaxRequestBytes == 0 || policy.MaxResponseBytes == 0 {
		return errors.New("connect user bridge: bounded route policy is required")
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
		message, failure, err := unwrap(result.Payload)
		if err != nil || (failure != 0 && message != nil) {
			return nil, publicError(connect.CodeInternal)
		}
		if failure != 0 {
			return nil, userFailure(failure)
		}
		if message == nil {
			return nil, publicError(connect.CodeInternal)
		}
		return connect.NewResponse(message), nil
	}, opts...)
	mux.Handle(procedure, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, int64(policy.MaxRequestBytes))
		handler.ServeHTTP(w, r)
	}))
	return nil
}

func userFailure(failure gatewayv1.UserBrowserFailure) error {
	switch failure {
	case gatewayv1.UserBrowserFailure_USER_BROWSER_FAILURE_PROFILE_NOT_READY:
		result := connect.NewError(connect.CodeNotFound, errors.New("profile not ready"))
		detail, err := connect.NewErrorDetail(&errdetails.ErrorInfo{Domain: "marketmesh.user", Reason: "PROFILE_NOT_READY"})
		if err != nil {
			return publicError(connect.CodeInternal)
		}
		result.AddDetail(detail)
		return result
	case gatewayv1.UserBrowserFailure_USER_BROWSER_FAILURE_ADDRESS_NOT_FOUND:
		return addressFailure(connect.CodeNotFound, "ADDRESS_NOT_FOUND")
	case gatewayv1.UserBrowserFailure_USER_BROWSER_FAILURE_ADDRESS_LIMIT_REACHED:
		return addressFailure(connect.CodeResourceExhausted, "ADDRESS_LIMIT_REACHED")
	case gatewayv1.UserBrowserFailure_USER_BROWSER_FAILURE_VERSION_CONFLICT:
		return connect.NewError(connect.CodeAborted, errors.New("version conflict"))
	default:
		return publicError(connect.CodeInternal)
	}
}

func addressFailure(code connect.Code, reason string) error {
	result := connect.NewError(code, errors.New("address operation failed"))
	detail, err := connect.NewErrorDetail(&errdetails.ErrorInfo{Domain: "marketmesh.user", Reason: reason})
	if err != nil {
		return publicError(connect.CodeInternal)
	}
	result.AddDetail(detail)
	return result
}
