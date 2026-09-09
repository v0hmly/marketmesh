package internalgrpc

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"io"
	"math/big"
	"net"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	userv1 "github.com/v0hmly/marketmesh/api/gen/go/user/v1"
	"github.com/v0hmly/marketmesh/platform/logger"
	"github.com/v0hmly/marketmesh/platform/workloadid"
	"github.com/v0hmly/marketmesh/services/user/internal/application/getme"
	"github.com/v0hmly/marketmesh/services/user/internal/application/identity"
	"github.com/v0hmly/marketmesh/services/user/internal/application/updateme"
	"github.com/v0hmly/marketmesh/services/user/internal/domain/profile"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type testStore struct{ calls atomic.Int32 }

func (s *testStore) Get(_ context.Context, id profile.SubjectID) (profile.Profile, error) {
	s.calls.Add(1)
	return profile.Profile{SubjectID: id, Version: 1, Fields: profile.Fields{DisplayName: "private name"}}, nil
}
func (s *testStore) Update(_ context.Context, id profile.SubjectID, fields profile.Fields, version uint64) (profile.Profile, error) {
	s.calls.Add(1)
	return profile.Profile{SubjectID: id, Fields: fields, Version: version + 1}, nil
}

type testVerifier struct{}

func (testVerifier) Verify(_ context.Context, token string) (identity.Principal, error) {
	if token == "revoked" {
		return identity.Principal{}, identity.ErrUnauthenticated
	}
	return identity.Principal{SubjectID: profile.SubjectID{1}, CanRead: token != "write", CanWrite: token != "read"}, nil
}
func TestWorkloadAndProfileBoundary(t *testing.T) {
	store := &testStore{}
	get, _ := getme.New(store)
	update, _ := updateme.New(store)
	policy, err := workloadid.NewPolicy(map[workloadid.Identity][]string{{TrustDomain: "marketmesh.test", Environment: "dev", Role: "gateway-out"}: {getMethod, updateMethod}})
	if err != nil {
		t.Fatal(err)
	}
	log, err := logger.New(logger.Config{Service: "user", Version: "test", Environment: "local", Level: "info", Output: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := New(get, update, testVerifier{}, policy, log)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = handler.GetMe(context.Background(), &userv1.GetMeRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Fatal(err)
	}
	ca, key := certificateAuthority(t)
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.Creds(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{issueCertificate(t, ca, key, "", true)}, ClientCAs: roots, ClientAuth: tls.RequireAndVerifyClientCert})), grpc.UnaryInterceptor(workloadid.UnaryServerInterceptor(policy)))
	userv1.RegisterUserServiceServer(server, handler)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)
	client := func(role string) userv1.UserServiceClient {
		cfg := &tls.Config{MinVersion: tls.VersionTLS13, ServerName: "localhost", RootCAs: roots}
		if role != "" {
			cfg.Certificates = []tls.Certificate{issueCertificate(t, ca, key, role, false)}
		}
		conn, err := grpc.NewClient(listener.Addr().String(), grpc.WithTransportCredentials(credentials.NewTLS(cfg)))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		return userv1.NewUserServiceClient(conn)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, role := range []string{"", "spiffe://marketmesh.test/dev/gateway-in", "spiffe://marketmesh.test/prod/gateway-out", "spiffe://other.test/dev/gateway-out"} {
		if _, err = client(role).GetMe(metadata.NewOutgoingContext(ctx, metadata.Pairs(AssertionMetadata, "valid")), &userv1.GetMeRequest{}); err == nil {
			t.Fatal("unauthorized workload accepted")
		}
	}
	if store.calls.Load() != 0 {
		t.Fatal("unauthorized request reached repository")
	}
	gateway := client("spiffe://marketmesh.test/dev/gateway-out")
	for _, md := range []metadata.MD{nil, metadata.Pairs(AssertionMetadata, ""), metadata.Pairs(AssertionMetadata, "one", AssertionMetadata, "two"), metadata.Pairs(AssertionMetadata, "valid", "cookie", "opaque"), metadata.Pairs(AssertionMetadata, "valid", "authorization", "Bearer opaque"), metadata.Pairs(AssertionMetadata, strings.Repeat("x", 16385)), metadata.Pairs(AssertionMetadata, "revoked")} {
		var headers metadata.MD
		_, err = gateway.GetMe(metadata.NewOutgoingContext(ctx, md), &userv1.GetMeRequest{}, grpc.Header(&headers))
		if status.Code(err) != codes.Unauthenticated || strings.Join(headers.Get("cache-control"), ",") != "no-store" {
			t.Fatalf("invalid request result=%v headers=%v", err, headers)
		}
	}
	var headers metadata.MD
	response, err := gateway.GetMe(metadata.NewOutgoingContext(ctx, metadata.Pairs(AssertionMetadata, "read")), &userv1.GetMeRequest{}, grpc.Header(&headers))
	if err != nil || response.GetProfile().GetSubjectId()[0] != 1 || strings.Join(headers.Get("cache-control"), ",") != "no-store" {
		t.Fatalf("get=%v %v", response, err)
	}
	updateRequest := &userv1.UpdateMeRequest{DisplayName: "changed", Bio: "private", ExpectedVersion: 1}
	if _, err = gateway.UpdateMe(metadata.NewOutgoingContext(ctx, metadata.Pairs(AssertionMetadata, "read")), updateRequest); status.Code(err) != codes.PermissionDenied {
		t.Fatal(err)
	}
	changed, err := gateway.UpdateMe(metadata.NewOutgoingContext(ctx, metadata.Pairs(AssertionMetadata, "write")), updateRequest)
	if err != nil || changed.GetProfile().GetVersion() != 2 || changed.GetProfile().GetDisplayName() != "changed" {
		t.Fatalf("update=%v %v", changed, err)
	}
	if _, err = gateway.GetMe(metadata.NewOutgoingContext(ctx, metadata.Pairs(AssertionMetadata, "write")), &userv1.GetMeRequest{}); status.Code(err) != codes.PermissionDenied {
		t.Fatal(err)
	}
}
func certificateAuthority(t *testing.T) (*x509.Certificate, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, template, template, pub, priv)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, priv
}
func issueCertificate(t *testing.T, ca *x509.Certificate, caKey ed25519.PrivateKey, identity string, server bool) tls.Certificate {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: serial, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	if server {
		template.DNSNames = []string{"localhost"}
		template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	} else {
		u, err := url.Parse(identity)
		if err != nil {
			t.Fatal(err)
		}
		template.URIs = []*url.URL{u}
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca, pub, caKey)
	if err != nil {
		t.Fatal(err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv}
}
