package app

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"os"
	"time"

	"github.com/v0hmly/marketmesh/platform/workloadid"
)

func profileTLS(c profileConfig, environment string) (*tls.Config, *tls.Config, error) {
	cert, err := tls.LoadX509KeyPair(c.certificateFile, c.keyFile)
	if err != nil || len(cert.Certificate) == 0 {
		return nil, nil, errors.New("user profile: TLS key pair unavailable")
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return nil, nil, errors.New("user profile: invalid TLS certificate")
	}
	identity, err := workloadid.IdentityFromCertificate(leaf)
	if err != nil || identity.TrustDomain != c.trustDomain || identity.Environment != environment || identity.Role != "user" {
		return nil, nil, errors.New("user profile: TLS certificate must identify the configured User workload")
	}
	if now := time.Now(); now.Before(leaf.NotBefore) || !now.Before(leaf.NotAfter) {
		return nil, nil, errors.New("user profile: TLS certificate outside validity interval")
	}
	var serverUsage, clientUsage bool
	for _, usage := range leaf.ExtKeyUsage {
		serverUsage = serverUsage || usage == x509.ExtKeyUsageServerAuth
		clientUsage = clientUsage || usage == x509.ExtKeyUsageClientAuth
	}
	if !serverUsage || !clientUsage {
		return nil, nil, errors.New("user profile: TLS certificate requires server and client authentication usage")
	}
	clients, err := profileCA(c.clientCAFile)
	if err != nil {
		return nil, nil, err
	}
	roots, err := profileCA(c.authCAFile)
	if err != nil {
		return nil, nil, err
	}
	server := &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}, ClientCAs: clients, ClientAuth: tls.RequireAndVerifyClientCert}
	client := &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}, RootCAs: roots, ServerName: c.authServerName,
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.VerifiedChains) == 0 || len(state.VerifiedChains[0]) == 0 {
				return errors.New("user profile: Auth TLS chain not verified")
			}
			peer, err := workloadid.IdentityFromCertificate(state.VerifiedChains[0][0])
			if err != nil || peer.TrustDomain != c.trustDomain || peer.Environment != environment || peer.Role != "auth" {
				return errors.New("user profile: Auth workload identity mismatch")
			}
			return nil
		},
	}
	return server, client, nil
}

func profileCA(path string) (*x509.CertPool, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("user profile: CA file unavailable")
	}
	defer func() { _ = f.Close() }()
	pem, err := io.ReadAll(io.LimitReader(f, 1024*1024+1))
	if err != nil || len(pem) > 1024*1024 {
		return nil, errors.New("user profile: CA file outside bounds")
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, errors.New("user profile: invalid CA file")
	}
	return pool, nil
}
