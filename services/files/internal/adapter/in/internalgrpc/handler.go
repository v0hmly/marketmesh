// Package internalgrpc exposes only bounded authenticated file control operations.
package internalgrpc

import (
	"context"
	"errors"
	"slices"
	"strings"

	filesv1 "github.com/v0hmly/marketmesh/api/gen/go/files/v1"
	"github.com/v0hmly/marketmesh/platform/workloadid"
	"github.com/v0hmly/marketmesh/services/files/internal/application/files"
	"github.com/v0hmly/marketmesh/services/files/internal/application/identity"
	"github.com/v0hmly/marketmesh/services/files/internal/domain/file"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

const AssertionMetadata = "marketmesh-session-assertion-bin"

type Verifier interface {
	Verify(context.Context, string) (identity.Principal, error)
}
type Handler struct {
	filesv1.UnimplementedFileServiceServer
	service  *files.Service
	verifier Verifier
	gateway  workloadid.Scope
}

func New(service *files.Service, verifier Verifier, gateway workloadid.Scope) (*Handler, error) {
	if service == nil || verifier == nil || gateway.Validate() != nil || gateway.ServiceAccount != "gateway-out" {
		return nil, file.ErrInvalid
	}
	return &Handler{service: service, verifier: verifier, gateway: gateway}, nil
}
func (h *Handler) owner(ctx context.Context, write bool) (file.Owner, error) {
	_ = grpc.SetHeader(ctx, metadata.Pairs("cache-control", "no-store"))
	workload, _, err := workloadid.ScopedFromContext(ctx)
	if err != nil || workload != h.gateway {
		return file.Owner{}, mapError(identity.ErrForbidden)
	}
	md, _ := metadata.FromIncomingContext(ctx)
	tokens := md.Get(AssertionMetadata)
	if len(tokens) != 1 || len(tokens[0]) == 0 || len(tokens[0]) > 16*1024 || strings.ContainsAny(tokens[0], " \t\r\n;=") || len(md.Get("cookie")) != 0 || len(md.Get("authorization")) != 0 {
		return file.Owner{}, mapError(identity.ErrUnauthenticated)
	}
	principal, err := h.verifier.Verify(ctx, tokens[0])
	if err != nil {
		return file.Owner{}, mapError(err)
	}
	if !principal.Owner.Valid() || (write && !principal.CanWrite) || (!write && !principal.CanRead) {
		return file.Owner{}, mapError(identity.ErrForbidden)
	}
	return principal.Owner, nil
}
func (h *Handler) CreateUpload(ctx context.Context, r *filesv1.CreateUploadRequest) (*filesv1.CreateUploadResponse, error) {
	owner, err := h.owner(ctx, true)
	if err != nil {
		return nil, err
	}
	if r == nil || r.SizeBytes > file.MaxSize || len(r.Sha256) != 32 || len(r.Parts) > file.MaxParts {
		return nil, mapError(file.ErrInvalid)
	}
	key, err := file.ParseID(r.IdempotencyKey)
	if err != nil {
		return nil, mapError(err)
	}
	m := file.Manifest{Format: file.Format(r.MediaType), Size: int64(r.SizeBytes), SHA256: file.Digest(r.Sha256)}
	for _, part := range r.Parts {
		if part == nil || part.SizeBytes > file.PartSize || len(part.Sha256) != 32 {
			return nil, mapError(file.ErrInvalid)
		}
		m.Parts = append(m.Parts, file.Part{Size: int64(part.SizeBytes), SHA256: file.Digest(part.Sha256)})
	}
	upload, err := h.service.Create(ctx, owner, key, m)
	if err != nil {
		return nil, mapError(err)
	}
	response := &filesv1.CreateUploadResponse{FileId: upload.File.ID[:], UploadId: upload.File.UploadID, State: wireState(upload.File.State), UploadExpiresAtUnix: upload.File.ExpiresAt.Unix()}
	for _, number := range upload.Received {
		response.ReceivedParts = append(response.ReceivedParts, uint32(number))
	}
	for _, cap := range upload.Parts {
		if cap.Method != "PUT" || cap.PartNumber <= 0 || int(cap.PartNumber) > len(m.Parts) {
			return nil, mapError(file.ErrUnavailable)
		}
		part := &filesv1.UploadPartCapability{PartNumber: uint32(cap.PartNumber), OffsetBytes: uint64(cap.PartNumber-1) * file.PartSize, SizeBytes: uint64(m.Parts[cap.PartNumber-1].Size), Url: cap.URL, ExpiresAtUnix: cap.ExpiresAt.Unix()}
		var names []string
		for name := range cap.Headers {
			names = append(names, name)
		}
		slices.Sort(names)
		for _, name := range names {
			part.Headers = append(part.Headers, &filesv1.CapabilityHeader{Name: name, Value: cap.Headers[name]})
		}
		response.Parts = append(response.Parts, part)
	}
	return response, nil
}
func (h *Handler) CompleteUpload(ctx context.Context, r *filesv1.CompleteUploadRequest) (*filesv1.CompleteUploadResponse, error) {
	owner, err := h.owner(ctx, true)
	if err != nil {
		return nil, err
	}
	id, err := file.ParseID(r.GetFileId())
	if err != nil {
		return nil, mapError(err)
	}
	record, err := h.service.Complete(ctx, owner, id)
	if err != nil {
		return nil, mapError(err)
	}
	return &filesv1.CompleteUploadResponse{State: wireState(record.State)}, nil
}
func (h *Handler) GetStatus(ctx context.Context, r *filesv1.GetStatusRequest) (*filesv1.GetStatusResponse, error) {
	owner, err := h.owner(ctx, false)
	if err != nil {
		return nil, err
	}
	id, err := file.ParseID(r.GetFileId())
	if err != nil {
		return nil, mapError(err)
	}
	record, err := h.service.Status(ctx, owner, id)
	if err != nil {
		return nil, mapError(err)
	}
	response := &filesv1.GetStatusResponse{State: wireState(record.State)}
	if record.State == file.Ready {
		response.CleanMediaType = string(record.CleanFormat)
		response.CleanSizeBytes = uint64(record.CleanSize)
		response.CleanSha256 = record.CleanSHA256[:]
	}
	return response, nil
}
func (h *Handler) CreateDownload(ctx context.Context, r *filesv1.CreateDownloadRequest) (*filesv1.CreateDownloadResponse, error) {
	owner, err := h.owner(ctx, false)
	if err != nil {
		return nil, err
	}
	id, err := file.ParseID(r.GetFileId())
	if err != nil {
		return nil, mapError(err)
	}
	cap, err := h.service.Download(ctx, owner, id)
	if err != nil {
		return nil, mapError(err)
	}
	return &filesv1.CreateDownloadResponse{Url: cap.URL, ExpiresAtUnix: cap.ExpiresAt.Unix()}, nil
}
func (h *Handler) Delete(ctx context.Context, r *filesv1.DeleteRequest) (*filesv1.DeleteResponse, error) {
	owner, err := h.owner(ctx, true)
	if err != nil {
		return nil, err
	}
	id, err := file.ParseID(r.GetFileId())
	if err != nil {
		return nil, mapError(err)
	}
	if err = h.service.Delete(ctx, owner, id); err != nil {
		return nil, mapError(err)
	}
	return &filesv1.DeleteResponse{}, nil
}
func wireState(state file.State) filesv1.FileState {
	switch state {
	case file.Uploading:
		return filesv1.FileState_FILE_STATE_UPLOADING
	case file.Scanning:
		return filesv1.FileState_FILE_STATE_SCANNING
	case file.Replicating:
		return filesv1.FileState_FILE_STATE_REPLICATING
	case file.Ready:
		return filesv1.FileState_FILE_STATE_READY
	case file.Rejected:
		return filesv1.FileState_FILE_STATE_REJECTED
	case file.Expired:
		return filesv1.FileState_FILE_STATE_EXPIRED
	case file.Deleted:
		return filesv1.FileState_FILE_STATE_DELETED
	default:
		return filesv1.FileState_FILE_STATE_UNSPECIFIED
	}
}
func mapError(err error) error {
	switch {
	case errors.Is(err, identity.ErrUnauthenticated):
		return status.Error(codes.Unauthenticated, "authentication failed")
	case errors.Is(err, identity.ErrForbidden):
		return status.Error(codes.PermissionDenied, "permission denied")
	case errors.Is(err, file.ErrInvalid):
		return status.Error(codes.InvalidArgument, "invalid file request")
	case errors.Is(err, file.ErrLimit):
		return status.Error(codes.ResourceExhausted, "file quota reached")
	case errors.Is(err, file.ErrNotFound):
		return status.Error(codes.NotFound, "file not found")
	case errors.Is(err, file.ErrConflict):
		return status.Error(codes.Aborted, "file state conflict")
	case errors.Is(err, file.ErrNotReady), errors.Is(err, file.ErrRejected):
		return status.Error(codes.FailedPrecondition, "file unavailable")
	case errors.Is(err, context.Canceled):
		return status.Error(codes.Canceled, "request canceled")
	case errors.Is(err, context.DeadlineExceeded):
		return status.Error(codes.DeadlineExceeded, "request timeout")
	default:
		return status.Error(codes.Unavailable, "file service unavailable")
	}
}
