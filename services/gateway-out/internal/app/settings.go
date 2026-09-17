package app

import (
	"context"
	authv1 "github.com/v0hmly/marketmesh/api/gen/go/auth/v1"
	gatewayv1 "github.com/v0hmly/marketmesh/api/gen/go/gateway/v1"
	tunnelv1 "github.com/v0hmly/marketmesh/api/gen/go/tunnel/v1"
	userv1 "github.com/v0hmly/marketmesh/api/gen/go/user/v1"
	"github.com/v0hmly/marketmesh/services/gateway-out/internal/tunnel"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"time"
)

type settingsResult interface {
	proto.Message
	GetSettings() *userv1.AccountSettings
}

func invokeSettings[Request proto.Message, Response settingsResult](ctx context.Context, c *userBrowserClient, browser *authv1.BrowserContext, request Request, call func(context.Context, Request, ...grpc.CallOption) (Response, error), mutating bool) (Response, gatewayv1.UserBrowserFailure, error) {
	var zero Response
	if browser == nil || !request.ProtoReflect().IsValid() {
		return zero, 0, status.Error(codes.InvalidArgument, "invalid request")
	}
	authenticated, err := c.authenticate(ctx, browser)
	if err != nil {
		return zero, 0, err
	}
	result, err := call(authenticated, request, grpc.WaitForReady(false), grpc.MaxCallRecvMsgSize(16*1024), grpc.MaxCallSendMsgSize(16*1024))
	if err != nil {
		if failure := userFailure(err, mutating); failure != 0 {
			return zero, failure, nil
		}
		return zero, 0, err
	}
	if !validSettings(result.GetSettings()) {
		return zero, 0, status.Error(codes.Internal, "invalid response")
	}
	return result, 0, nil
}

func validSettings(settings *userv1.AccountSettings) bool {
	if settings == nil || !validAddressID(settings.GetSubjectId()) || settings.GetVersion() == 0 || proto.Size(settings) > 16*1024 {
		return false
	}
	switch settings.GetTheme() {
	case userv1.Theme_THEME_SYSTEM, userv1.Theme_THEME_LIGHT, userv1.Theme_THEME_DARK:
		return true
	default:
		return false
	}
}
func settingsRoutes(timeout time.Duration) []tunnel.RouteSpec {
	specs := []tunnel.RouteSpec{
		{ID: tunnelv1.RouteId_ROUTE_ID_USER_BROWSER_GET_SETTINGS, Method: gatewayv1.UserBrowserService_BrowserGetSettings_FullMethodName, NewRequest: func() proto.Message { return new(gatewayv1.BrowserGetSettingsRequest) }, NewResponse: func() proto.Message { return new(gatewayv1.BrowserGetSettingsResponse) }, Mutating: false},
		{ID: tunnelv1.RouteId_ROUTE_ID_USER_BROWSER_UPDATE_SETTINGS, Method: gatewayv1.UserBrowserService_BrowserUpdateSettings_FullMethodName, NewRequest: func() proto.Message { return new(gatewayv1.BrowserUpdateSettingsRequest) }, NewResponse: func() proto.Message { return new(gatewayv1.BrowserUpdateSettingsResponse) }, Mutating: true},
	}
	for i := range specs {
		specs[i].TrafficClass = tunnelv1.TrafficClass_TRAFFIC_CLASS_REGULAR
		specs[i].MaxRequestBytes = 16 * 1024
		specs[i].MaxResponseBytes = 16 * 1024
		specs[i].MaxDeadline = timeout
	}
	return specs
}
