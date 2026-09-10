package internalgrpc

import (
	"testing"

	"github.com/v0hmly/marketmesh/services/user/internal/application/identity"
	"github.com/v0hmly/marketmesh/services/user/internal/domain/profile"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func TestPreparingProfileIsNotAnAuthenticationFailure(t *testing.T) {
	preparing := status.Convert(mapError(profile.ErrNotReady))
	if preparing.Code() != codes.NotFound || len(preparing.Details()) != 1 {
		t.Fatal("missing typed profile preparation state", preparing)
	}
	detail, ok := preparing.Details()[0].(*errdetails.ErrorInfo)
	if !ok || detail.Reason != "PROFILE_NOT_READY" || detail.Domain != "marketmesh.user" || len(detail.Metadata) != 0 {
		t.Fatal("profile preparation must be stable and contain no identity data")
	}
	unauthenticated := status.Convert(mapError(identity.ErrUnauthenticated))
	if unauthenticated.Code() != codes.Unauthenticated || len(unauthenticated.Details()) != 0 {
		t.Fatal("an invalid session must not expose the profile preparation state")
	}
}
