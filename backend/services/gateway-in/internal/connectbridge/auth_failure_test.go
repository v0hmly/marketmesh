package connectbridge

import (
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"
	authv1 "github.com/v0hmly/marketmesh/api/gen/go/auth/v1"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
)

func TestAuthFailureExposesOnlyFiniteReason(t *testing.T) {
	for name, number := range authv1.AuthBrowserFailure_value {
		if number == 0 {
			continue
		}
		var fault *connect.Error
		if !errors.As(authFailure(authv1.AuthBrowserFailure(number)), &fault) || len(fault.Details()) != 1 {
			t.Fatal("missing bounded failure", name)
		}
		value, err := fault.Details()[0].Value()
		if err != nil {
			t.Fatal(err)
		}
		info, ok := value.(*errdetails.ErrorInfo)
		if !ok || info.Domain != "marketmesh.auth" || len(info.Metadata) != 0 || info.Reason != strings.TrimPrefix(name, "AUTH_BROWSER_FAILURE_") {
			t.Fatal("failure expanded beyond contract")
		}
	}
	for _, number := range []int32{0, -1, 100000} {
		var fault *connect.Error
		if !errors.As(authFailure(authv1.AuthBrowserFailure(number)), &fault) || len(fault.Details()) != 0 || fault.Code() != connect.CodeInternal {
			t.Fatal("unknown failure reached public response")
		}
	}
}
