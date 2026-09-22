package internalgrpc

import (
	"context"
	"errors"

	userv1 "github.com/v0hmly/marketmesh/api/gen/go/user/v1"
	"github.com/v0hmly/marketmesh/services/user/internal/application/identity"
	"github.com/v0hmly/marketmesh/services/user/internal/domain/avatar"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type AvatarUseCase interface {
	Get(context.Context, identity.Principal) (avatar.Avatar, error)
	Set(context.Context, identity.Principal, avatar.FileID, uint64) (avatar.Avatar, error)
	Clear(context.Context, identity.Principal, uint64) (avatar.Avatar, error)
}

func (h *Handler) EnableAvatar(s AvatarUseCase) error {
	if s == nil {
		return errors.New("avatar grpc: use case required")
	}
	h.avatars = s
	return nil
}
func (h *Handler) avatarPrincipal(ctx context.Context, method string) (identity.Principal, error) {
	noStore(ctx)
	if h.avatars == nil {
		return identity.Principal{}, status.Error(codes.Unimplemented, "method unavailable")
	}
	return h.authenticate(ctx, method)
}
func (h *Handler) GetAvatar(ctx context.Context, r *userv1.GetAvatarRequest) (*userv1.GetAvatarResponse, error) {
	p, err := h.avatarPrincipal(ctx, userv1.UserService_GetAvatar_FullMethodName)
	if err != nil {
		return nil, err
	}
	if r == nil {
		return nil, avatarError(avatar.ErrInvalid)
	}
	a, err := h.avatars.Get(ctx, p)
	if err != nil {
		return nil, avatarError(err)
	}
	return &userv1.GetAvatarResponse{Avatar: wireAvatar(a)}, nil
}
func (h *Handler) SetAvatar(ctx context.Context, r *userv1.SetAvatarRequest) (*userv1.SetAvatarResponse, error) {
	p, err := h.avatarPrincipal(ctx, userv1.UserService_SetAvatar_FullMethodName)
	if err != nil {
		return nil, err
	}
	id, err := avatar.ParseFileID(r.GetFileId())
	if err != nil {
		return nil, avatarError(err)
	}
	a, err := h.avatars.Set(ctx, p, id, r.GetExpectedVersion())
	if err != nil {
		return nil, avatarError(err)
	}
	return &userv1.SetAvatarResponse{Avatar: wireAvatar(a)}, nil
}
func (h *Handler) ClearAvatar(ctx context.Context, r *userv1.ClearAvatarRequest) (*userv1.ClearAvatarResponse, error) {
	p, err := h.avatarPrincipal(ctx, userv1.UserService_ClearAvatar_FullMethodName)
	if err != nil {
		return nil, err
	}
	a, err := h.avatars.Clear(ctx, p, r.GetExpectedVersion())
	if err != nil {
		return nil, avatarError(err)
	}
	return &userv1.ClearAvatarResponse{Avatar: wireAvatar(a)}, nil
}
func wireAvatar(a avatar.Avatar) *userv1.Avatar {
	r := &userv1.Avatar{SubjectId: a.SubjectID.Bytes(), Version: a.Version}
	if a.FileID != (avatar.FileID{}) {
		r.FileId = append([]byte(nil), a.FileID[:]...)
	}
	return r
}
func avatarError(err error) error {
	switch {
	case errors.Is(err, avatar.ErrInvalid):
		return status.Error(codes.InvalidArgument, "invalid avatar")
	case errors.Is(err, avatar.ErrConflict):
		return status.Error(codes.Aborted, "avatar version conflict")
	case errors.Is(err, avatar.ErrUnavailable):
		return status.Error(codes.FailedPrecondition, "avatar unavailable")
	default:
		return mapError(err)
	}
}
