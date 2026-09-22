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

type avatarResult interface {
	proto.Message
	GetAvatar() *userv1.Avatar
}

func invokeAvatar[Request proto.Message, Response avatarResult](ctx context.Context, c *userBrowserClient, browser *authv1.BrowserContext, request Request, call func(context.Context, Request, ...grpc.CallOption) (Response, error), mutating bool) (Response, gatewayv1.UserBrowserFailure, error) {
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
		if mutating && status.Code(err) == codes.FailedPrecondition {
			return zero, gatewayv1.UserBrowserFailure_USER_BROWSER_FAILURE_AVATAR_UNAVAILABLE, nil
		}
		if failure := userFailure(err, mutating); failure != 0 {
			return zero, failure, nil
		}
		return zero, 0, err
	}
	if !validAvatar(result.GetAvatar()) {
		return zero, 0, status.Error(codes.Internal, "invalid response")
	}
	return result, 0, nil
}

func validAvatar(a *userv1.Avatar) bool {
	return a != nil && validAddressID(a.SubjectId) && a.Version > 0 && a.Version <= 9223372036854775807 && (len(a.FileId) == 0 || validAddressID(a.FileId))
}
func avatarRoutes(timeout time.Duration) []tunnel.RouteSpec {
	specs := []tunnel.RouteSpec{
		{ID: tunnelv1.RouteId_ROUTE_ID_USER_BROWSER_GET_AVATAR, Method: gatewayv1.UserBrowserService_BrowserGetAvatar_FullMethodName, NewRequest: func() proto.Message { return new(gatewayv1.BrowserGetAvatarRequest) }, NewResponse: func() proto.Message { return new(gatewayv1.BrowserGetAvatarResponse) }, Mutating: false},
		{ID: tunnelv1.RouteId_ROUTE_ID_USER_BROWSER_SET_AVATAR, Method: gatewayv1.UserBrowserService_BrowserSetAvatar_FullMethodName, NewRequest: func() proto.Message { return new(gatewayv1.BrowserSetAvatarRequest) }, NewResponse: func() proto.Message { return new(gatewayv1.BrowserSetAvatarResponse) }, Mutating: true},
		{ID: tunnelv1.RouteId_ROUTE_ID_USER_BROWSER_CLEAR_AVATAR, Method: gatewayv1.UserBrowserService_BrowserClearAvatar_FullMethodName, NewRequest: func() proto.Message { return new(gatewayv1.BrowserClearAvatarRequest) }, NewResponse: func() proto.Message { return new(gatewayv1.BrowserClearAvatarResponse) }, Mutating: true},
	}

	for i := range specs {
		specs[i].TrafficClass = tunnelv1.TrafficClass_TRAFFIC_CLASS_REGULAR
		specs[i].MaxRequestBytes = 16 * 1024
		specs[i].MaxResponseBytes = 16 * 1024
		specs[i].MaxDeadline = timeout
	}
	return specs
}
