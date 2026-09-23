package app

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"net/url"
	"os"
)

func loadClientTLS(
	certificateFile string,
	keyFile string,
	rootCAFile string,
	serverName string,
	expectedIdentity string,
) (*tls.Config, error) {
	certificate, err := tls.LoadX509KeyPair(certificateFile, keyFile)
	if err != nil {
		return nil, errors.New("gateway-out: loading client key pair")
	}
	caPEM, err := os.ReadFile(rootCAFile)
	if err != nil {
		return nil, errors.New("gateway-out: reading root ca")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("gateway-out: parsing root ca")
	}
	expected, err := url.Parse(expectedIdentity)
	if err != nil || expected.Scheme == "" || expected.Host == "" || expected.User != nil ||
		expected.RawQuery != "" || expected.Fragment != "" {
		return nil, errors.New("gateway-out: expected server identity is invalid")
	}

	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		ServerName:   serverName,
		RootCAs:      roots,
		Certificates: []tls.Certificate{certificate},
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.VerifiedChains) == 0 || len(state.VerifiedChains[0]) == 0 {
				return errors.New("gateway-out: server chain is not verified")
			}
			identities := state.VerifiedChains[0][0].URIs
			if len(identities) != 1 || identities[0] == nil || identities[0].String() != expected.String() {
				return errors.New("gateway-out: server workload identity mismatch")
			}
			return nil
		},
	}, nil
}
