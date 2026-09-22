package connectbridge

import (
	"connectrpc.com/connect"
	"errors"
	authv1 "github.com/v0hmly/marketmesh/api/gen/go/auth/v1"
	gatewayv1 "github.com/v0hmly/marketmesh/api/gen/go/gateway/v1"
	contractv1 "github.com/v0hmly/marketmesh/api/gen/go/tunnel/v1"
	userv1 "github.com/v0hmly/marketmesh/api/gen/go/user/v1"
	userv1connect "github.com/v0hmly/marketmesh/api/gen/go/user/v1/userv1connect"
	"google.golang.org/protobuf/proto"
	"net/http"
)

func mountAvatar(mux *http.ServeMux, invoker Invoker, options []connect.HandlerOption) error {
	if err := mountUser(mux, invoker, userv1connect.UserServiceGetAvatarProcedure, contractv1.RouteId_ROUTE_ID_USER_BROWSER_GET_AVATAR,
		func(r *userv1.GetAvatarRequest, b *authv1.BrowserContext) proto.Message {
			return &gatewayv1.BrowserGetAvatarRequest{Request: r, Context: b}
		},
		func(payload []byte) (*userv1.GetAvatarResponse, gatewayv1.UserBrowserFailure, error) {
			r := new(gatewayv1.BrowserGetAvatarResponse)
			err := proto.Unmarshal(payload, r)
			failure := r.GetFailure()
			if failure != 0 && failure != gatewayv1.UserBrowserFailure_USER_BROWSER_FAILURE_PROFILE_NOT_READY {
				return nil, 0, errors.New("invalid avatar failure")
			}
			return r.GetResponse(), failure, err
		}, options); err != nil {
		return err
	}
	if err := mountUser(mux, invoker, userv1connect.UserServiceSetAvatarProcedure, contractv1.RouteId_ROUTE_ID_USER_BROWSER_SET_AVATAR,
		func(r *userv1.SetAvatarRequest, b *authv1.BrowserContext) proto.Message {
			return &gatewayv1.BrowserSetAvatarRequest{Request: r, Context: b}
		},
		func(payload []byte) (*userv1.SetAvatarResponse, gatewayv1.UserBrowserFailure, error) {
			r := new(gatewayv1.BrowserSetAvatarResponse)
			err := proto.Unmarshal(payload, r)
			failure := r.GetFailure()
			if failure != 0 && failure != gatewayv1.UserBrowserFailure_USER_BROWSER_FAILURE_PROFILE_NOT_READY && failure != gatewayv1.UserBrowserFailure_USER_BROWSER_FAILURE_VERSION_CONFLICT && failure != gatewayv1.UserBrowserFailure_USER_BROWSER_FAILURE_AVATAR_UNAVAILABLE {
				return nil, 0, errors.New("invalid avatar failure")
			}
			return r.GetResponse(), failure, err
		}, options); err != nil {
		return err
	}
	if err := mountUser(mux, invoker, userv1connect.UserServiceClearAvatarProcedure, contractv1.RouteId_ROUTE_ID_USER_BROWSER_CLEAR_AVATAR,
		func(r *userv1.ClearAvatarRequest, b *authv1.BrowserContext) proto.Message {
			return &gatewayv1.BrowserClearAvatarRequest{Request: r, Context: b}
		},
		func(payload []byte) (*userv1.ClearAvatarResponse, gatewayv1.UserBrowserFailure, error) {
			r := new(gatewayv1.BrowserClearAvatarResponse)
			err := proto.Unmarshal(payload, r)
			failure := r.GetFailure()
			if failure != 0 && failure != gatewayv1.UserBrowserFailure_USER_BROWSER_FAILURE_PROFILE_NOT_READY && failure != gatewayv1.UserBrowserFailure_USER_BROWSER_FAILURE_VERSION_CONFLICT && failure != gatewayv1.UserBrowserFailure_USER_BROWSER_FAILURE_AVATAR_UNAVAILABLE {
				return nil, 0, errors.New("invalid avatar failure")
			}
			return r.GetResponse(), failure, err
		}, options); err != nil {
		return err
	}
	return nil
}
