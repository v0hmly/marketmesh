package fakeapp

import (
	"time"

	serviceruntime "github.com/v0hmly/marketmesh/platform/runtime"
)

const (
	serviceName             = "tunnel-e2e-fake-internal"
	defaultGRPCAddress      = ":9443"
	defaultHTTPAddress      = ":8080"
	defaultLedgerEntries    = 10_000
	defaultRequestTimeout   = 5 * time.Second
	defaultShutdownTimeout  = 20 * time.Second
	defaultConnectionWait   = 5 * time.Second
	defaultKeepaliveTime    = 30 * time.Second
	defaultKeepaliveTimeout = 5 * time.Second
	defaultMessageBytes     = 64 * 1024
)

type config struct {
	serviceVersion    string
	environment       string
	instanceID        string
	grpcAddress       string
	httpAddress       string
	tlsCertificate    string
	tlsPrivateKey     string
	tlsClientCA       string
	expectedClientURI string
	maxLedgerEntries  int
	requestTimeout    time.Duration
	shutdownTimeout   time.Duration
	logLevel          string
}

func loadConfig(env serviceruntime.Env) (config, error) {
	var result config
	var err error

	if result.serviceVersion, err = env.RequiredString("SERVICE_VERSION"); err != nil {
		return config{}, err
	}
	if result.environment, err = env.RequiredString("ENVIRONMENT"); err != nil {
		return config{}, err
	}
	if result.instanceID, err = env.RequiredString("SERVICE_INSTANCE_ID"); err != nil {
		return config{}, err
	}
	if result.grpcAddress, err = env.String("GRPC_ADDRESS", defaultGRPCAddress); err != nil {
		return config{}, err
	}
	if result.httpAddress, err = env.String("HTTP_ADDRESS", defaultHTTPAddress); err != nil {
		return config{}, err
	}
	if result.tlsCertificate, err = env.RequiredString("TLS_CERT_FILE"); err != nil {
		return config{}, err
	}
	if result.tlsPrivateKey, err = env.RequiredString("TLS_KEY_FILE"); err != nil {
		return config{}, err
	}
	if result.tlsClientCA, err = env.RequiredString("TLS_CLIENT_CA_FILE"); err != nil {
		return config{}, err
	}
	if result.expectedClientURI, err = env.RequiredString("EXPECTED_GATEWAY_OUT_URI"); err != nil {
		return config{}, err
	}
	if result.maxLedgerEntries, err = env.PositiveInt("MAX_LEDGER_ENTRIES", defaultLedgerEntries); err != nil {
		return config{}, err
	}
	if result.requestTimeout, err = env.PositiveDuration("REQUEST_TIMEOUT", defaultRequestTimeout); err != nil {
		return config{}, err
	}
	if result.shutdownTimeout, err = env.PositiveDuration("SHUTDOWN_TIMEOUT", defaultShutdownTimeout); err != nil {
		return config{}, err
	}
	if result.logLevel, err = env.String("LOG_LEVEL", "info"); err != nil {
		return config{}, err
	}

	return result, nil
}
