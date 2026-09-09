package app

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type profilePKI struct {
	ca    *x509.Certificate
	key   ed25519.PrivateKey
	caPEM []byte
}

func newProfilePKI(t *testing.T) profilePKI {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, template, template, pub, key)
	if err != nil {
		t.Fatal(err)
	}
	ca, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return profilePKI{ca: ca, key: key, caPEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})}
}
func (p profilePKI) leaf(t *testing.T, identity string, usages []x509.ExtKeyUsage) (tls.Certificate, []byte, []byte) {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(identity)
	if err != nil {
		t.Fatal(err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: serial, URIs: []*url.URL{u}, DNSNames: []string{"localhost"}, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: usages, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, template, p.ca, pub, p.key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	cert.Leaf, err = x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	return cert, certPEM, keyPEM
}
func (p profilePKI) config(t *testing.T, role string, usages []x509.ExtKeyUsage) profileConfig {
	t.Helper()
	dir := t.TempDir()
	_, cert, key := p.leaf(t, role, usages)
	for name, contents := range map[string][]byte{"cert.pem": cert, "key.pem": key, "ca.pem": p.caPEM} {
		if err := os.WriteFile(filepath.Join(dir, name), contents, 0600); err != nil {
			t.Fatal(err)
		}
	}
	return profileConfig{trustDomain: "marketmesh.test", certificateFile: filepath.Join(dir, "cert.pem"), keyFile: filepath.Join(dir, "key.pem"), clientCAFile: filepath.Join(dir, "ca.pem"), authCAFile: filepath.Join(dir, "ca.pem"), authServerName: "localhost"}
}
func TestProfileTLSRequiresCorrectDualUseIdentity(t *testing.T) {
	pki := newProfilePKI(t)
	for _, tc := range []struct {
		identity string
		usage    []x509.ExtKeyUsage
	}{
		{"spiffe://marketmesh.test/test/user", []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}},
		{"spiffe://marketmesh.test/test/user", []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}},
		{"spiffe://marketmesh.test/test/auth", []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth}},
		{"spiffe://marketmesh.test/prod/user", []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth}},
	} {
		if _, _, err := profileTLS(pki.config(t, tc.identity, tc.usage), "test"); err == nil {
			t.Fatal("unsafe own certificate accepted", tc.identity)
		}
	}
	c := pki.config(t, "spiffe://marketmesh.test/test/user", []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth})
	server, client, err := profileTLS(c, "test")
	if err != nil {
		t.Fatal(err)
	}
	if server.MinVersion != tls.VersionTLS13 || server.ClientAuth != tls.RequireAndVerifyClientCert || client.InsecureSkipVerify {
		t.Fatal("insecure transport")
	}
	if client.VerifyConnection(tls.ConnectionState{}) == nil {
		t.Fatal("unverified Auth accepted")
	}
	for _, role := range []string{"spiffe://marketmesh.test/test/auth", "spiffe://marketmesh.test/test/gateway-in", "spiffe://marketmesh.test/prod/auth", "spiffe://other.test/test/auth"} {
		leaf, _, _ := pki.leaf(t, role, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth})
		err := client.VerifyConnection(tls.ConnectionState{VerifiedChains: [][]*x509.Certificate{{leaf.Leaf, pki.ca}}})
		if (err == nil) != (role == "spiffe://marketmesh.test/test/auth") {
			t.Fatal("Auth peer policy", role, err)
		}
	}
}
