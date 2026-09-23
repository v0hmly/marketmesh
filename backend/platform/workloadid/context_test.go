package workloadid

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/url"
	"testing"
	"time"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
)

// nonTLSAuthInfo — AuthInfo без TLS, чтобы проверить отказ на не-TLS типе.
type nonTLSAuthInfo struct{}

func (nonTLSAuthInfo) AuthType() string { return "insecure" }

func rawStateContext(state tls.ConnectionState) context.Context {
	return peer.NewContext(context.Background(), &peer.Peer{
		AuthInfo: credentials.TLSInfo{State: state},
	})
}

func TestFromContextValidPeer(t *testing.T) {
	ca := newTestCA(t)
	leaf := ca.issueLeaf(t, testLeafConfig{})

	identity, certificate, err := FromContext(tlsPeerContext(leaf, leaf))
	if err != nil {
		t.Fatalf("FromContext: %v", err)
	}
	want := Identity{TrustDomain: "marketmesh.test", Environment: "prod", Role: "gateway-in"}
	if identity != want {
		t.Fatalf("got identity %+v, want %+v", identity, want)
	}
	if certificate != leaf {
		t.Fatal("returned certificate is not the peer leaf")
	}
}

func TestFromContextServerAuthVariant(t *testing.T) {
	ca := newTestCA(t)
	serverLeaf := ca.issueLeaf(t, testLeafConfig{
		eku: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})

	t.Run("server eku accepted with explicit option", func(t *testing.T) {
		_, _, err := FromContext(
			tlsPeerContext(serverLeaf, serverLeaf),
			WithPeerExtKeyUsage(x509.ExtKeyUsageServerAuth),
		)
		if err != nil {
			t.Fatalf("FromContext: %v", err)
		}
	})

	t.Run("server eku rejected by default client check", func(t *testing.T) {
		_, _, err := FromContext(tlsPeerContext(serverLeaf, serverLeaf))
		if !errors.Is(err, ErrUnauthenticated) {
			t.Fatalf("got %v, want ErrUnauthenticated", err)
		}
	})
}

func TestFromContextRejects(t *testing.T) {
	ca := newTestCA(t)
	validLeaf := ca.issueLeaf(t, testLeafConfig{})
	now := time.Now()

	otherLeaf := ca.issueLeaf(t, testLeafConfig{})
	expiredLeaf := ca.issueLeaf(t, testLeafConfig{
		notBefore: now.Add(-2 * time.Hour),
		notAfter:  now.Add(-time.Hour),
	})
	notYetValidLeaf := ca.issueLeaf(t, testLeafConfig{
		notBefore: now.Add(time.Hour),
		notAfter:  now.Add(2 * time.Hour),
	})
	serverEKULeaf := ca.issueLeaf(t, testLeafConfig{
		eku: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	})
	foreignURILeaf := ca.issueLeaf(t, testLeafConfig{
		uris: []*url.URL{mustParseURL(t, "https://example.com/not-a-workload")},
	})
	noURILeaf := ca.issueLeaf(t, testLeafConfig{uris: []*url.URL{}})

	cases := map[string]context.Context{
		"no peer in context":     context.Background(),
		"peer without auth info": peer.NewContext(context.Background(), &peer.Peer{}),
		"non-tls auth info": peer.NewContext(context.Background(), &peer.Peer{
			AuthInfo: nonTLSAuthInfo{}}),
		"empty verified chains": rawStateContext(tls.ConnectionState{
			PeerCertificates: []*x509.Certificate{validLeaf},
		}),
		"empty peer certificates": rawStateContext(tls.ConnectionState{
			VerifiedChains: [][]*x509.Certificate{{validLeaf}},
		}),
		"leaf differs from verified leaf": tlsPeerContext(validLeaf, otherLeaf),
		"server eku against client check": tlsPeerContext(serverEKULeaf, serverEKULeaf),
		"expired certificate":             tlsPeerContext(expiredLeaf, expiredLeaf),
		"not yet valid certificate":       tlsPeerContext(notYetValidLeaf, notYetValidLeaf),
		"foreign uri san":                 tlsPeerContext(foreignURILeaf, foreignURILeaf),
		"missing uri san":                 tlsPeerContext(noURILeaf, noURILeaf),
	}

	for name, ctx := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, err := FromContext(ctx)
			if !errors.Is(err, ErrUnauthenticated) {
				t.Fatalf("got %v, want ErrUnauthenticated", err)
			}
		})
	}
}

func TestExpiryInfo(t *testing.T) {
	ca := newTestCA(t)
	now := time.Now()

	validLeaf := ca.issueLeaf(t, testLeafConfig{
		notBefore: now.Add(-time.Minute),
		notAfter:  now.Add(30 * time.Minute),
	})
	remaining, expired := ExpiryInfoAt(validLeaf, now)
	if expired {
		t.Fatal("valid certificate reported as expired")
	}
	// Время сертификата в DER хранится с точностью до секунды.
	if remaining <= 29*time.Minute || remaining > 30*time.Minute {
		t.Fatalf("got remaining %v, want about 30m", remaining)
	}

	expiredLeaf := ca.issueLeaf(t, testLeafConfig{
		notBefore: now.Add(-2 * time.Hour),
		notAfter:  now.Add(-time.Hour),
	})
	remaining, expired = ExpiryInfoAt(expiredLeaf, now)
	if !expired {
		t.Fatal("expired certificate reported as valid")
	}
	if remaining != 0 {
		t.Fatalf("got remaining %v, want 0", remaining)
	}

	notYetValidLeaf := ca.issueLeaf(t, testLeafConfig{
		notBefore: now.Add(time.Hour),
		notAfter:  now.Add(2 * time.Hour),
	})
	if _, expired = ExpiryInfoAt(notYetValidLeaf, now); !expired {
		t.Fatal("not-yet-valid certificate reported as valid")
	}

	if _, expired = ExpiryInfo(nil); !expired {
		t.Fatal("nil certificate reported as valid")
	}
}
