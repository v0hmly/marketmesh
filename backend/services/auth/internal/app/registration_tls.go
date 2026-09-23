package app

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"time"

	"github.com/v0hmly/marketmesh/platform/workloadid"
)

func registrationTLS(c registrationConfig, environment string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(c.certificateFile, c.keyFile)
	if err != nil || len(cert.Certificate) == 0 {
		return nil, errors.New("auth registration: TLS key pair unavailable")
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return nil, errors.New("auth registration: invalid TLS certificate")
	}
	id, err := workloadid.IdentityFromCertificate(leaf)
	if err != nil || id.TrustDomain != c.trustDomain || id.Environment != environment || id.Role != "auth" {
		return nil, errors.New("auth registration: TLS certificate must identify Auth")
	}
	validUsage := false
	for _, usage := range leaf.ExtKeyUsage {
		validUsage = validUsage || usage == x509.ExtKeyUsageClientAuth
	}
	if now := time.Now(); !validUsage || now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
		return nil, errors.New("auth registration: TLS certificate lacks valid client authentication usage")
	}
	roots, err := loadSessionCA(c.caFile)
	if err != nil {
		return nil, errors.New("auth registration: CA unavailable")
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}, RootCAs: roots, ServerName: c.serverName}, nil
}
