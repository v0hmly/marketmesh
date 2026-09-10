package app

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"time"

	"github.com/v0hmly/marketmesh/platform/workloadid"
)

func registrationTLS(c registrationConfig, trustDomain, environment string) (*tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(c.certificateFile, c.keyFile)
	if err != nil || len(cert.Certificate) == 0 {
		return nil, errors.New("user registration: TLS key pair unavailable")
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return nil, errors.New("user registration: invalid TLS certificate")
	}
	id, err := workloadid.IdentityFromCertificate(leaf)
	if err != nil || id.TrustDomain != trustDomain || id.Environment != environment || id.Role != "user" {
		return nil, errors.New("user registration: certificate must identify the configured User workload")
	}
	clientUsage := false
	for _, usage := range leaf.ExtKeyUsage {
		clientUsage = clientUsage || usage == x509.ExtKeyUsageClientAuth
	}
	if now := time.Now(); !clientUsage || now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
		return nil, errors.New("user registration: certificate lacks valid client authentication usage")
	}
	roots, err := profileCA(c.caFile)
	if err != nil {
		return nil, errors.New("user registration: CA unavailable")
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, Certificates: []tls.Certificate{cert}, ServerName: c.serverName}, nil
}
