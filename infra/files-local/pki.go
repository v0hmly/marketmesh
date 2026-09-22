//go:build ignore

// Disposable fixture PKI. No CA private key is written to disk.
package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

func main() {
	if len(os.Args) != 2 && len(os.Args) != 3 {
		os.Exit(2)
	}
	cluster := "dc-a"
	if len(os.Args) == 3 {
		cluster = os.Args[2]
		if cluster != "dc-a" && cluster != "dc-b" {
			os.Exit(2)
		}
	}
	root := os.Args[1]
	must(os.MkdirAll(root, 0700))
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	must(err)
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "MarketMesh Files disposable CA"}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
	must(err)
	write(root, "ca.crt", "CERTIFICATE", der)
	ca, err = x509.ParseCertificate(der)
	must(err)
	names := []string{"bao", "quarantine", "internal-clean", "delivery-a", "delivery-b", "pg-primary", "pg-replica", "files", "gateway-out", "files-auth", "auth", "redis"}
	for i, name := range names {
		leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		must(err)
		leaf := &x509.Certificate{SerialNumber: big.NewInt(int64(i + 2)), Subject: pkix.Name{CommonName: name}, NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(12 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth}, DNSNames: []string{name, "localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
		if name == "pg-primary" || name == "pg-replica" {
			// Both disposable nodes may serve the primary alias after fenced promotion.
			leaf.DNSNames = []string{"pg-primary", "pg-replica", "localhost"}
		}
		identity := ""
		switch name {
		case "files", "gateway-out", "auth":
			identity = "spiffe://marketmesh.test/env/test/cluster/" + cluster + "/ns/marketmesh/sa/" + name + "/pod/01234567-89ab-cdef-0123-456789abcdef"
		case "files-auth":
			identity = "spiffe://marketmesh.test/env/test/cluster/" + cluster + "/ns/marketmesh/sa/files"
		}
		if identity != "" {
			uri, err := url.Parse(identity)
			must(err)
			leaf.URIs = []*url.URL{uri}
		}
		cert, err := x509.CreateCertificate(rand.Reader, leaf, ca, &leafKey.PublicKey, key)
		must(err)
		private, err := x509.MarshalPKCS8PrivateKey(leafKey)
		must(err)
		write(root, name+".crt", "CERTIFICATE", cert)
		write(root, name+".key", "PRIVATE KEY", private)
	}
}
func write(root, name, kind string, data []byte) {
	must(os.WriteFile(filepath.Join(root, name), pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: data}), 0600))
}
func must(err error) {
	if err != nil {
		os.Stderr.WriteString("fixture PKI failed\n")
		os.Exit(1)
	}
}
