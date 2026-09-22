package internalgrpc

import (
	"context"
	"encoding/hex"
	"io"
	"log/slog"
	"sync"

	filesv1 "github.com/v0hmly/marketmesh/api/gen/go/files/v1"
	"github.com/v0hmly/marketmesh/platform/workloadid"
	"github.com/v0hmly/marketmesh/services/files/internal/application/identity"
	"google.golang.org/grpc"
	"google.golang.org/grpc/status"
)

type auditPrincipalKey struct{}

// A request-local slot lets the outer interceptor observe authentication performed
// by the handler. Claims from incoming metadata are never used as audit identity.
type auditPrincipal struct {
	sync.Mutex
	principal identity.Principal
}

func recordAuditPrincipal(ctx context.Context, principal identity.Principal) {
	if slot, ok := ctx.Value(auditPrincipalKey{}).(*auditPrincipal); ok && principal.Owner.Valid() && principal.SessionID != "" {
		slot.Lock()
		defer slot.Unlock()
		slot.principal = principal
	}
}

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
		slot := &auditPrincipal{}
		ctx = context.WithValue(ctx, auditPrincipalKey{}, slot)
		response, err := next(ctx, request)
		operation, ok := operations[info.FullMethod]
		if !ok {
			return response, err
		}
		fields := []any{"operation", operation, "result", status.Code(err).String()}
		slot.Lock()
		principal := slot.principal
		slot.Unlock()
		if principal.Owner.Valid() {
			fields = append(fields, "subject_id", principal.Owner.Subject.String(), "tenant_id", principal.Owner.Tenant.String(), "session_id", principal.SessionID)
		}
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
