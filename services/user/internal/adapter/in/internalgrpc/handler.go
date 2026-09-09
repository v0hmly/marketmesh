// Package internalgrpc exposes profile operations to authenticated workloads.
package internalgrpc

import (
	"context"
	"errors"
	"strings"

	userv1 "github.com/v0hmly/marketmesh/api/gen/go/user/v1"
	"github.com/v0hmly/marketmesh/platform/logger"
	"github.com/v0hmly/marketmesh/platform/workloadid"
	"github.com/v0hmly/marketmesh/services/user/internal/adapter/out/authsession"
	"github.com/v0hmly/marketmesh/services/user/internal/application/identity"
	"github.com/v0hmly/marketmesh/services/user/internal/application/updateme"
	"github.com/v0hmly/marketmesh/services/user/internal/domain/profile"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const AssertionMetadata = "marketmesh-session-assertion-bin"
const getMethod = "/user.v1.UserService/GetMe"
const updateMethod = "/user.v1.UserService/UpdateMe"

type GetUseCase interface {
	Execute(context.Context, identity.Principal) (profile.Profile, error)
}
type UpdateUseCase interface {
	Execute(context.Context, identity.Principal, updateme.Command) (profile.Profile, error)
}
type Verifier interface {
	Verify(context.Context, string) (identity.Principal, error)
}
type Handler struct {
	userv1.UnimplementedUserServiceServer
	get      GetUseCase
	update   UpdateUseCase
	verifier Verifier
	policy   *workloadid.Policy
}

func New(get GetUseCase, update UpdateUseCase, verifier Verifier, policy *workloadid.Policy, log *logger.Logger) (*Handler, error) {
	if get == nil || update == nil || verifier == nil || policy == nil || log == nil {
		return nil, errors.New("user internal grpc: required dependency missing")
	}
	return &Handler{get: get, update: update, verifier: verifier, policy: policy}, nil
}
func (h *Handler) GetMe(ctx context.Context, request *userv1.GetMeRequest) (*userv1.GetMeResponse, error) {
	noStore(ctx)
	principal, err := h.authenticate(ctx, getMethod)
	if err != nil {
		return nil, err
	}
	if request == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	result, err := h.get.Execute(ctx, principal)
	if err != nil {
		return nil, mapError(err)
	}
	return &userv1.GetMeResponse{Profile: wireProfile(result)}, nil
}
func (h *Handler) UpdateMe(ctx context.Context, request *userv1.UpdateMeRequest) (*userv1.UpdateMeResponse, error) {
	noStore(ctx)
	principal, err := h.authenticate(ctx, updateMethod)
	if err != nil {
		return nil, err
	}
	if request == nil {
		return nil, status.Error(codes.InvalidArgument, "invalid request")
	}
	result, err := h.update.Execute(ctx, principal, updateme.Command{DisplayName: request.GetDisplayName(), Bio: request.GetBio(), ExpectedVersion: request.GetExpectedVersion()})
	if err != nil {
		return nil, mapError(err)
	}
	return &userv1.UpdateMeResponse{Profile: wireProfile(result)}, nil
}
func (h *Handler) authenticate(ctx context.Context, method string) (identity.Principal, error) {
	peer, _, err := workloadid.FromContext(ctx)
	if err != nil {
		return identity.Principal{}, mapError(identity.ErrUnauthenticated)
	}
	if !h.policy.Allow(peer, method) {
		return identity.Principal{}, mapError(identity.ErrForbidden)
	}
	md, _ := metadata.FromIncomingContext(ctx)
	values := md.Get(AssertionMetadata)
	if len(md.Get("cookie")) != 0 || len(md.Get("authorization")) != 0 || len(values) != 1 || len(values[0]) == 0 || len(values[0]) > authsession.MaxAssertionBytes || strings.ContainsAny(values[0], " \t\r\n;=") {
		return identity.Principal{}, mapError(identity.ErrUnauthenticated)
	}
	principal, err := h.verifier.Verify(ctx, values[0])
	if err != nil {
		return identity.Principal{}, mapError(err)
	}
	return principal, nil
}
func noStore(ctx context.Context) {
	_ = grpc.SetHeader(ctx, metadata.Pairs("cache-control", "no-store"))
}
func wireProfile(value profile.Profile) *userv1.Profile {
	return &userv1.Profile{SubjectId: append([]byte(nil), value.SubjectID[:]...), DisplayName: value.Fields.DisplayName, Bio: value.Fields.Bio, Version: value.Version, CreatedAtUnix: value.CreatedAt.Unix(), UpdatedAtUnix: value.UpdatedAt.Unix()}
}
func mapError(err error) error {
	switch {
	case errors.Is(err, identity.ErrUnauthenticated):
		return status.Error(codes.Unauthenticated, "authentication failed")
	case errors.Is(err, identity.ErrForbidden):
		return status.Error(codes.PermissionDenied, "permission denied")
	case errors.Is(err, profile.ErrInvalidProfile):
		return status.Error(codes.InvalidArgument, "invalid profile")
	case errors.Is(err, profile.ErrNotReady):
		return status.Error(codes.NotFound, "profile not ready")
	case errors.Is(err, profile.ErrConflict):
		return status.Error(codes.Aborted, "profile version conflict")
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "request canceled")
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "request deadline exceeded")
	default:
		return status.Error(codes.Unavailable, "service unavailable")
	}
}

var _ userv1.UserServiceServer = (*Handler)(nil)
