package connectbridge

import (
	"connectrpc.com/connect"
	authv1 "github.com/v0hmly/marketmesh/api/gen/go/auth/v1"
	gatewayv1 "github.com/v0hmly/marketmesh/api/gen/go/gateway/v1"
	contractv1 "github.com/v0hmly/marketmesh/api/gen/go/tunnel/v1"
	userv1 "github.com/v0hmly/marketmesh/api/gen/go/user/v1"
	userv1connect "github.com/v0hmly/marketmesh/api/gen/go/user/v1/userv1connect"
	"google.golang.org/protobuf/proto"
	"net/http"
)

func mountAddresses(mux *http.ServeMux, invoker Invoker, options []connect.HandlerOption) error {
	if err := mountUser(mux, invoker, userv1connect.UserServiceListAddressesProcedure, contractv1.RouteId_ROUTE_ID_USER_BROWSER_LIST_ADDRESSES,
		func(request *userv1.ListAddressesRequest, browser *authv1.BrowserContext) proto.Message {
			return &gatewayv1.BrowserListAddressesRequest{Request: request, Context: browser}
		},
		func(payload []byte) (*userv1.ListAddressesResponse, gatewayv1.UserBrowserFailure, error) {
			response := new(gatewayv1.BrowserListAddressesResponse)
			err := proto.Unmarshal(payload, response)
			return response.GetResponse(), response.GetFailure(), err
		}, options); err != nil {
		return err
	}
	if err := mountUser(mux, invoker, userv1connect.UserServiceCreateAddressProcedure, contractv1.RouteId_ROUTE_ID_USER_BROWSER_CREATE_ADDRESS,
		func(request *userv1.CreateAddressRequest, browser *authv1.BrowserContext) proto.Message {
			return &gatewayv1.BrowserCreateAddressRequest{Request: request, Context: browser}
		},
		func(payload []byte) (*userv1.CreateAddressResponse, gatewayv1.UserBrowserFailure, error) {
			response := new(gatewayv1.BrowserCreateAddressResponse)
			err := proto.Unmarshal(payload, response)
			return response.GetResponse(), response.GetFailure(), err
		}, options); err != nil {
		return err
	}
	if err := mountUser(mux, invoker, userv1connect.UserServiceUpdateAddressProcedure, contractv1.RouteId_ROUTE_ID_USER_BROWSER_UPDATE_ADDRESS,
		func(request *userv1.UpdateAddressRequest, browser *authv1.BrowserContext) proto.Message {
			return &gatewayv1.BrowserUpdateAddressRequest{Request: request, Context: browser}
		},
		func(payload []byte) (*userv1.UpdateAddressResponse, gatewayv1.UserBrowserFailure, error) {
			response := new(gatewayv1.BrowserUpdateAddressResponse)
			err := proto.Unmarshal(payload, response)
			return response.GetResponse(), response.GetFailure(), err
		}, options); err != nil {
		return err
	}
	if err := mountUser(mux, invoker, userv1connect.UserServiceDeleteAddressProcedure, contractv1.RouteId_ROUTE_ID_USER_BROWSER_DELETE_ADDRESS,
		func(request *userv1.DeleteAddressRequest, browser *authv1.BrowserContext) proto.Message {
			return &gatewayv1.BrowserDeleteAddressRequest{Request: request, Context: browser}
		},
		func(payload []byte) (*userv1.DeleteAddressResponse, gatewayv1.UserBrowserFailure, error) {
			response := new(gatewayv1.BrowserDeleteAddressResponse)
			err := proto.Unmarshal(payload, response)
			return response.GetResponse(), response.GetFailure(), err
		}, options); err != nil {
		return err
	}
	if err := mountUser(mux, invoker, userv1connect.UserServiceSetDefaultAddressProcedure, contractv1.RouteId_ROUTE_ID_USER_BROWSER_SET_DEFAULT_ADDRESS,
		func(request *userv1.SetDefaultAddressRequest, browser *authv1.BrowserContext) proto.Message {
			return &gatewayv1.BrowserSetDefaultAddressRequest{Request: request, Context: browser}
		},
		func(payload []byte) (*userv1.SetDefaultAddressResponse, gatewayv1.UserBrowserFailure, error) {
			response := new(gatewayv1.BrowserSetDefaultAddressResponse)
			err := proto.Unmarshal(payload, response)
			return response.GetResponse(), response.GetFailure(), err
		}, options); err != nil {
		return err
	}
	return nil
}
