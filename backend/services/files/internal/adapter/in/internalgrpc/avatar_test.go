package internalgrpc

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/url"
	"testing"
	"time"

	filesv1 "github.com/v0hmly/marketmesh/api/gen/go/files/v1"
	"github.com/v0hmly/marketmesh/platform/workloadid"
	"github.com/v0hmly/marketmesh/services/files/internal/application/files"
	"github.com/v0hmly/marketmesh/services/files/internal/domain/file"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

type avatarRepo struct {
	files.Repository
	record file.Record
	calls  int
}

func (r *avatarRepo) Get(_ context.Context, owner file.Owner, id file.ID) (file.Record, error) {
	r.calls++
	if owner != r.record.Owner || id != r.record.ID {
		return file.Record{}, file.ErrNotFound
	}
	return r.record, nil
}
func (r *avatarRepo) Transition(_ context.Context, record file.Record, state file.State) (file.Record, error) {
	r.record.State = state
	return r.record, nil
}

type avatarStorage struct{ files.Storage }

func avatarContext(scope workloadid.Scope, md metadata.MD) context.Context {
	uri, _ := url.Parse(scope.String())
	leaf := &x509.Certificate{URIs: []*url.URL{uri}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour)}
	return metadata.NewIncomingContext(peer.NewContext(context.Background(), &peer.Peer{AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{HandshakeComplete: true, VerifiedChains: [][]*x509.Certificate{{leaf}}}}}), md)
}
func TestAvatarRequiresExactUserOwnerAndReadyBoundedRaster(t *testing.T) {
	scope := workloadid.Scope{TrustDomain: "files.test", Environment: "test", Cluster: "dc-a", Namespace: "marketmesh", ServiceAccount: "user"}
	owner := file.Owner{Tenant: file.ID{1}, Subject: file.ID{1}}
	record := file.Record{ID: file.ID{2}, Owner: owner, State: file.Ready, Manifest: file.Manifest{Size: 1024}, CleanFormat: file.PNG, CleanSize: 2048, CleanSHA256: file.Digest{3}}
	repo := &avatarRepo{record: record}
	service, _ := files.New(repo, avatarStorage{}, time.Now)
	h, _ := NewAvatar(service, scope)
	request := &filesv1.InspectOwnedAvatarRequest{SubjectId: owner.Subject[:], FileId: record.ID[:], SessionId: "01010101010101010101010101010101"}
	for _, change := range []func(*workloadid.Scope){func(s *workloadid.Scope) { s.ServiceAccount = "gateway-out" }, func(s *workloadid.Scope) { s.Environment = "production" }, func(s *workloadid.Scope) { s.Cluster = "dc-b" }, func(s *workloadid.Scope) { s.Namespace = "other" }} {
		bad := scope
		change(&bad)
		if _, err := h.InspectOwnedAvatar(avatarContext(bad, nil), request); status.Code(err) != codes.PermissionDenied {
			t.Fatal("foreign workload accepted", err)
		}
	}
	if repo.calls != 0 {
		t.Fatal("unauthorized request reached repository")
	}
	ctx := avatarContext(scope, nil)
	for _, md := range []metadata.MD{metadata.Pairs("cookie", "secret"), metadata.Pairs("authorization", "secret"), metadata.Pairs(AssertionMetadata, "a.b.c")} {
		if _, err := h.InspectOwnedAvatar(avatarContext(scope, md), request); status.Code(err) != codes.Unauthenticated {
			t.Fatal("external credential accepted", err)
		}
	}
	foreign := file.ID{9}
	request.SubjectId = foreign[:]
	if _, err := h.InspectOwnedAvatar(ctx, request); status.Code(err) != codes.NotFound {
		t.Fatal("foreign file accepted", err)
	}
	request.SubjectId = owner.Subject[:]
	for _, change := range []func(*file.Record){func(r *file.Record) { r.State = file.Scanning }, func(r *file.Record) { r.State = file.Deleted }, func(r *file.Record) { r.CleanFormat = file.PDF }, func(r *file.Record) { r.Manifest.Size = 5*1024*1024 + 1 }, func(r *file.Record) { r.CleanSize = 20*1024*1024 + 1 }} {
		repo.record = record
		change(&repo.record)
		if _, err := h.InspectOwnedAvatar(ctx, request); status.Code(err) != codes.FailedPrecondition {
			t.Fatal("unsafe avatar accepted", err)
		}
	}
	repo.record = record
	var out bytes.Buffer
	response, err := AuditInterceptor(&out)(ctx, request, &grpc.UnaryServerInfo{FullMethod: filesv1.FileAvatarService_InspectOwnedAvatar_FullMethodName}, func(ctx context.Context, r any) (any, error) {
		return h.InspectOwnedAvatar(ctx, r.(*filesv1.InspectOwnedAvatarRequest))
	})
	if err != nil || response.(*filesv1.InspectOwnedAvatarResponse).CleanSizeBytes != 2048 {
		t.Fatal(err)
	}
	var event map[string]any
	if err = json.Unmarshal(out.Bytes(), &event); err != nil || event["subject_id"] != owner.Subject.String() || event["session_id"] != request.SessionId || event["operation"] != "inspect_avatar" {
		t.Fatal("audit identity lost", event, err)
	}
	for range 2 {
		if _, err = h.RetireOwnedAvatar(ctx, &filesv1.RetireOwnedAvatarRequest{SubjectId: owner.Subject[:], FileId: record.ID[:]}); err != nil {
			t.Fatal(err)
		}
	}
	if repo.record.State != file.Deleted {
		t.Fatal("retirement not durable")
	}

}
