package tunnel

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/url"
	"testing"
	"time"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
)

func TestPeerAuthorizer(t *testing.T) {
	t.Parallel()

	allowedURI, err := url.Parse(testPeerURI)
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	otherURI, err := url.Parse("spiffe://marketmesh.test/user/instance-1")
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	now := time.Now()

	tests := []struct {
		name        string
		certificate *x509.Certificate
		withTLS     bool
		wantError   bool
	}{
		{
			name: "authorized gateway-out client purpose",
			certificate: &x509.Certificate{
				Raw:         []byte{1},
				URIs:        []*url.URL{allowedURI},
				ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
				NotBefore:   now.Add(-time.Minute),
				NotAfter:    now.Add(time.Minute),
			},
			withTLS: true,
		},
		{
			name: "valid chain with wrong role",
			certificate: &x509.Certificate{
				Raw:         []byte{2},
				URIs:        []*url.URL{otherURI},
				ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
				NotBefore:   now.Add(-time.Minute),
				NotAfter:    now.Add(time.Minute),
			},
			withTLS:   true,
			wantError: true,
		},
		{
			name: "wrong certificate purpose",
			certificate: &x509.Certificate{
				Raw:         []byte{3},
				URIs:        []*url.URL{allowedURI},
				ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
				NotBefore:   now.Add(-time.Minute),
				NotAfter:    now.Add(time.Minute),
			},
			withTLS:   true,
			wantError: true,
		},
		{
			name: "expired certificate",
			certificate: &x509.Certificate{
				Raw:         []byte{4},
				URIs:        []*url.URL{allowedURI},
				ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
				NotBefore:   now.Add(-2 * time.Minute),
				NotAfter:    now.Add(-time.Minute),
			},
			withTLS:   true,
			wantError: true,
		},
		{
			name: "multiple identities",
			certificate: &x509.Certificate{
				Raw:         []byte{5},
				URIs:        []*url.URL{allowedURI, otherURI},
				ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
				NotBefore:   now.Add(-time.Minute),
				NotAfter:    now.Add(time.Minute),
			},
			withTLS:   true,
			wantError: true,
		},
		{
			name:      "missing tls peer",
			wantError: true,
		},
	}

	authorizer, err := newPeerAuthorizer(PeerPolicy{AllowedURIs: []string{testPeerURI}})
	if err != nil {
		t.Fatalf("newPeerAuthorizer() error = %v", err)
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			if test.withTLS {
				ctx = peer.NewContext(ctx, &peer.Peer{
					AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{
						PeerCertificates: []*x509.Certificate{test.certificate},
						VerifiedChains:   [][]*x509.Certificate{{test.certificate}},
					}},
				})
			}
			err := authorizer.authorize(ctx)
			if test.wantError && err == nil {
				t.Fatal("authorize() error = nil, want rejection")
			}
			if !test.wantError && err != nil {
				t.Fatalf("authorize() error = %v", err)
			}
		})
	}
}
