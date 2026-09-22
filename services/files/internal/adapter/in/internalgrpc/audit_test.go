package internalgrpc

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"net/url"
	"time"

	filesv1 "github.com/v0hmly/marketmesh/api/gen/go/files/v1"
	"github.com/v0hmly/marketmesh/platform/workloadid"
	"github.com/v0hmly/marketmesh/services/files/internal/application/identity"
	"github.com/v0hmly/marketmesh/services/files/internal/domain/file"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
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

func TestAuditRecordsOnlyVerifiedPrincipalIncludingDeniedAccess(t *testing.T) {
	scope := workloadid.Scope{TrustDomain: "files.test", Environment: "test", Cluster: "dc-a", Namespace: "marketmesh", ServiceAccount: "gateway-out"}
	uri, _ := url.Parse(scope.String())
	leaf := &x509.Certificate{URIs: []*url.URL{uri}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour)}
	ctx := metadata.NewIncomingContext(peer.NewContext(context.Background(), &peer.Peer{AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{HandshakeComplete: true, VerifiedChains: [][]*x509.Certificate{{leaf}}}}}), metadata.Pairs(AssertionMetadata, "forged.subject.session", "subject-id", "untrusted", "session-id", "untrusted"))
	for _, tc := range []struct {
		name      string
		verifyErr error
		allow     bool
		code      codes.Code
	}{
		{"success", nil, true, codes.OK},
		{"scope denied", nil, false, codes.PermissionDenied},
		{"revoked or forged", identity.ErrUnauthenticated, true, codes.Unauthenticated},
		{"Auth unavailable", errors.New("private Auth detail"), true, codes.Unavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			principal := identity.Principal{Owner: file.Owner{Tenant: file.ID{1}, Subject: file.ID{2}}, SessionID: "verified-session", CanRead: tc.allow}
			h := &Handler{gateway: scope, verifier: verifyFunc(func(context.Context, string) (identity.Principal, error) {
				// Even a nonempty principal accompanying an error must not be logged.
				return principal, tc.verifyErr
			})}
			var out bytes.Buffer
			_, err := AuditInterceptor(&out)(ctx, &filesv1.CreateDownloadRequest{FileId: bytes.Repeat([]byte{3}, 16)}, &grpc.UnaryServerInfo{FullMethod: filesv1.FileService_CreateDownload_FullMethodName}, func(ctx context.Context, _ any) (any, error) {
				_, err := h.owner(ctx, false)
				return &filesv1.CreateDownloadResponse{Url: "https://private/capability"}, err
			})
			if status.Code(err) != tc.code {
				t.Fatal(err)
			}
			var event map[string]any
			if err := json.Unmarshal(out.Bytes(), &event); err != nil {
				t.Fatal(err)
			}
			if event["operation"] != "create_download" || event["result"] != tc.code.String() || event["workload"] != scope.String() || event["file_id"] != strings.Repeat("03", 16) {
				t.Fatal("missing audit fields", event)
			}
			if tc.verifyErr == nil {
				if event["subject_id"] != principal.Owner.Subject.String() || event["tenant_id"] != principal.Owner.Tenant.String() || event["session_id"] != principal.SessionID {
					t.Fatal("missing verified identity", event)
				}
			} else {
				for _, field := range []string{"subject_id", "tenant_id", "session_id"} {
					if _, exists := event[field]; exists {
						t.Fatal("unverified identity recorded", field)
					}
				}
			}
			for _, secret := range []string{"forged", "untrusted", "private", "capability"} {
				if strings.Contains(out.String(), secret) {
					t.Fatal("untrusted data leaked")
				}
			}
		})
	}
}
