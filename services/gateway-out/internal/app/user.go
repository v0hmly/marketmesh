package app

import (
	"context"
	"strings"
	"time"

	authv1 "github.com/v0hmly/marketmesh/api/gen/go/auth/v1"
	gatewayv1 "github.com/v0hmly/marketmesh/api/gen/go/gateway/v1"
	tunnelv1 "github.com/v0hmly/marketmesh/api/gen/go/tunnel/v1"
	userv1 "github.com/v0hmly/marketmesh/api/gen/go/user/v1"
	"github.com/v0hmly/marketmesh/services/gateway-out/internal/tunnel"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

const userAssertionMetadata = "marketmesh-session-assertion-bin"

type browserExchanger interface {
	ExchangeBrowserSession(context.Context, *authv1.ExchangeBrowserSessionRequest, ...grpc.CallOption) (*authv1.ExchangeBrowserSessionResponse, error)
}

// userBrowserClient composes two fixed internal RPCs. No credentials from the
// tunnel metadata are trusted, and the resulting assertion never crosses it.
type userBrowserClient struct {
	auth browserExchanger
	user userv1.UserServiceClient
}

func (c *userBrowserClient) Invoke(ctx context.Context, method string, args, reply any, _ ...grpc.CallOption) error {
	switch method {
	case gatewayv1.UserBrowserService_BrowserGetMe_FullMethodName:
		request, ok := args.(*gatewayv1.BrowserGetMeRequest)
		response, responseOK := reply.(*gatewayv1.BrowserGetMeResponse)
		if !ok || !responseOK || request == nil || response == nil || request.GetRequest() == nil || request.GetContext() == nil {
			return status.Error(codes.InvalidArgument, "invalid request")
		}
		*response = gatewayv1.BrowserGetMeResponse{}
		authenticated, err := c.authenticate(ctx, request.GetContext())
		if err != nil {
			return err
		}
		result, err := c.user.GetMe(authenticated, request.GetRequest(), grpc.WaitForReady(false), grpc.MaxCallRecvMsgSize(16*1024), grpc.MaxCallSendMsgSize(16*1024))
		if err != nil {
			response.Failure = userFailure(err, false)
			if response.Failure != gatewayv1.UserBrowserFailure_USER_BROWSER_FAILURE_UNSPECIFIED {
				return nil
			}
			return err
		}
		if !validUserProfile(result.GetProfile()) {
			return status.Error(codes.Internal, "invalid response")
		}
		response.Response = result
		return nil
	case gatewayv1.UserBrowserService_BrowserUpdateMe_FullMethodName:
		request, ok := args.(*gatewayv1.BrowserUpdateMeRequest)
		response, responseOK := reply.(*gatewayv1.BrowserUpdateMeResponse)
		if !ok || !responseOK || request == nil || response == nil || request.GetRequest() == nil || request.GetContext() == nil {
			return status.Error(codes.InvalidArgument, "invalid request")
		}
		*response = gatewayv1.BrowserUpdateMeResponse{}
		authenticated, err := c.authenticate(ctx, request.GetContext())
		if err != nil {
			return err
		}
		result, err := c.user.UpdateMe(authenticated, request.GetRequest(), grpc.WaitForReady(false), grpc.MaxCallRecvMsgSize(16*1024), grpc.MaxCallSendMsgSize(16*1024))
		if err != nil {
			response.Failure = userFailure(err, true)
			if response.Failure != gatewayv1.UserBrowserFailure_USER_BROWSER_FAILURE_UNSPECIFIED {
				return nil
			}
			return err
		}
		if !validUserProfile(result.GetProfile()) {
			return status.Error(codes.Internal, "invalid response")
		}
		response.Response = result
		return nil
	case gatewayv1.UserBrowserService_BrowserListAddresses_FullMethodName:
		request, ok := args.(*gatewayv1.BrowserListAddressesRequest)
		response, responseOK := reply.(*gatewayv1.BrowserListAddressesResponse)
		if !ok || !responseOK || request == nil || response == nil {
			return status.Error(codes.InvalidArgument, "invalid request")
		}
		*response = gatewayv1.BrowserListAddressesResponse{}
		var err error
		response.Response, response.Failure, err = invokeAddress(ctx, c, request.GetContext(), request.GetRequest(), c.user.ListAddresses, false)
		return err
	case gatewayv1.UserBrowserService_BrowserCreateAddress_FullMethodName:
		request, ok := args.(*gatewayv1.BrowserCreateAddressRequest)
		response, responseOK := reply.(*gatewayv1.BrowserCreateAddressResponse)
		if !ok || !responseOK || request == nil || response == nil {
			return status.Error(codes.InvalidArgument, "invalid request")
		}
		*response = gatewayv1.BrowserCreateAddressResponse{}
		var err error
		response.Response, response.Failure, err = invokeAddress(ctx, c, request.GetContext(), request.GetRequest(), c.user.CreateAddress, true)
		return err
	case gatewayv1.UserBrowserService_BrowserUpdateAddress_FullMethodName:
		request, ok := args.(*gatewayv1.BrowserUpdateAddressRequest)
		response, responseOK := reply.(*gatewayv1.BrowserUpdateAddressResponse)
		if !ok || !responseOK || request == nil || response == nil {
			return status.Error(codes.InvalidArgument, "invalid request")
		}
		*response = gatewayv1.BrowserUpdateAddressResponse{}
		var err error
		response.Response, response.Failure, err = invokeAddress(ctx, c, request.GetContext(), request.GetRequest(), c.user.UpdateAddress, true)
		return err
	case gatewayv1.UserBrowserService_BrowserDeleteAddress_FullMethodName:
		request, ok := args.(*gatewayv1.BrowserDeleteAddressRequest)
		response, responseOK := reply.(*gatewayv1.BrowserDeleteAddressResponse)
		if !ok || !responseOK || request == nil || response == nil {
			return status.Error(codes.InvalidArgument, "invalid request")
		}
		*response = gatewayv1.BrowserDeleteAddressResponse{}
		var err error
		response.Response, response.Failure, err = invokeAddress(ctx, c, request.GetContext(), request.GetRequest(), c.user.DeleteAddress, true)
		return err
	case gatewayv1.UserBrowserService_BrowserSetDefaultAddress_FullMethodName:
		request, ok := args.(*gatewayv1.BrowserSetDefaultAddressRequest)
		response, responseOK := reply.(*gatewayv1.BrowserSetDefaultAddressResponse)
		if !ok || !responseOK || request == nil || response == nil {
			return status.Error(codes.InvalidArgument, "invalid request")
		}
		*response = gatewayv1.BrowserSetDefaultAddressResponse{}
		var err error
		response.Response, response.Failure, err = invokeAddress(ctx, c, request.GetContext(), request.GetRequest(), c.user.SetDefaultAddress, true)
		return err
	case gatewayv1.UserBrowserService_BrowserGetAvatar_FullMethodName:
		request, ok := args.(*gatewayv1.BrowserGetAvatarRequest)
		response, responseOK := reply.(*gatewayv1.BrowserGetAvatarResponse)
		if !ok || !responseOK || request == nil || response == nil {
			return status.Error(codes.InvalidArgument, "invalid request")
		}
		*response = gatewayv1.BrowserGetAvatarResponse{}
		var err error
		response.Response, response.Failure, err = invokeAvatar(ctx, c, request.GetContext(), request.GetRequest(), c.user.GetAvatar, false)
		return err
	case gatewayv1.UserBrowserService_BrowserSetAvatar_FullMethodName:
		request, ok := args.(*gatewayv1.BrowserSetAvatarRequest)
		response, responseOK := reply.(*gatewayv1.BrowserSetAvatarResponse)
		if !ok || !responseOK || request == nil || response == nil {
			return status.Error(codes.InvalidArgument, "invalid request")
		}
		*response = gatewayv1.BrowserSetAvatarResponse{}
		var err error
		response.Response, response.Failure, err = invokeAvatar(ctx, c, request.GetContext(), request.GetRequest(), c.user.SetAvatar, true)
		return err
	case gatewayv1.UserBrowserService_BrowserClearAvatar_FullMethodName:
		request, ok := args.(*gatewayv1.BrowserClearAvatarRequest)
		response, responseOK := reply.(*gatewayv1.BrowserClearAvatarResponse)
		if !ok || !responseOK || request == nil || response == nil {
			return status.Error(codes.InvalidArgument, "invalid request")
		}
		*response = gatewayv1.BrowserClearAvatarResponse{}
		var err error
		response.Response, response.Failure, err = invokeAvatar(ctx, c, request.GetContext(), request.GetRequest(), c.user.ClearAvatar, true)
		return err
	case gatewayv1.UserBrowserService_BrowserGetSettings_FullMethodName:
		request, ok := args.(*gatewayv1.BrowserGetSettingsRequest)
		response, responseOK := reply.(*gatewayv1.BrowserGetSettingsResponse)
		if !ok || !responseOK || request == nil || response == nil {
			return status.Error(codes.InvalidArgument, "invalid request")
		}
		*response = gatewayv1.BrowserGetSettingsResponse{}
		var err error
		response.Response, response.Failure, err = invokeSettings(ctx, c, request.GetContext(), request.GetRequest(), c.user.GetSettings, false)
		return err
	case gatewayv1.UserBrowserService_BrowserUpdateSettings_FullMethodName:
		request, ok := args.(*gatewayv1.BrowserUpdateSettingsRequest)
		response, responseOK := reply.(*gatewayv1.BrowserUpdateSettingsResponse)
		if !ok || !responseOK || request == nil || response == nil {
			return status.Error(codes.InvalidArgument, "invalid request")
		}
		*response = gatewayv1.BrowserUpdateSettingsResponse{}
		var err error
		response.Response, response.Failure, err = invokeSettings(ctx, c, request.GetContext(), request.GetRequest(), c.user.UpdateSettings, true)
		return err
	default:
		return status.Error(codes.PermissionDenied, "route unavailable")
	}
}

func (c *userBrowserClient) authenticate(ctx context.Context, browser *authv1.BrowserContext) (context.Context, error) {
	// An empty outgoing context discards both tunnel metadata and inherited caller
	// credentials. Auth alone interprets cookie and browser origin values.
	clean := metadata.NewOutgoingContext(ctx, metadata.MD{})
	exchanged, err := c.auth.ExchangeBrowserSession(clean, &authv1.ExchangeBrowserSessionRequest{Context: browser, Audience: "user"}, grpc.WaitForReady(false), grpc.MaxCallRecvMsgSize(16*1024), grpc.MaxCallSendMsgSize(16*1024))
	if err != nil {
		return nil, err
	}
	token := exchanged.GetAssertion()
	if len(token) == 0 || len(token) > 16*1024 || strings.Count(token, ".") != 2 || strings.ContainsAny(token, " \t\r\n;=") || exchanged.GetExpiresAtUnix() <= time.Now().Unix() {
		return nil, status.Error(codes.Unauthenticated, "authentication failed")
	}
	return metadata.NewOutgoingContext(ctx, metadata.Pairs(userAssertionMetadata, token)), nil
}

func (*userBrowserClient) NewStream(context.Context, *grpc.StreamDesc, string, ...grpc.CallOption) (grpc.ClientStream, error) {
	return nil, status.Error(codes.Unimplemented, "streaming unavailable")
}

func userFailure(err error, update bool) gatewayv1.UserBrowserFailure {
	if update && status.Code(err) == codes.Aborted {
		return gatewayv1.UserBrowserFailure_USER_BROWSER_FAILURE_VERSION_CONFLICT
	}
	if status.Code(err) == codes.NotFound {
		for _, detail := range status.Convert(err).Details() {
			if info, ok := detail.(*errdetails.ErrorInfo); ok && info.GetDomain() == "marketmesh.user" && info.GetReason() == "PROFILE_NOT_READY" && len(info.GetMetadata()) == 0 {
				return gatewayv1.UserBrowserFailure_USER_BROWSER_FAILURE_PROFILE_NOT_READY
			}
		}
	}
	return gatewayv1.UserBrowserFailure_USER_BROWSER_FAILURE_UNSPECIFIED
}

func validUserProfile(profile *userv1.Profile) bool {
	return profile != nil && len(profile.GetSubjectId()) == 16 && profile.GetVersion() > 0 && proto.Size(profile) <= 16*1024
}

func userRoutes(timeout time.Duration) []tunnel.RouteSpec {
	specs := []tunnel.RouteSpec{
		{ID: tunnelv1.RouteId_ROUTE_ID_USER_BROWSER_GET_ME, Method: gatewayv1.UserBrowserService_BrowserGetMe_FullMethodName, NewRequest: func() proto.Message { return new(gatewayv1.BrowserGetMeRequest) }, NewResponse: func() proto.Message { return new(gatewayv1.BrowserGetMeResponse) }},
		{ID: tunnelv1.RouteId_ROUTE_ID_USER_BROWSER_UPDATE_ME, Method: gatewayv1.UserBrowserService_BrowserUpdateMe_FullMethodName, NewRequest: func() proto.Message { return new(gatewayv1.BrowserUpdateMeRequest) }, NewResponse: func() proto.Message { return new(gatewayv1.BrowserUpdateMeResponse) }, Mutating: true},
	}
	for i := range specs {
		specs[i].TrafficClass = tunnelv1.TrafficClass_TRAFFIC_CLASS_REGULAR
		specs[i].MaxRequestBytes = 16 * 1024
		specs[i].MaxResponseBytes = 16 * 1024
		specs[i].MaxDeadline = timeout
	}
	return specs
}

var _ grpc.ClientConnInterface = (*userBrowserClient)(nil)
