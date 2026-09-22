package app

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	filesv1 "github.com/v0hmly/marketmesh/api/gen/go/files/v1"
	"github.com/v0hmly/marketmesh/platform/workloadid"
	"github.com/v0hmly/marketmesh/services/files/internal/adapter/in/internalgrpc"
	"github.com/v0hmly/marketmesh/services/files/internal/application/files"
	"github.com/v0hmly/marketmesh/services/files/internal/domain/file"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
)

type avatarBoundaryRepository struct {
	files.Repository
	calls atomic.Int32
}

func (r *avatarBoundaryRepository) Get(_ context.Context, owner file.Owner, id file.ID) (file.Record, error) {
	r.calls.Add(1)
	return file.Record{ID: id, Owner: owner, State: file.Ready, Manifest: file.Manifest{Size: 100}, CleanFormat: file.PNG, CleanSize: 100, CleanSHA256: file.Digest{1}}, nil
}

type avatarBoundaryStorage struct{ files.Storage }

func TestAvatarRealMTLSBoundaryAndSeparateRegistration(t *testing.T) {
	pki := newProfilePKI(t)
	scope := workloadid.Scope{TrustDomain: "marketmesh.test", Environment: "test", Cluster: "dc-a", Namespace: "marketmesh", ServiceAccount: "files"}
	user, gateway := scope, scope
	user.ServiceAccount = "user"
	gateway.ServiceAccount = "gateway-out"
	usages := []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth}
	cert, certPEM, keyPEM := pki.leaf(t, scope.String(), usages)
	dir := t.TempDir()
	for name, data := range map[string][]byte{"cert": certPEM, "key": keyPEM, "ca": pki.caPEM} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	cfg := controlConfig{Own: scope, TLS: tlsFiles{Certificate: filepath.Join(dir, "cert"), PrivateKey: filepath.Join(dir, "key"), RootCA: filepath.Join(dir, "ca")}, Avatar: avatarConfig{Enabled: true, Address: "127.0.0.1:0", User: user}}
	repo := &avatarBoundaryRepository{}
	service, err := files.New(repo, avatarBoundaryStorage{}, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	server, listener, err := avatarServer(cfg, service)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Stop)
	t.Cleanup(func() { _ = listener.Close() })
	go func() { _ = server.Serve(listener) }()
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(pki.caPEM)
	dial := func(address string, identity workloadid.Scope, issuer profilePKI) *grpc.ClientConn {
		leaf, _, _ := issuer.leaf(t, identity.String(), usages)
		conn, e := grpc.NewClient(address, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, Certificates: []tls.Certificate{leaf}, ServerName: "localhost"})), grpc.WithDisableRetry())
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { _ = conn.Close() })
		return conn
	}
	id := file.ID{1}
	request := &filesv1.InspectOwnedAvatarRequest{SubjectId: id[:], FileId: id[:], SessionId: "01010101010101010101010101010101"}
	good := dial(listener.Addr().String(), user, pki)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	response, err := filesv1.NewFileAvatarServiceClient(good).InspectOwnedAvatar(ctx, request)
	if err != nil || response.GetCleanMediaType() != "image/png" {
		t.Fatal("authorized User denied", status.Code(err))
	}
	if _, err := filesv1.NewFileServiceClient(good).GetStatus(ctx, &filesv1.GetStatusRequest{FileId: id[:]}); status.Code(err) != codes.Unimplemented {
		t.Fatal("public API exposed on avatar listener", status.Code(err))
	}
	for _, name := range []string{"gateway", "environment", "cluster", "namespace", "untrusted-ca"} {
		t.Run(name, func(t *testing.T) {
			identity, issuer := user, pki
			switch name {
			case "gateway":
				identity = gateway
			case "environment":
				identity.Environment = "production"
			case "cluster":
				identity.Cluster = "dc-b"
			case "namespace":
				identity.Namespace = "other"
			case "untrusted-ca":
				issuer = newProfilePKI(t)
			}
			conn := dial(listener.Addr().String(), identity, issuer)
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			_, err := filesv1.NewFileAvatarServiceClient(conn).InspectOwnedAvatar(ctx, request)
			if status.Code(err) != codes.Unavailable && status.Code(err) != codes.DeadlineExceeded {
				t.Fatal("untrusted handshake not rejected", status.Code(err))
			}
		})
	}
	if repo.calls.Load() != 1 {
		t.Fatal("rejected identity reached repository")
	}
	// Execute the exact public-server registration used by serveFiles.
	publicTLS, err := workloadid.ScopedTLS(cert, roots, scope, gateway, "", true)
	if err != nil {
		t.Fatal(err)
	}
	public := controlServer(publicTLS, &internalgrpc.Handler{}, func(context.Context) error { return nil })
	publicListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(public.Stop)
	t.Cleanup(func() { _ = publicListener.Close() })
	go func() { _ = public.Serve(publicListener) }()
	conn := dial(publicListener.Addr().String(), gateway, pki)
	_, err = filesv1.NewFileAvatarServiceClient(conn).InspectOwnedAvatar(ctx, request)
	if status.Code(err) != codes.Unimplemented {
		t.Fatal("private avatar API exposed on public Files listener", status.Code(err))
	}
}
