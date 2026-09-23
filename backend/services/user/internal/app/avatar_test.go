package app

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	filesv1 "github.com/v0hmly/marketmesh/api/gen/go/files/v1"
	"github.com/v0hmly/marketmesh/platform/workloadid"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/status"
)

type avatarProbe struct {
	filesv1.UnimplementedFileAvatarServiceServer
}

func (*avatarProbe) InspectOwnedAvatar(context.Context, *filesv1.InspectOwnedAvatarRequest) (*filesv1.InspectOwnedAvatarResponse, error) {
	return &filesv1.InspectOwnedAvatarResponse{CleanMediaType: "image/png"}, nil
}

func TestAvatarConnectionAuthenticatesRealFilesScope(t *testing.T) {
	for _, name := range []string{"allowed", "foreign-cluster", "foreign-role", "foreign-ca"} {
		t.Run(name, func(t *testing.T) {
			pki := newProfilePKI(t)
			user := workloadid.Scope{TrustDomain: "marketmesh.test", Environment: "test", Cluster: "dc-a", Namespace: "marketmesh", ServiceAccount: "user"}
			files := user
			files.ServiceAccount = "files"
			actual := files
			issuer := pki
			switch name {
			case "foreign-cluster":
				actual.Cluster = "dc-b"
			case "foreign-role":
				actual.ServiceAccount = "auth"
			case "foreign-ca":
				issuer = newProfilePKI(t)
			}
			usages := []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth}
			serverCert, _, _ := issuer.leaf(t, actual.String(), usages)
			_, cert, key := pki.leaf(t, user.String(), usages)
			roots := x509.NewCertPool()
			roots.AppendCertsFromPEM(pki.caPEM)
			server := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{serverCert}, ClientCAs: roots, ClientAuth: tls.RequireAndVerifyClientCert})))
			filesv1.RegisterFileAvatarServiceServer(server, &avatarProbe{})
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(server.Stop)
			t.Cleanup(func() { _ = listener.Close() })
			go func() { _ = server.Serve(listener) }()
			dir := t.TempDir()
			for name, data := range map[string][]byte{"cert": cert, "key": key, "ca": pki.caPEM} {
				if err := os.WriteFile(filepath.Join(dir, name), data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			conn, err := newAvatarConnection(avatarConfig{target: listener.Addr().String(), serverName: "localhost", cert: filepath.Join(dir, "cert"), key: filepath.Join(dir, "key"), ca: filepath.Join(dir, "ca"), ownURI: user.String(), filesURI: files.String()}, "test")
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = conn.Close() })
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			_, err = filesv1.NewFileAvatarServiceClient(conn).InspectOwnedAvatar(ctx, &filesv1.InspectOwnedAvatarRequest{})
			if name == "allowed" {
				if err != nil {
					t.Fatal("valid Files rejected", status.Code(err))
				}
			} else if status.Code(err) != codes.Unavailable && status.Code(err) != codes.DeadlineExceeded {
				t.Fatal("foreign Files accepted", status.Code(err))
			}
		})
	}
}
