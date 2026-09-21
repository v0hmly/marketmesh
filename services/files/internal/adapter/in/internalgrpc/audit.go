package internalgrpc

import (
	"context"
	"encoding/hex"
	filesv1 "github.com/v0hmly/marketmesh/api/gen/go/files/v1"
	"github.com/v0hmly/marketmesh/platform/workloadid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/status"
	"io"
	"log/slog"
)

// AuditInterceptor emits a finite control audit without request/response bodies,
// bearer capabilities, user credentials, original filenames or raw errors.
func AuditInterceptor(output io.Writer) grpc.UnaryServerInterceptor {
	log := slog.New(slog.NewJSONHandler(output, nil))
	operations := map[string]string{
		filesv1.FileService_CreateUpload_FullMethodName:   "create_upload",
		filesv1.FileService_CompleteUpload_FullMethodName: "complete_upload",
		filesv1.FileService_GetStatus_FullMethodName:      "get_status",
		filesv1.FileService_CreateDownload_FullMethodName: "create_download",
		filesv1.FileService_Delete_FullMethodName:         "delete",
	}
	return func(ctx context.Context, request any, info *grpc.UnaryServerInfo, next grpc.UnaryHandler) (any, error) {
		response, err := next(ctx, request)
		operation, ok := operations[info.FullMethod]
		if !ok {
			return response, err
		}
		fields := []any{"operation", operation, "result", status.Code(err).String()}
		if scope, pod, scopeErr := workloadid.ScopedFromContext(ctx); scopeErr == nil {
			fields = append(fields, "workload", scope.String(), "pod_uid", pod)
		}
		var id []byte
		if r, ok := request.(interface{ GetFileId() []byte }); ok {
			id = r.GetFileId()
		}
		if r, ok := response.(*filesv1.CreateUploadResponse); ok {
			id = r.GetFileId()
		}
		if len(id) == 16 {
			fields = append(fields, "file_id", hex.EncodeToString(id))
		}
		log.InfoContext(ctx, "file control", fields...)
		return response, err
	}
}
