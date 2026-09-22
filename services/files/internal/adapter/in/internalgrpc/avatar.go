package internalgrpc

import (
	"context"
	"encoding/hex"
	"errors"

	filesv1 "github.com/v0hmly/marketmesh/api/gen/go/files/v1"
	"github.com/v0hmly/marketmesh/platform/workloadid"
	"github.com/v0hmly/marketmesh/services/files/internal/application/files"
	"github.com/v0hmly/marketmesh/services/files/internal/application/identity"
	"github.com/v0hmly/marketmesh/services/files/internal/domain/file"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// AvatarHandler trusts exactly one User workload to supply its verified owner.
// It is registered on a separate listener, never alongside public FileService.
type AvatarHandler struct {
	filesv1.UnimplementedFileAvatarServiceServer
	service *files.Service
	user    workloadid.Scope
}

func NewAvatar(service *files.Service, user workloadid.Scope) (*AvatarHandler, error) {
	if service == nil || user.Validate() != nil || user.ServiceAccount != "user" {
		return nil, file.ErrInvalid
	}
	return &AvatarHandler{service: service, user: user}, nil
}
func (h *AvatarHandler) owner(ctx context.Context, raw []byte) (file.Owner, error) {
	got, _, err := workloadid.ScopedFromContext(ctx)
	if err != nil || got != h.user {
		return file.Owner{}, status.Error(codes.PermissionDenied, "workload denied")
	}
	md, _ := metadata.FromIncomingContext(ctx)
	if len(md.Get("cookie")) != 0 || len(md.Get("authorization")) != 0 || len(md.Get(AssertionMetadata)) != 0 {
		return file.Owner{}, status.Error(codes.Unauthenticated, "external credentials rejected")
	}
	id, err := file.ParseID(raw)
	if err != nil {
		return file.Owner{}, mapError(err)
	}
	owner := file.Owner{Tenant: id, Subject: id} // Personal namespaces, ADR-0015.
	recordWorkloadOwner(ctx, identity.Principal{Owner: owner})
	return owner, nil
}
func (h *AvatarHandler) InspectOwnedAvatar(ctx context.Context, r *filesv1.InspectOwnedAvatarRequest) (*filesv1.InspectOwnedAvatarResponse, error) {
	owner, err := h.owner(ctx, r.GetSubjectId())
	if err != nil {
		return nil, err
	}
	sid, err := hex.DecodeString(r.GetSessionId())
	if err != nil || len(sid) != 16 || hex.EncodeToString(sid) != r.GetSessionId() || r.GetSessionId() == "00000000000000000000000000000000" {
		return nil, status.Error(codes.InvalidArgument, "invalid audit identity")
	}
	recordWorkloadOwner(ctx, identity.Principal{Owner: owner, SessionID: r.GetSessionId()})
	id, err := file.ParseID(r.GetFileId())
	if err != nil {
		return nil, mapError(err)
	}
	record, err := h.service.Status(ctx, owner, id)
	if err != nil {
		return nil, mapError(err)
	}
	if record.State != file.Ready || (record.CleanFormat != file.PNG && record.CleanFormat != file.JPEG) || record.Manifest.Size > 5*1024*1024 || record.CleanSize <= 0 || record.CleanSize > 20*1024*1024 || record.CleanSHA256 == (file.Digest{}) {
		return nil, mapError(file.ErrNotReady)
	}
	return &filesv1.InspectOwnedAvatarResponse{CleanMediaType: string(record.CleanFormat), CleanSizeBytes: uint64(record.CleanSize), CleanSha256: record.CleanSHA256[:]}, nil
}
func (h *AvatarHandler) RetireOwnedAvatar(ctx context.Context, r *filesv1.RetireOwnedAvatarRequest) (*filesv1.RetireOwnedAvatarResponse, error) {
	owner, err := h.owner(ctx, r.GetSubjectId())
	if err != nil {
		return nil, err
	}
	id, err := file.ParseID(r.GetFileId())
	if err != nil {
		return nil, mapError(err)
	}
	err = h.service.Delete(ctx, owner, id)
	if err != nil && !errors.Is(err, file.ErrNotFound) {
		return nil, mapError(err)
	}
	return &filesv1.RetireOwnedAvatarResponse{}, nil
}
