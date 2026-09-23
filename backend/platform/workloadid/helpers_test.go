package workloadid

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"math/big"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
)

// testCA — одноразовый центр сертификации для тестов пакета; закрытые ключи
// leaf-сертификатов не сохраняются, так как нужны только сами сертификаты.
type testCA struct {
	certificate *x509.Certificate
	privateKey  ed25519.PrivateKey
}

var testSerialCounter atomic.Int64

func newTestCA(t *testing.T) *testCA {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ca key: %v", err)
	}
	now := time.Now()
	template := &x509.Certificate{
		SerialNumber:          nextTestSerial(),
		Subject:               pkix.Name{CommonName: "workloadid test ca"},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, privateKey)
	if err != nil {
		t.Fatalf("create ca certificate: %v", err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse ca certificate: %v", err)
	}

	return &testCA{certificate: certificate, privateKey: privateKey}
}

// testLeafConfig — параметры leaf-сертификата; нулевые поля заменяются
// безопасными значениями по умолчанию (ClientAuth, срок вокруг текущего
// момента, идентичность spiffe://marketmesh.test/prod/gateway-in).
type testLeafConfig struct {
	uris      []*url.URL
	eku       []x509.ExtKeyUsage
	notBefore time.Time
	notAfter  time.Time
}

func (ca *testCA) issueLeaf(t *testing.T, cfg testLeafConfig) *x509.Certificate {
	t.Helper()
	if cfg.uris == nil {
		cfg.uris = []*url.URL{mustParseURL(t, "spiffe://marketmesh.test/prod/gateway-in")}
	}
	if cfg.eku == nil {
		cfg.eku = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	}
	if cfg.notBefore.IsZero() {
		cfg.notBefore = time.Now().Add(-time.Minute)
	}
	if cfg.notAfter.IsZero() {
		cfg.notAfter = time.Now().Add(time.Hour)
	}

	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate leaf key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: nextTestSerial(),
		Subject:      pkix.Name{CommonName: "workloadid test leaf"},
		NotBefore:    cfg.notBefore,
		NotAfter:     cfg.notAfter,
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  cfg.eku,
		URIs:         cfg.uris,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, ca.certificate, publicKey, ca.privateKey)
	if err != nil {
		t.Fatalf("create leaf certificate: %v", err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse leaf certificate: %v", err)
	}

	return leaf
}

func nextTestSerial() *big.Int {
	return big.NewInt(testSerialCounter.Add(1) + 1)
}

func mustParseURL(t *testing.T, raw string) *url.URL {
	t.Helper()
	parsed, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse url: %v", err)
	}

	return parsed
}

func mustURIs(t *testing.T, raws ...string) []*url.URL {
	t.Helper()
	uris := make([]*url.URL, 0, len(raws))
	for _, raw := range raws {
		uris = append(uris, mustParseURL(t, raw))
	}

	return uris
}

// tlsPeerContext строит контекст, который видел бы gRPC-сервер после
// mTLS-рукопожатия: peer с TLSInfo, leaf в PeerCertificates и
// верифицированная цепочка из одного leaf.
func tlsPeerContext(leaf, verifiedLeaf *x509.Certificate) context.Context {
	verifiedChains := [][]*x509.Certificate{}
	if verifiedLeaf != nil {
		verifiedChains = [][]*x509.Certificate{{verifiedLeaf}}
	}
	peerCertificates := []*x509.Certificate{}
	if leaf != nil {
		peerCertificates = []*x509.Certificate{leaf}
	}

	return peer.NewContext(context.Background(), &peer.Peer{
		AuthInfo: credentials.TLSInfo{State: tls.ConnectionState{
			PeerCertificates: peerCertificates,
			VerifiedChains:   verifiedChains,
		}},
	})
}
