package app

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCorporateTLSRequiresTrustedClientIdentity(t *testing.T) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "corporate.crt")
	if err := os.WriteFile(file, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0600); err != nil {
		t.Fatal(err)
	}
	config, err := corporateTLS(file)
	if err != nil {
		t.Fatal(err)
	}
	reached := 0
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { reached++; w.WriteHeader(204) }))
	server.TLS = config
	server.StartTLS()
	defer server.Close()
	for _, tc := range []struct {
		name             string
		present, trusted bool
		usage            x509.ExtKeyUsage
		want             bool
	}{
		{"missing", false, false, x509.ExtKeyUsageClientAuth, false},
		{"untrusted", true, false, x509.ExtKeyUsageClientAuth, false},
		{"server-only", true, true, x509.ExtKeyUsageServerAuth, false},
		{"corporate-client", true, true, x509.ExtKeyUsageClientAuth, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			transport := server.Client().Transport.(*http.Transport).Clone()
			defer transport.CloseIdleConnections()
			if tc.present {
				key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
				if err != nil {
					t.Fatal(err)
				}
				leaf := &x509.Certificate{SerialNumber: big.NewInt(2), NotBefore: ca.NotBefore, NotAfter: ca.NotAfter, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{tc.usage}}
				signer := caKey
				if !tc.trusted {
					signer = key
				}
				der, err := x509.CreateCertificate(rand.Reader, leaf, ca, &key.PublicKey, signer)
				if err != nil {
					t.Fatal(err)
				}
				// Force presentation even if the server's CA list would hide it.
				transport.TLSClientConfig.GetClientCertificate = func(*tls.CertificateRequestInfo) (*tls.Certificate, error) {
					return &tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key}, nil
				}
			}
			client := &http.Client{Transport: transport, Timeout: time.Second}
			response, err := client.Get(server.URL + "/staff")
			if tc.want {
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				if response.StatusCode != 204 {
					t.Fatal(response.StatusCode)
				}
			} else if err == nil {
				response.Body.Close()
				t.Fatal("unauthorized identity reached HTTP")
			}
		})
	}
	if reached != 1 {
		t.Fatalf("HTTP requests = %d, want only trusted client", reached)
	}
}
