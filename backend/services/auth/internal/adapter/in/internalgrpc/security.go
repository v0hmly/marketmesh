package internalgrpc

import (
	"errors"

	"connectrpc.com/connect"
	authv1 "github.com/v0hmly/marketmesh/api/gen/go/auth/v1"
	public "github.com/v0hmly/marketmesh/services/auth/internal/adapter/in/connectrpc"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
)

func browserAuthFailure(err error) authv1.AuthBrowserFailure {
	var failure *connect.Error
	if !errors.As(err, &failure) || len(failure.Details()) != 1 {
		return 0
	}
	value, e := failure.Details()[0].Value()
	if e != nil {
		return 0
	}
	info, ok := value.(*errdetails.ErrorInfo)
	if !ok || info.Domain != "marketmesh.auth" || len(info.Metadata) != 0 {
		return 0
	}
	code, ok := public.AuthFailureCode(info.Reason)
	if !ok || code != failure.Code() {
		return 0
	}
	id, ok := authv1.AuthBrowserFailure_value["AUTH_BROWSER_FAILURE_"+info.Reason]
	if !ok || id == 0 {
		return 0
	}
	return authv1.AuthBrowserFailure(id)
}
