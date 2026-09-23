package app

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"os"
)

func loadServerTLS(certificateFile string, keyFile string, clientCAFile string) (*tls.Config, error) {
	certificate, err := tls.LoadX509KeyPair(certificateFile, keyFile)
	if err != nil {
		return nil, errors.New("gateway-in: loading server key pair")
	}
	caPEM, err := os.ReadFile(clientCAFile)
	if err != nil {
		return nil, errors.New("gateway-in: reading client ca")
	}
	clientCAs := x509.NewCertPool()
	if !clientCAs.AppendCertsFromPEM(caPEM) {
		return nil, errors.New("gateway-in: parsing client ca")
	}

	return &tls.Config{
		MinVersion:   tls.VersionTLS13,
		Certificates: []tls.Certificate{certificate},
		ClientAuth:   tls.RequireAndVerifyClientCert,
		ClientCAs:    clientCAs,
	}, nil
}
