// Package pki creates short-lived, in-memory credentials for the disposable
// tunnel E2E environment. It is not a general-purpose certificate authority.
package pki

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/url"
	"strings"
	"time"
)

const (
	TrustDomain         = "marketmesh.test"
	Namespace           = "marketmesh-e2e-tunnel"
	GatewayInService    = "mm29-gateway-in"
	FakeInternalService = "mm29-fake-internal"
	validity            = 4 * time.Hour
	clockSkew           = time.Minute
	maxIdentifierBytes  = 32
)

// Certificate contains one PEM-encoded leaf certificate and private key.
type Certificate struct {
	CertificatePEM []byte
	PrivateKeyPEM  []byte
}

// Bundle contains the two isolated trust boundaries needed by one DC.
// CA private keys are intentionally not retained after leaf issuance.
type Bundle struct {
	TunnelCAPEM        []byte
	GatewayIn          Certificate
	GatewayOutTunnel   Certificate
	InternalCAPEM      []byte
	FakeInternal       Certificate
	GatewayOutInternal Certificate
	GatewayInURI       string
	GatewayOutURI      string
	FakeInternalURI    string
}

// New creates a short-lived bundle using cryptographically secure randomness.
func New(runID string, dc string, now time.Time) (Bundle, error) {
	return newBundle(runID, dc, now, rand.Reader)
}

// ValidateRunID validates the finite label/identity form without creating keys.
func ValidateRunID(runID string) error {
	return validateIdentifier("run id", runID)
}

func newBundle(runID string, dc string, now time.Time, random io.Reader) (Bundle, error) {
	if err := validateIdentifier("run id", runID); err != nil {
		return Bundle{}, err
	}
	if err := validateIdentifier("dc", dc); err != nil {
		return Bundle{}, err
	}
	if now.IsZero() {
		return Bundle{}, errors.New("pki: current time is required")
	}
	if random == nil {
		return Bundle{}, errors.New("pki: random source is required")
	}

	gatewayInURI := workloadURI(runID, dc, "gateway-in")
	gatewayOutURI := workloadURI(runID, dc, "gateway-out")
	fakeInternalURI := workloadURI(runID, dc, "fake-internal")

	tunnelCA, err := newCA("MarketMesh E2E tunnel "+dc, now, random)
	if err != nil {
		return Bundle{}, err
	}
	defer clear(tunnelCA.privateKey)
	internalCA, err := newCA("MarketMesh E2E internal "+dc, now, random)
	if err != nil {
		return Bundle{}, err
	}
	defer clear(internalCA.privateKey)

	gatewayIn, err := tunnelCA.issue(leafConfig{
		commonName: "gateway-in",
		uri:        gatewayInURI,
		dnsNames:   serviceDNSNames(GatewayInService),
		server:     true,
	}, now, random)
	if err != nil {
		return Bundle{}, err
	}
	gatewayOutTunnel, err := tunnelCA.issue(leafConfig{
		commonName: "gateway-out-tunnel",
		uri:        gatewayOutURI,
		client:     true,
	}, now, random)
	if err != nil {
		return Bundle{}, err
	}
	fakeInternal, err := internalCA.issue(leafConfig{
		commonName: "fake-internal",
		uri:        fakeInternalURI,
		dnsNames:   serviceDNSNames(FakeInternalService),
		server:     true,
	}, now, random)
	if err != nil {
		return Bundle{}, err
	}
	gatewayOutInternal, err := internalCA.issue(leafConfig{
		commonName: "gateway-out-internal",
		uri:        gatewayOutURI,
		client:     true,
	}, now, random)
	if err != nil {
		return Bundle{}, err
	}

	return Bundle{
		TunnelCAPEM:        tunnelCA.certificatePEM,
		GatewayIn:          gatewayIn,
		GatewayOutTunnel:   gatewayOutTunnel,
		InternalCAPEM:      internalCA.certificatePEM,
		FakeInternal:       fakeInternal,
		GatewayOutInternal: gatewayOutInternal,
		GatewayInURI:       gatewayInURI.String(),
		GatewayOutURI:      gatewayOutURI.String(),
		FakeInternalURI:    fakeInternalURI.String(),
	}, nil
}

type certificateAuthority struct {
	certificate    *x509.Certificate
	certificatePEM []byte
	privateKey     ed25519.PrivateKey
}

func newCA(commonName string, now time.Time, random io.Reader) (certificateAuthority, error) {
	publicKey, privateKey, err := ed25519.GenerateKey(random)
	if err != nil {
		return certificateAuthority{}, fmt.Errorf("pki: generating ca key: %w", err)
	}
	serial, err := serialNumber(random)
	if err != nil {
		return certificateAuthority{}, err
	}
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             now.Add(-clockSkew),
		NotAfter:              now.Add(validity),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
		MaxPathLen:            0,
		MaxPathLenZero:        true,
	}
	der, err := x509.CreateCertificate(random, template, template, publicKey, privateKey)
	if err != nil {
		return certificateAuthority{}, fmt.Errorf("pki: creating ca certificate: %w", err)
	}
	certificate, err := x509.ParseCertificate(der)
	if err != nil {
		return certificateAuthority{}, fmt.Errorf("pki: parsing ca certificate: %w", err)
	}

	return certificateAuthority{
		certificate:    certificate,
		certificatePEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		privateKey:     privateKey,
	}, nil
}

type leafConfig struct {
	commonName string
	uri        *url.URL
	dnsNames   []string
	server     bool
	client     bool
}

func (ca certificateAuthority) issue(
	config leafConfig,
	now time.Time,
	random io.Reader,
) (Certificate, error) {
	if config.uri == nil || config.server == config.client {
		return Certificate{}, errors.New("pki: invalid leaf configuration")
	}
	publicKey, privateKey, err := ed25519.GenerateKey(random)
	if err != nil {
		return Certificate{}, fmt.Errorf("pki: generating leaf key: %w", err)
	}
	defer clear(privateKey)
	serial, err := serialNumber(random)
	if err != nil {
		return Certificate{}, err
	}
	extendedKeyUsage := x509.ExtKeyUsageClientAuth
	if config.server {
		extendedKeyUsage = x509.ExtKeyUsageServerAuth
	}
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: config.commonName},
		NotBefore:    now.Add(-clockSkew),
		NotAfter:     now.Add(validity),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{extendedKeyUsage},
		DNSNames:     append([]string(nil), config.dnsNames...),
		URIs:         []*url.URL{config.uri},
	}
	der, err := x509.CreateCertificate(
		random,
		template,
		ca.certificate,
		publicKey,
		ca.privateKey,
	)
	if err != nil {
		return Certificate{}, fmt.Errorf("pki: creating leaf certificate: %w", err)
	}
	privateKeyDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return Certificate{}, fmt.Errorf("pki: encoding leaf key: %w", err)
	}
	defer clear(privateKeyDER)

	return Certificate{
		CertificatePEM: pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		PrivateKeyPEM:  pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateKeyDER}),
	}, nil
}

func workloadURI(runID string, dc string, workload string) *url.URL {
	return &url.URL{
		Scheme: "spiffe",
		Host:   TrustDomain,
		Path:   "/e2e/" + runID + "/" + dc + "/" + workload,
	}
}

func serviceDNSNames(service string) []string {
	shortName := service + "." + Namespace + ".svc"
	return []string{shortName, shortName + ".cluster.local"}
}

func serialNumber(random io.Reader) (*big.Int, error) {
	serialBytes := make([]byte, 16)
	if _, err := io.ReadFull(random, serialBytes); err != nil {
		return nil, fmt.Errorf("pki: generating serial number: %w", err)
	}
	serialBytes[0] &= 0x7f
	serial := new(big.Int).SetBytes(serialBytes)
	if serial.Sign() == 0 {
		serial.SetInt64(1)
	}

	return serial, nil
}

func validateIdentifier(name string, value string) error {
	if value == "" || len(value) > maxIdentifierBytes || strings.TrimSpace(value) != value {
		return fmt.Errorf("pki: %s is outside bounds", name)
	}
	for _, character := range []byte(value) {
		if (character < 'a' || character > 'z') &&
			(character < '0' || character > '9') && character != '-' {
			return fmt.Errorf("pki: %s must use lower-kebab-case", name)
		}
	}
	if value[0] == '-' || value[len(value)-1] == '-' {
		return fmt.Errorf("pki: %s must use lower-kebab-case", name)
	}

	return nil
}
