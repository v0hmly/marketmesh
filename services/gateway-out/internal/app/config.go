package app

import (
	"time"

	serviceruntime "github.com/v0hmly/marketmesh/platform/runtime"
)

const (
	serviceName            = "gateway-out"
	defaultHTTPAddress     = ":8080"
	defaultConnectTimeout  = 10 * time.Second
	defaultCallTimeout     = 10 * time.Second
	defaultShutdownTimeout = 20 * time.Second
	defaultHealthTimeout   = 2 * time.Second
)

type config struct {
	serviceVersion       string
	environment          string
	instanceID           string
	httpAddress          string
	gatewayInTarget      string
	gatewayInServerName  string
	expectedGatewayInURI string
	internalTarget       string
	internalServerName   string
	expectedInternalURI  string
	tunnelCertificate    string
	tunnelPrivateKey     string
	tunnelRootCA         string
	internalCertificate  string
	internalPrivateKey   string
	internalRootCA       string
	connectTimeout       time.Duration
	callTimeout          time.Duration
	shutdownTimeout      time.Duration
	healthTimeout        time.Duration
	logLevel             string
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
	if result.httpAddress, err = env.String("HTTP_ADDRESS", defaultHTTPAddress); err != nil {
		return config{}, err
	}
	if result.gatewayInTarget, err = env.RequiredString("GATEWAY_IN_TARGET"); err != nil {
		return config{}, err
	}
	if result.gatewayInServerName, err = env.RequiredString("GATEWAY_IN_SERVER_NAME"); err != nil {
		return config{}, err
	}
	if result.expectedGatewayInURI, err = env.RequiredString("EXPECTED_GATEWAY_IN_URI"); err != nil {
		return config{}, err
	}
	if result.internalTarget, err = env.RequiredString("INTERNAL_TARGET"); err != nil {
		return config{}, err
	}
	if result.internalServerName, err = env.RequiredString("INTERNAL_SERVER_NAME"); err != nil {
		return config{}, err
	}
	if result.expectedInternalURI, err = env.RequiredString("EXPECTED_INTERNAL_URI"); err != nil {
		return config{}, err
	}
	if result.tunnelCertificate, err = env.RequiredString("TUNNEL_TLS_CERT_FILE"); err != nil {
		return config{}, err
	}
	if result.tunnelPrivateKey, err = env.RequiredString("TUNNEL_TLS_KEY_FILE"); err != nil {
		return config{}, err
	}
	if result.tunnelRootCA, err = env.RequiredString("TUNNEL_TLS_ROOT_CA_FILE"); err != nil {
		return config{}, err
	}
	if result.internalCertificate, err = env.RequiredString("INTERNAL_TLS_CERT_FILE"); err != nil {
		return config{}, err
	}
	if result.internalPrivateKey, err = env.RequiredString("INTERNAL_TLS_KEY_FILE"); err != nil {
		return config{}, err
	}
	if result.internalRootCA, err = env.RequiredString("INTERNAL_TLS_ROOT_CA_FILE"); err != nil {
		return config{}, err
	}
	if result.connectTimeout, err = env.PositiveDuration("CONNECT_TIMEOUT", defaultConnectTimeout); err != nil {
		return config{}, err
	}
	if result.callTimeout, err = env.PositiveDuration("CALL_TIMEOUT", defaultCallTimeout); err != nil {
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
