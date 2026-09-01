package testkit

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

const certificateLifetime = time.Hour

// TLSMaterial содержит временный CA и подписанные им server/client
// сертификаты. Материал создаётся только в памяти и очищается в Cleanup.
type TLSMaterial struct {
	mu sync.Mutex

	serverName        string
	caPEM             []byte
	caPool            *x509.CertPool
	serverCertificate tls.Certificate
	clientCertificate tls.Certificate
}

// NewTLS создаёт временный CA и сертификаты для TLS и mTLS тестов.
// serverName должен быть DNS-именем или IP, проверяемым TLS-клиентом.
func NewTLS(t testing.TB, serverName string) *TLSMaterial {
	t.Helper()

	material, err := newTLSMaterial(strings.TrimSpace(serverName), time.Now())
	if err != nil {
		t.Fatalf("testkit: create tls material: %v", err)
	}
	t.Cleanup(material.clear)

	return material
}

// ServerConfig возвращает независимую безопасную конфигурацию TLS server.
// При mutualTLS client обязан предъявить сертификат, подписанный временным CA.
func (material *TLSMaterial) ServerConfig(mutualTLS bool) *tls.Config {
	material.mu.Lock()
	defer material.mu.Unlock()

	config := &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{cloneCertificate(material.serverCertificate)},
	}
	if mutualTLS {
		config.ClientAuth = tls.RequireAndVerifyClientCert
		config.ClientCAs = material.caPool.Clone()
	}

	return config
}

// ClientConfig возвращает независимую конфигурацию TLS client с проверкой
// serverName. При mutualTLS она также предъявляет client certificate.
func (material *TLSMaterial) ClientConfig(mutualTLS bool) *tls.Config {
	material.mu.Lock()
	defer material.mu.Unlock()

	config := &tls.Config{
		MinVersion: tls.VersionTLS12,
		RootCAs:    material.caPool.Clone(),
		ServerName: material.serverName,
	}
	if mutualTLS {
		config.Certificates = []tls.Certificate{cloneCertificate(material.clientCertificate)}
	}

	return config
}

// CAPEM возвращает копию PEM временного CA для API, принимающих trust roots в
// сериализованном виде.
func (material *TLSMaterial) CAPEM() []byte {
	material.mu.Lock()
	defer material.mu.Unlock()

	return append([]byte{}, material.caPEM...)
}

func newTLSMaterial(serverName string, now time.Time) (*TLSMaterial, error) {
	if serverName == "" {
		return nil, errors.New("server name must not be empty")
	}

	caCertificate, caKey, caPEM, err := createCA(now)
	if err != nil {
		return nil, err
	}
	serverCertificate, err := createLeaf(
		caCertificate,
		caKey,
		now,
		serverName,
		[]x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	)
	if err != nil {
		return nil, err
	}
	clientCertificate, err := createLeaf(
		caCertificate,
		caKey,
		now,
		"marketmesh-test-client",
		[]x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	)
	if err != nil {
		return nil, err
	}

	caPool := x509.NewCertPool()
	if !caPool.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("append generated ca certificate")
	}

	return &TLSMaterial{
		serverName:        serverName,
		caPEM:             caPEM,
		caPool:            caPool,
		serverCertificate: serverCertificate,
		clientCertificate: clientCertificate,
	}, nil
}

func createCA(now time.Time) (*x509.Certificate, *ecdsa.PrivateKey, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("generate ca key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, nil, nil, err
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "MarketMesh test CA"},
		NotBefore:             now.Add(-time.Minute),
		NotAfter:              now.Add(certificateLifetime),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("create ca certificate: %w", err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("parse ca certificate: %w", err)
	}

	return certificate, key, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}

func createLeaf(
	caCertificate *x509.Certificate,
	caKey *ecdsa.PrivateKey,
	now time.Time,
	name string,
	usage []x509.ExtKeyUsage,
) (tls.Certificate, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("generate leaf key: %w", err)
	}
	serial, err := randomSerial()
	if err != nil {
		return tls.Certificate{}, err
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    now.Add(-time.Minute),
		NotAfter:     now.Add(certificateLifetime),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  append([]x509.ExtKeyUsage{}, usage...),
	}
	if address := net.ParseIP(name); address != nil {
		template.IPAddresses = []net.IP{address}
	} else {
		template.DNSNames = []string{name}
	}

	der, err := x509.CreateCertificate(
		rand.Reader,
		template,
		caCertificate,
		&key.PublicKey,
		caKey,
	)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("create leaf certificate: %w", err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("parse leaf certificate: %w", err)
	}

	return tls.Certificate{
		Certificate: [][]byte{der, caCertificate.Raw},
		PrivateKey:  key,
		Leaf:        certificate,
	}, nil
}

func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, fmt.Errorf("generate certificate serial: %w", err)
	}
	if serial.Sign() == 0 {
		serial.SetInt64(1)
	}

	return serial, nil
}

func cloneCertificate(source tls.Certificate) tls.Certificate {
	certificate := source
	certificate.Certificate = make([][]byte, len(source.Certificate))
	for index, der := range source.Certificate {
		certificate.Certificate[index] = append([]byte{}, der...)
	}
	certificate.OCSPStaple = append([]byte{}, source.OCSPStaple...)
	certificate.SignedCertificateTimestamps = make([][]byte, len(source.SignedCertificateTimestamps))
	for index, timestamp := range source.SignedCertificateTimestamps {
		certificate.SignedCertificateTimestamps[index] = append([]byte{}, timestamp...)
	}
	if key, ok := source.PrivateKey.(*ecdsa.PrivateKey); ok {
		encoded, err := key.Bytes()
		if err != nil {
			panic(fmt.Sprintf("testkit: encode generated private key: %v", err))
		}
		clonedKey, err := ecdsa.ParseRawPrivateKey(key.Curve, encoded)
		clear(encoded)
		if err != nil {
			panic(fmt.Sprintf("testkit: parse generated private key: %v", err))
		}
		certificate.PrivateKey = clonedKey
	}
	if len(certificate.Certificate) > 0 {
		leaf, err := x509.ParseCertificate(certificate.Certificate[0])
		if err == nil {
			certificate.Leaf = leaf
		}
	}

	return certificate
}

func (material *TLSMaterial) clear() {
	material.mu.Lock()
	defer material.mu.Unlock()

	clear(material.caPEM)
	material.caPEM = nil
	clearCertificate(&material.serverCertificate)
	clearCertificate(&material.clientCertificate)
	material.caPool = nil
}

func clearCertificate(certificate *tls.Certificate) {
	for _, der := range certificate.Certificate {
		clear(der)
	}
	certificate.Certificate = nil
	certificate.PrivateKey = nil
	certificate.Leaf = nil
}
