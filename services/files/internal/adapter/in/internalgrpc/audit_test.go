package internalgrpc

import (
	"bytes"
	"context"
	filesv1 "github.com/v0hmly/marketmesh/api/gen/go/files/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"strings"
	"testing"
)

func TestAuditNeverRecordsCapabilitiesOrErrors(t *testing.T) {
	var out bytes.Buffer
	secret := "https://storage.example/key?X-Amz-Signature=private"
	audit := AuditInterceptor(&out)
	_, _ = audit(context.Background(), &filesv1.CreateDownloadRequest{FileId: bytes.Repeat([]byte{1}, 16)}, &grpc.UnaryServerInfo{FullMethod: filesv1.FileService_CreateDownload_FullMethodName}, func(context.Context, any) (any, error) {
		return &filesv1.CreateDownloadResponse{Url: secret}, status.Error(codes.Unavailable, secret)
	})
	if strings.Contains(out.String(), "storage.example") || strings.Contains(out.String(), "Signature") || !strings.Contains(out.String(), "create_download") || !strings.Contains(out.String(), "Unavailable") {
		t.Fatal("unsafe or missing audit fields")
	}
}
