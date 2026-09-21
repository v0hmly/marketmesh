package internalgrpc

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"github.com/v0hmly/marketmesh/platform/workloadid"
	"github.com/v0hmly/marketmesh/services/files/internal/application/identity"
	"github.com/v0hmly/marketmesh/services/files/internal/domain/file"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
	"net/url"
	"strings"
	"testing"
	"time"
)

type verifyFunc func(context.Context, string) (identity.Principal, error)

func (f verifyFunc) Verify(ctx context.Context, token string) (identity.Principal, error) {
	return f(ctx, token)
}
func TestOwnerBoundary(t *testing.T) {
	scope := workloadid.Scope{TrustDomain: "files.test", Environment: "test", Cluster: "dc-a", Namespace: "marketmesh", ServiceAccount: "gateway-out"}
	calls := 0
	principal := identity.Principal{Owner: file.Owner{Tenant: file.ID{1}, Subject: file.ID{2}}, CanRead: true}
	h := &Handler{gateway: scope, verifier: verifyFunc(func(_ context.Context, token string) (identity.Principal, error) {
		calls++
		if token != "a.b.c" {
			t.Fatal("unexpected assertion")
		}
		return principal, nil
	})}
	ctx := func(raw string, md metadata.MD) context.Context {
		uri, _ := url.Parse(raw)
		leaf := &x509.Certificate{URIs: []*url.URL{uri}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour)}
		return metadata.NewIncomingContext(peer.NewContext(context.Background(), &peer.Peer{AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{HandshakeComplete: true, VerifiedChains: [][]*x509.Certificate{{leaf}}}}}), md)
	}
	for _, raw := range []string{"spiffe://files.test/test/gateway-out", strings.Replace(scope.String(), "dc-a", "dc-b", 1), strings.Replace(scope.String(), "marketmesh", "foreign", 1), strings.Replace(scope.String(), "env/test", "env/prod", 1)} {
		if _, err := h.owner(ctx(raw, metadata.Pairs(AssertionMetadata, "a.b.c")), false); status.Code(err) != codes.PermissionDenied {
			t.Fatal("foreign workload accepted")
		}
	}
	for _, md := range []metadata.MD{nil, metadata.Pairs(AssertionMetadata, "a.b.c", AssertionMetadata, "a.b.c"), metadata.Pairs(AssertionMetadata, "a.b.c", "cookie", "secret"), metadata.Pairs(AssertionMetadata, "a.b.c", "authorization", "forged"), metadata.Pairs(AssertionMetadata, strings.Repeat("x", 16385))} {
		if _, err := h.owner(ctx(scope.String(), md), false); status.Code(err) != codes.Unauthenticated {
			t.Fatal("untrusted metadata accepted")
		}
	}
	if calls != 0 {
		t.Fatal("unauthorized caller reached Auth")
	}
	valid := ctx(scope.String()+"/pod/01234567-89ab-cdef-0123-456789abcdef", metadata.Pairs(AssertionMetadata, "a.b.c"))
	if got, err := h.owner(valid, false); err != nil || got != principal.Owner {
		t.Fatal("verified owner lost")
	}
	if _, err := h.owner(valid, true); status.Code(err) != codes.PermissionDenied {
		t.Fatal("read scope permitted mutation")
	}
	principal.CanWrite = true
	if _, err := h.owner(valid, true); err != nil {
		t.Fatal(err)
	}
	h.verifier = verifyFunc(func(context.Context, string) (identity.Principal, error) {
		return identity.Principal{}, identity.ErrUnauthenticated
	})
	if _, err := h.owner(valid, false); status.Code(err) != codes.Unauthenticated {
		t.Fatal("unavailable Auth permitted access")
	}
}
