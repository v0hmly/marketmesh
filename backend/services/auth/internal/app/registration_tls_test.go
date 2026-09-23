package app

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRegistrationTLSRequiresMatchingAuthIdentityAndUsage(t *testing.T) {
	for _, test := range []struct {
		identity       string
		usage          x509.ExtKeyUsage
		expired, valid bool
	}{
		{"spiffe://marketmesh.test/test/auth", x509.ExtKeyUsageClientAuth, false, true},
		{"spiffe://marketmesh.test/test/user", x509.ExtKeyUsageClientAuth, false, false},
		{"spiffe://marketmesh.test/prod/auth", x509.ExtKeyUsageClientAuth, false, false},
		{"spiffe://foreign.test/test/auth", x509.ExtKeyUsageClientAuth, false, false},
		{"spiffe://marketmesh.test/test/auth", x509.ExtKeyUsageServerAuth, false, false},
		{"spiffe://marketmesh.test/test/auth", x509.ExtKeyUsageClientAuth, true, false},
	} {
		dir := t.TempDir()
		pub, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		uri, err := url.Parse(test.identity)
		if err != nil {
			t.Fatal(err)
		}
		now := time.Now()
		until := now.Add(time.Hour)
		if test.expired {
			until = now.Add(-time.Minute)
		}
		template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test"}, NotBefore: now.Add(-time.Hour), NotAfter: until, URIs: []*url.URL{uri}, ExtKeyUsage: []x509.ExtKeyUsage{test.usage}, KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign, IsCA: true, BasicConstraintsValid: true}
		der, err := x509.CreateCertificate(rand.Reader, template, template, pub, key)
		if err != nil {
			t.Fatal(err)
		}
		private, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			t.Fatal(err)
		}
		certPath, keyPath := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
		if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}), 0600); err != nil {
			t.Fatal(err)
		}
		c := registrationConfig{trustDomain: "marketmesh.test", certificateFile: certPath, keyFile: keyPath, caFile: certPath, serverName: "nats"}
		result, err := registrationTLS(c, "test")
		if (err == nil) != test.valid {
			t.Fatalf("identity=%s usage=%v expired=%v err=%v", test.identity, test.usage, test.expired, err)
		}
		if test.valid && (result.MinVersion != tls.VersionTLS13 || result.InsecureSkipVerify || result.RootCAs == nil || result.ServerName != "nats") {
			t.Fatal("unsafe TLS client")
		}
	}
}
