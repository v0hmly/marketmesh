package app

import (
	"bytes"
	"context"
	authv1 "github.com/v0hmly/marketmesh/api/gen/go/auth/v1"
	gatewayv1 "github.com/v0hmly/marketmesh/api/gen/go/gateway/v1"
	tunnelv1 "github.com/v0hmly/marketmesh/api/gen/go/tunnel/v1"
	userv1 "github.com/v0hmly/marketmesh/api/gen/go/user/v1"
	"github.com/v0hmly/marketmesh/services/gateway-out/internal/tunnel"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"time"
)

const addressResponseLimit = 128 * 1024

type addressResult interface {
	proto.Message
	GetBook() *userv1.AddressBook
}

func invokeAddress[Request proto.Message, Response addressResult](ctx context.Context, c *userBrowserClient, browser *authv1.BrowserContext, request Request, call func(context.Context, Request, ...grpc.CallOption) (Response, error), mutating bool) (Response, gatewayv1.UserBrowserFailure, error) {
	var zero Response
	if browser == nil || !request.ProtoReflect().IsValid() {
		return zero, 0, status.Error(codes.InvalidArgument, "invalid request")
	}
	authenticated, err := c.authenticate(ctx, browser)
	if err != nil {
		return zero, 0, err
	}
	result, err := call(authenticated, request, grpc.WaitForReady(false), grpc.MaxCallRecvMsgSize(addressResponseLimit), grpc.MaxCallSendMsgSize(16*1024))
	if err != nil {
		if failure := addressBrowserFailure(err, mutating); failure != 0 {
			return zero, failure, nil
		}
		return zero, 0, err
	}
	if !validAddressBook(result.GetBook()) {
		return zero, 0, status.Error(codes.Internal, "invalid response")
	}
	return result, 0, nil
}

func addressBrowserFailure(err error, mutating bool) gatewayv1.UserBrowserFailure {
	if failure := userFailure(err, mutating); failure != 0 {
		return failure
	}
	for _, detail := range status.Convert(err).Details() {
		info, ok := detail.(*errdetails.ErrorInfo)
		if !ok || info.GetDomain() != "marketmesh.user" || len(info.GetMetadata()) != 0 {
			continue
		}
		if status.Code(err) == codes.NotFound && info.GetReason() == "ADDRESS_NOT_FOUND" {
			return gatewayv1.UserBrowserFailure_USER_BROWSER_FAILURE_ADDRESS_NOT_FOUND
		}
		if status.Code(err) == codes.ResourceExhausted && info.GetReason() == "ADDRESS_LIMIT_REACHED" {
			return gatewayv1.UserBrowserFailure_USER_BROWSER_FAILURE_ADDRESS_LIMIT_REACHED
		}
	}
	return 0
}

func validAddressBook(book *userv1.AddressBook) bool {
	if book == nil || !validAddressID(book.GetSubjectId()) || book.GetVersion() == 0 || len(book.GetAddresses()) > 20 || proto.Size(book) > addressResponseLimit-64 {
		return false
	}
	seen := make(map[string]bool, len(book.GetAddresses()))
	defaults := 0
	for _, address := range book.GetAddresses() {
		if address == nil || !validAddressID(address.GetAddressId()) || address.GetFields() == nil || seen[string(address.GetAddressId())] {
			return false
		}
		seen[string(address.GetAddressId())] = true
		if address.GetIsDefault() {
			defaults++
		}
	}
	return defaults <= 1
}
func validAddressID(id []byte) bool { return len(id) == 16 && !bytes.Equal(id, make([]byte, 16)) }

func addressRoutes(timeout time.Duration) []tunnel.RouteSpec {
	specs := []tunnel.RouteSpec{
		{ID: tunnelv1.RouteId_ROUTE_ID_USER_BROWSER_LIST_ADDRESSES, Method: gatewayv1.UserBrowserService_BrowserListAddresses_FullMethodName, NewRequest: func() proto.Message { return new(gatewayv1.BrowserListAddressesRequest) }, NewResponse: func() proto.Message { return new(gatewayv1.BrowserListAddressesResponse) }, Mutating: false},
		{ID: tunnelv1.RouteId_ROUTE_ID_USER_BROWSER_CREATE_ADDRESS, Method: gatewayv1.UserBrowserService_BrowserCreateAddress_FullMethodName, NewRequest: func() proto.Message { return new(gatewayv1.BrowserCreateAddressRequest) }, NewResponse: func() proto.Message { return new(gatewayv1.BrowserCreateAddressResponse) }, Mutating: true},
		{ID: tunnelv1.RouteId_ROUTE_ID_USER_BROWSER_UPDATE_ADDRESS, Method: gatewayv1.UserBrowserService_BrowserUpdateAddress_FullMethodName, NewRequest: func() proto.Message { return new(gatewayv1.BrowserUpdateAddressRequest) }, NewResponse: func() proto.Message { return new(gatewayv1.BrowserUpdateAddressResponse) }, Mutating: true},
		{ID: tunnelv1.RouteId_ROUTE_ID_USER_BROWSER_DELETE_ADDRESS, Method: gatewayv1.UserBrowserService_BrowserDeleteAddress_FullMethodName, NewRequest: func() proto.Message { return new(gatewayv1.BrowserDeleteAddressRequest) }, NewResponse: func() proto.Message { return new(gatewayv1.BrowserDeleteAddressResponse) }, Mutating: true},
		{ID: tunnelv1.RouteId_ROUTE_ID_USER_BROWSER_SET_DEFAULT_ADDRESS, Method: gatewayv1.UserBrowserService_BrowserSetDefaultAddress_FullMethodName, NewRequest: func() proto.Message { return new(gatewayv1.BrowserSetDefaultAddressRequest) }, NewResponse: func() proto.Message { return new(gatewayv1.BrowserSetDefaultAddressResponse) }, Mutating: true},
	}
	for i := range specs {
		specs[i].TrafficClass = tunnelv1.TrafficClass_TRAFFIC_CLASS_REGULAR
		specs[i].MaxRequestBytes = 16 * 1024
		specs[i].MaxResponseBytes = addressResponseLimit
		specs[i].MaxDeadline = timeout
	}
	return specs
}
