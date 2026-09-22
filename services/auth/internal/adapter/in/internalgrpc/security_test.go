package internalgrpc

import (
	"errors"
	"testing"

	"connectrpc.com/connect"
	authv1 "github.com/v0hmly/marketmesh/api/gen/go/auth/v1"
	public "github.com/v0hmly/marketmesh/services/auth/internal/adapter/in/connectrpc"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
)

func TestBrowserAuthFailureIsClosedAndDoesNotForwardDetails(t *testing.T) {
	for name, value := range authv1.AuthBrowserFailure_value {
		if value == 0 {
			continue
		}
		reason := name[len("AUTH_BROWSER_FAILURE_"):]
		code, known := public.AuthFailureCode(reason)
		if !known {
			t.Fatal("missing public mapping", name)
		}
		fault := connect.NewError(code, errors.New("secret upstream text"))
		detail, _ := connect.NewErrorDetail(&errdetails.ErrorInfo{Domain: "marketmesh.auth", Reason: reason})
		fault.AddDetail(detail)
		if got := browserAuthFailure(fault); got != authv1.AuthBrowserFailure(value) {
			t.Fatal(name, got)
		}
	}
	for _, tc := range []struct {
		domain, reason string
		code           connect.Code
		metadata       map[string]string
		duplicate      bool
	}{
		{domain: "marketmesh.auth", reason: "CODE_REISSUED", code: connect.CodeInvalidArgument},
		{domain: "untrusted", reason: "CODE_MISMATCH", code: connect.CodeInvalidArgument},
		{domain: "marketmesh.auth", reason: "CODE_MISMATCH", code: connect.CodeInvalidArgument, metadata: map[string]string{"email": "secret@example.test"}},
		{domain: "marketmesh.auth", reason: "INTERNAL_SECRET", code: connect.CodeInternal},
		{domain: "marketmesh.auth", reason: "CODE_MISMATCH", code: connect.CodeInvalidArgument, duplicate: true},
	} {
		fault := connect.NewError(tc.code, errors.New("secret"))
		detail, _ := connect.NewErrorDetail(&errdetails.ErrorInfo{Domain: tc.domain, Reason: tc.reason, Metadata: tc.metadata})
		fault.AddDetail(detail)
		if tc.duplicate {
			fault.AddDetail(detail)
		}
		if browserAuthFailure(fault) != 0 {
			t.Fatal("untrusted failure was forwarded")
		}
	}
}
