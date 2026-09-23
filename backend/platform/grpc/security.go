package grpc

import (
	"crypto/tls"
	"errors"
	"fmt"
	"strings"

	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

// PlaintextMode описывает явное исключение из TLS policy.
type PlaintextMode uint8

const (
	// PlaintextForbidden запрещает незашифрованный transport.
	PlaintextForbidden PlaintextMode = iota

	// PlaintextLocal разрешает plaintext только вне production: для loopback,
	// bufconn и локальной разработки.
	PlaintextLocal

	// PlaintextTrustedMesh разрешает plaintext, когда шифрование и workload
	// identity гарантирует отдельно согласованный trusted service mesh.
	PlaintextTrustedMesh
)

// ServerSecurity настраивает TLS/mTLS либо явное plaintext-исключение.
type ServerSecurity struct {
	TLSConfig                *tls.Config
	RequireClientCertificate bool
	Plaintext                PlaintextMode
}

// ClientSecurity настраивает TLS/mTLS либо явное plaintext-исключение.
type ClientSecurity struct {
	TLSConfig                *tls.Config
	RequireClientCertificate bool
	Plaintext                PlaintextMode
}

func serverTransportCredentials(
	environment string,
	security ServerSecurity,
) (credentials.TransportCredentials, error) {
	if security.TLSConfig == nil {
		if security.RequireClientCertificate {
			return nil, errors.New("grpc: mTLS server requires TLS config")
		}

		return plaintextCredentials(environment, security.Plaintext)
	}
	if security.Plaintext != PlaintextForbidden {
		return nil, errors.New("grpc: TLS config and plaintext mode are mutually exclusive")
	}

	config, err := secureTLSConfig(security.TLSConfig)
	if err != nil {
		return nil, fmt.Errorf("grpc: validating server TLS: %w", err)
	}
	if len(config.Certificates) == 0 &&
		config.GetCertificate == nil &&
		config.GetConfigForClient == nil {
		return nil, errors.New("grpc: server TLS certificate is required")
	}
	if security.RequireClientCertificate {
		if config.ClientCAs == nil {
			return nil, errors.New("grpc: mTLS server client CA pool is required")
		}
		config.ClientAuth = tls.RequireAndVerifyClientCert
	}

	return credentials.NewTLS(config), nil
}

func clientTransportCredentials(
	environment string,
	security ClientSecurity,
) (credentials.TransportCredentials, error) {
	if security.TLSConfig == nil {
		if security.RequireClientCertificate {
			return nil, errors.New("grpc: mTLS client requires TLS config")
		}

		return plaintextCredentials(environment, security.Plaintext)
	}
	if security.Plaintext != PlaintextForbidden {
		return nil, errors.New("grpc: TLS config and plaintext mode are mutually exclusive")
	}

	config, err := secureTLSConfig(security.TLSConfig)
	if err != nil {
		return nil, fmt.Errorf("grpc: validating client TLS: %w", err)
	}
	if security.RequireClientCertificate &&
		len(config.Certificates) == 0 &&
		config.GetClientCertificate == nil {
		return nil, errors.New("grpc: mTLS client certificate is required")
	}

	return credentials.NewTLS(config), nil
}

func secureTLSConfig(config *tls.Config) (*tls.Config, error) {
	if config.InsecureSkipVerify {
		return nil, errors.New("TLS InsecureSkipVerify is forbidden")
	}

	cloned := config.Clone()
	if cloned.MinVersion == 0 {
		cloned.MinVersion = tls.VersionTLS12
	}
	if cloned.MinVersion < tls.VersionTLS12 {
		return nil, errors.New("minimum TLS version must be TLS 1.2 or newer")
	}

	return cloned, nil
}

func plaintextCredentials(
	environment string,
	mode PlaintextMode,
) (credentials.TransportCredentials, error) {
	environment = strings.TrimSpace(environment)
	if environment == "" {
		return nil, errors.New("grpc: environment must not be empty")
	}

	switch mode {
	case PlaintextLocal:
		if isProduction(environment) {
			return nil, errors.New("grpc: local plaintext is forbidden in production")
		}
	case PlaintextTrustedMesh:
		// Это намеренно явное исключение: service mesh обязан обеспечивать
		// шифрование и workload identity вне процесса.
	case PlaintextForbidden:
		return nil, errors.New("grpc: TLS config is required when plaintext is forbidden")
	default:
		return nil, errors.New("grpc: unknown plaintext mode")
	}

	return insecure.NewCredentials(), nil
}

func isProduction(environment string) bool {
	return strings.EqualFold(strings.TrimSpace(environment), "production") ||
		strings.EqualFold(strings.TrimSpace(environment), "prod")
}
