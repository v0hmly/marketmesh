package connectbridge

import (
	"errors"
	"strings"

	"connectrpc.com/connect"
	authv1 "github.com/v0hmly/marketmesh/api/gen/go/auth/v1"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
)

func authFailure(failure authv1.AuthBrowserFailure) error {
	var code connect.Code
	switch failure {
	case authv1.AuthBrowserFailure_AUTH_BROWSER_FAILURE_INVALID_INPUT, authv1.AuthBrowserFailure_AUTH_BROWSER_FAILURE_CODE_MISMATCH:
		code = connect.CodeInvalidArgument
	case authv1.AuthBrowserFailure_AUTH_BROWSER_FAILURE_INVALID_CREDENTIALS:
		code = connect.CodeUnauthenticated
	case authv1.AuthBrowserFailure_AUTH_BROWSER_FAILURE_LOGIN_LOCKED, authv1.AuthBrowserFailure_AUTH_BROWSER_FAILURE_CODE_REISSUED, authv1.AuthBrowserFailure_AUTH_BROWSER_FAILURE_CODE_EXPIRED, authv1.AuthBrowserFailure_AUTH_BROWSER_FAILURE_TOKEN_EXPIRED, authv1.AuthBrowserFailure_AUTH_BROWSER_FAILURE_TOKEN_USED, authv1.AuthBrowserFailure_AUTH_BROWSER_FAILURE_EMAIL_UNVERIFIED, authv1.AuthBrowserFailure_AUTH_BROWSER_FAILURE_CODE_REQUIRED, authv1.AuthBrowserFailure_AUTH_BROWSER_FAILURE_NEW_DEVICE_COOLDOWN:
		code = connect.CodeFailedPrecondition
	case authv1.AuthBrowserFailure_AUTH_BROWSER_FAILURE_RATE_LIMITED:
		code = connect.CodeResourceExhausted
	case authv1.AuthBrowserFailure_AUTH_BROWSER_FAILURE_NOT_FOUND:
		code = connect.CodeNotFound
	default:
		return publicError(connect.CodeInternal)
	}
	result := connect.NewError(code, errors.New("account operation rejected"))
	detail, err := connect.NewErrorDetail(&errdetails.ErrorInfo{Domain: "marketmesh.auth", Reason: strings.TrimPrefix(failure.String(), "AUTH_BROWSER_FAILURE_")})
	if err != nil {
		return publicError(connect.CodeInternal)
	}
	result.AddDetail(detail)
	return result
}
