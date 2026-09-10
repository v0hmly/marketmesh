// Command certgen creates ephemeral credentials only inside the disposable test volume.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

func main() {
	if err := generate("/fixture"); err != nil {
		fmt.Fprintln(os.Stderr, "fixture certificate generation failed")
		os.Exit(1)
	}
}
func generate(dir string) error {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "MarketMesh disposable registration fixture"}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign}
	der, err := x509.CreateCertificate(rand.Reader, ca, ca, pub, key)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "ca.pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		return err
	}
	for index, item := range []struct {
		name, email, identity string
		server                bool
	}{{"auth", "auth-publisher@marketmesh.test", "spiffe://marketmesh.test/test/auth", false}, {"user", "user-consumer@marketmesh.test", "spiffe://marketmesh.test/test/user", false}, {"admin", "fixture-admin@marketmesh.test", "", false}, {"server", "", "", true}} {
		leafPub, leafKey, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return err
		}
		leaf := &x509.Certificate{SerialNumber: big.NewInt(int64(index + 2)), Subject: pkix.Name{CommonName: item.name}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
		if item.email != "" {
			leaf.EmailAddresses = []string{item.email}
		}
		if item.identity != "" {
			u, err := url.Parse(item.identity)
			if err != nil {
				return err
			}
			leaf.URIs = []*url.URL{u}
		}
		if item.server {
			leaf.DNSNames = []string{"nats"}
			leaf.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		}
		der, err := x509.CreateCertificate(rand.Reader, leaf, ca, leafPub, key)
		if err != nil {
			return err
		}
		private, err := x509.MarshalPKCS8PrivateKey(leafKey)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, item.name+"-cert.pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, item.name+"-key.pem"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: private}), 0600); err != nil {
			return err
		}
	}
	return os.WriteFile(filepath.Join(dir, "nats.conf"), []byte(natsConfig), 0600)
}

const natsConfig = `port: 4222
server_name: registration-fixture
jetstream { store_dir: "/data/jetstream", max_mem_store: 64MB, max_file_store: 128MB }
max_payload: 16384
tls {
 cert_file: "/fixture/server-cert.pem"
 key_file: "/fixture/server-key.pem"
 ca_file: "/fixture/ca.pem"
 min_version: "1.3"
 verify_and_map: true
 timeout: 2
}
authorization {
 users: [
  { user: "auth-publisher@marketmesh.test", permissions: { publish: { allow: ["auth.account.registered.v1"] }, subscribe: { allow: ["_INBOX.>"] } } },
  { user: "user-consumer@marketmesh.test", permissions: { publish: { allow: ["$JS.API.CONSUMER.INFO.AUTH_REGISTRATION.USER_PROFILES_V1", "$JS.API.CONSUMER.MSG.NEXT.AUTH_REGISTRATION.USER_PROFILES_V1", "$JS.ACK.AUTH_REGISTRATION.USER_PROFILES_V1.>"] }, subscribe: { allow: ["_INBOX.>"] } } },
  { user: "fixture-admin@marketmesh.test", permissions: { publish: { allow: [">"] }, subscribe: { allow: [">"] } } }
 ]
}
`
