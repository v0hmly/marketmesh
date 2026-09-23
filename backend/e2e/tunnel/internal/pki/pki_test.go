package pki

import (
	"crypto/x509"
	"encoding/pem"
	"testing"
	"time"
)

func TestNewCreatesIsolatedShortLivedTrustBoundaries(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
	bundle, err := New("run-29", "dc-a", now)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	other, err := New("other-run", "dc-a", now)
	if err != nil {
		t.Fatalf("New(other) error = %v", err)
	}

	tunnelRoots := certPool(t, bundle.TunnelCAPEM)
	internalRoots := certPool(t, bundle.InternalCAPEM)
	wrongRoots := certPool(t, other.TunnelCAPEM)

	verifyLeaf(t, bundle.GatewayIn, x509.VerifyOptions{
		Roots:       tunnelRoots,
		DNSName:     GatewayInService + "." + Namespace + ".svc",
		KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		CurrentTime: now,
	}, bundle.GatewayInURI)
	verifyLeaf(t, bundle.GatewayOutTunnel, x509.VerifyOptions{
		Roots:       tunnelRoots,
		KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		CurrentTime: now,
	}, bundle.GatewayOutURI)
	verifyLeaf(t, bundle.FakeInternal, x509.VerifyOptions{
		Roots:       internalRoots,
		DNSName:     FakeInternalService + "." + Namespace + ".svc",
		KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		CurrentTime: now,
	}, bundle.FakeInternalURI)
	verifyLeaf(t, bundle.GatewayOutInternal, x509.VerifyOptions{
		Roots:       internalRoots,
		KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		CurrentTime: now,
	}, bundle.GatewayOutURI)

	assertVerifyFails(t, bundle.GatewayIn, x509.VerifyOptions{
		Roots:       wrongRoots,
		DNSName:     GatewayInService + "." + Namespace + ".svc",
		KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		CurrentTime: now,
	})
	assertVerifyFails(t, bundle.FakeInternal, x509.VerifyOptions{
		Roots:       tunnelRoots,
		DNSName:     FakeInternalService + "." + Namespace + ".svc",
		KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		CurrentTime: now,
	})
	assertVerifyFails(t, bundle.GatewayOutTunnel, x509.VerifyOptions{
		Roots:       tunnelRoots,
		KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		CurrentTime: now,
	})
	assertVerifyFails(t, bundle.GatewayIn, x509.VerifyOptions{
		Roots:       tunnelRoots,
		DNSName:     "wrong." + Namespace + ".svc",
		KeyUsages:   []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		CurrentTime: now,
	})
}

func TestNewRejectsUnboundedIdentifiers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		runID string
		dc    string
	}{
		{name: "empty run id", dc: "dc-a"},
		{name: "unsafe run id", runID: "run/29", dc: "dc-a"},
		{name: "uppercase dc", runID: "run-29", dc: "DC-A"},
		{name: "leading hyphen", runID: "-run-29", dc: "dc-a"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := New(test.runID, test.dc, time.Now()); err == nil {
				t.Fatal("New() error = nil, want validation error")
			}
		})
	}
}

func verifyLeaf(
	t *testing.T,
	certificate Certificate,
	options x509.VerifyOptions,
	wantURI string,
) {
	t.Helper()
	leaf := parseCertificate(t, certificate.CertificatePEM)
	if _, err := leaf.Verify(options); err != nil {
		t.Fatalf("certificate verification error = %v", err)
	}
	if len(leaf.URIs) != 1 || leaf.URIs[0].String() != wantURI {
		t.Fatalf("certificate URIs = %v, want [%s]", leaf.URIs, wantURI)
	}
	keyBlock, _ := pem.Decode(certificate.PrivateKeyPEM)
	if keyBlock == nil || keyBlock.Type != "PRIVATE KEY" {
		t.Fatal("private key is not a PKCS#8 PEM block")
	}
	if _, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes); err != nil {
		t.Fatalf("parsing private key: %v", err)
	}
}

func assertVerifyFails(t *testing.T, certificate Certificate, options x509.VerifyOptions) {
	t.Helper()
	if _, err := parseCertificate(t, certificate.CertificatePEM).Verify(options); err == nil {
		t.Fatal("certificate verification error = nil, want rejection")
	}
}

func certPool(t *testing.T, certificatePEM []byte) *x509.CertPool {
	t.Helper()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(certificatePEM) {
		t.Fatal("AppendCertsFromPEM() = false")
	}

	return pool
}

func parseCertificate(t *testing.T, certificatePEM []byte) *x509.Certificate {
	t.Helper()
	block, _ := pem.Decode(certificatePEM)
	if block == nil || block.Type != "CERTIFICATE" {
		t.Fatal("certificate is not a PEM block")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate() error = %v", err)
	}

	return certificate
}
