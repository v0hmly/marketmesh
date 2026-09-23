package grpc

import (
	"context"
	"errors"
	"strings"
	"testing"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	grpcgo "google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestPublicErrorInfoSanitizesAndScopesDetails(t *testing.T) {
	policy := PublicErrorInfo{Method: "/user.v1.UserService/GetMe", Code: codes.NotFound, Domain: "marketmesh.user", Reason: "PROFILE_NOT_READY"}
	detail := &errdetails.ErrorInfo{Domain: policy.Domain, Reason: policy.Reason, Metadata: map[string]string{"subject": "private-subject"}}
	detail.ProtoReflect().SetUnknown([]byte{0x22, 0x06, 's', 'e', 'c', 'r', 'e', 't'})
	original, err := status.New(codes.NotFound, "private database details").WithDetails(detail, &errdetails.DebugInfo{Detail: "private stack"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, method string
		policy       []PublicErrorInfo
		mapper       ErrorCodeMapper
		allowed      bool
	}{
		{"disabled", policy.Method, nil, nil, false},
		{"allowed", policy.Method, []PublicErrorInfo{policy}, nil, true},
		{"other method", "/user.v1.UserService/UpdateMe", []PublicErrorInfo{policy}, nil, false},
		{"mapped code", policy.Method, []PublicErrorInfo{policy}, func(error) (codes.Code, bool) { return codes.Unavailable, true }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result := status.Convert(sanitizedMethodStatusError(original.Err(), tc.mapper, tc.method, tc.policy))
			if strings.Contains(result.Err().Error(), "private") {
				t.Fatal("message leaked")
			}
			if !tc.allowed {
				if len(result.Details()) != 0 {
					t.Fatal("unapproved detail escaped")
				}
				return
			}
			if result.Message() != "not found" || len(result.Details()) != 1 {
				t.Fatal("unexpected status", result)
			}
			got, ok := result.Details()[0].(*errdetails.ErrorInfo)
			if !ok || got.Domain != policy.Domain || got.Reason != policy.Reason || len(got.Metadata) != 0 || len(got.ProtoReflect().GetUnknown()) != 0 {
				t.Fatal("detail was not rebuilt from policy")
			}
		})
	}
	for _, bad := range []*errdetails.ErrorInfo{{Domain: "other", Reason: policy.Reason}, {Domain: policy.Domain, Reason: "UNKNOWN_REASON"}} {
		input, _ := status.New(policy.Code, "private").WithDetails(bad)
		if len(status.Convert(sanitizedMethodStatusError(input.Err(), nil, policy.Method, []PublicErrorInfo{policy})).Details()) != 0 {
			t.Fatal("non-allowlisted info escaped")
		}
	}
	for _, input := range []error{nil, errors.New("private"), context.Canceled} {
		if len(status.Convert(sanitizedMethodStatusError(input, nil, policy.Method, []PublicErrorInfo{policy})).Details()) != 0 {
			t.Fatal("detail invented")
		}
	}
	interceptor := unaryServerStatusInterceptor(nil, policy)
	_, err = interceptor(context.Background(), nil, &grpcgo.UnaryServerInfo{FullMethod: policy.Method}, func(context.Context, any) (any, error) { return nil, original.Err() })
	if len(status.Convert(err).Details()) != 1 {
		t.Fatal("unary boundary lost allowed state")
	}
}

func TestPublicErrorInfoPolicyValidation(t *testing.T) {
	valid := PublicErrorInfo{Method: "/user.v1.UserService/GetMe", Code: codes.NotFound, Domain: "marketmesh.user", Reason: "PROFILE_NOT_READY"}
	if err := validatePublicErrorInfo([]PublicErrorInfo{valid}); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*PublicErrorInfo){
		func(p *PublicErrorInfo) { p.Method = "*" }, func(p *PublicErrorInfo) { p.Domain = "private value" }, func(p *PublicErrorInfo) { p.Reason = "any reason" }, func(p *PublicErrorInfo) { p.Code = codes.OK }, func(p *PublicErrorInfo) { p.Code = codes.Unknown }, func(p *PublicErrorInfo) { p.Code = codes.Code(99) },
	} {
		p := valid
		change(&p)
		if validatePublicErrorInfo([]PublicErrorInfo{p}) == nil {
			t.Fatal("unsafe policy accepted")
		}
	}
	if validatePublicErrorInfo(make([]PublicErrorInfo, 33)) == nil {
		t.Fatal("unbounded policy accepted")
	}
}
