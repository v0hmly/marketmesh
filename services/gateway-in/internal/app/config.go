package app

import (
	"errors"
	"time"

	serviceruntime "github.com/v0hmly/marketmesh/platform/runtime"
)

const (
	serviceName                 = "gateway-in"
	defaultHTTPAddress          = ":8080"
	defaultGRPCAddress          = ":8443"
	defaultRequestTimeout       = 10 * time.Second
	defaultTunnelSessionTimeout = 30 * time.Minute
	defaultShutdownTimeout      = 20 * time.Second
	defaultHealthTimeout        = 2 * time.Second
)

type config struct {
	serviceVersion        string
	environment           string
	instanceID            string
	dataCenter            string
	httpAddress           string
	grpcAddress           string
	tlsCertificate        string
	tlsPrivateKey         string
	tlsClientCA           string
	expectedGatewayOutURI string
	requestTimeout        time.Duration
	tunnelSessionTimeout  time.Duration
	shutdownTimeout       time.Duration
	healthTimeout         time.Duration
	logLevel              string
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
	if result.dataCenter, err = env.RequiredString("DATA_CENTER"); err != nil {
		return config{}, err
	}
	if result.dataCenter != "dc-a" && result.dataCenter != "dc-b" {
		return config{}, errors.New("DATA_CENTER must be dc-a or dc-b")
	}
	if result.httpAddress, err = env.String("HTTP_ADDRESS", defaultHTTPAddress); err != nil {
		return config{}, err
	}
	if result.grpcAddress, err = env.String("GRPC_ADDRESS", defaultGRPCAddress); err != nil {
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
	if result.expectedGatewayOutURI, err = env.RequiredString("EXPECTED_GATEWAY_OUT_URI"); err != nil {
		return config{}, err
	}
	if result.requestTimeout, err = env.PositiveDuration("REQUEST_TIMEOUT", defaultRequestTimeout); err != nil {
		return config{}, err
	}
	if result.tunnelSessionTimeout, err = env.PositiveDuration(
		"TUNNEL_SESSION_TIMEOUT",
		defaultTunnelSessionTimeout,
	); err != nil {
		return config{}, err
	}
	if result.shutdownTimeout, err = env.PositiveDuration("SHUTDOWN_TIMEOUT", defaultShutdownTimeout); err != nil {
		return config{}, err
	}
	if result.healthTimeout, err = env.PositiveDuration("HEALTH_CHECK_TIMEOUT", defaultHealthTimeout); err != nil {
		return config{}, err
	}
	if result.logLevel, err = env.String("LOG_LEVEL", "info"); err != nil {
		return config{}, err
	}

	return result, nil
}
