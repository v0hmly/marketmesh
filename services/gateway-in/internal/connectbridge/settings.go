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

func mountSettings(mux *http.ServeMux, invoker Invoker, options []connect.HandlerOption) error {
	if err := mountUser(mux, invoker, userv1connect.UserServiceGetSettingsProcedure, contractv1.RouteId_ROUTE_ID_USER_BROWSER_GET_SETTINGS,
		func(request *userv1.GetSettingsRequest, browser *authv1.BrowserContext) proto.Message {
			return &gatewayv1.BrowserGetSettingsRequest{Request: request, Context: browser}
		},
		func(payload []byte) (*userv1.GetSettingsResponse, gatewayv1.UserBrowserFailure, error) {
			response := new(gatewayv1.BrowserGetSettingsResponse)
			err := proto.Unmarshal(payload, response)
			failure := response.GetFailure()
			if failure != 0 && failure != gatewayv1.UserBrowserFailure_USER_BROWSER_FAILURE_PROFILE_NOT_READY {
				return nil, 0, errors.New("invalid settings failure")
			}
			return response.GetResponse(), failure, err
		}, options); err != nil {
		return err
	}
	if err := mountUser(mux, invoker, userv1connect.UserServiceUpdateSettingsProcedure, contractv1.RouteId_ROUTE_ID_USER_BROWSER_UPDATE_SETTINGS,
		func(request *userv1.UpdateSettingsRequest, browser *authv1.BrowserContext) proto.Message {
			return &gatewayv1.BrowserUpdateSettingsRequest{Request: request, Context: browser}
		},
		func(payload []byte) (*userv1.UpdateSettingsResponse, gatewayv1.UserBrowserFailure, error) {
			response := new(gatewayv1.BrowserUpdateSettingsResponse)
			err := proto.Unmarshal(payload, response)
			failure := response.GetFailure()
			if failure != 0 && failure != gatewayv1.UserBrowserFailure_USER_BROWSER_FAILURE_PROFILE_NOT_READY && failure != gatewayv1.UserBrowserFailure_USER_BROWSER_FAILURE_VERSION_CONFLICT {
				return nil, 0, errors.New("invalid settings failure")
			}
			return response.GetResponse(), failure, err
		}, options); err != nil {
		return err
	}
	return nil
}
