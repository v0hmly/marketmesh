package app

import (
	"errors"
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
	filesBrowserEnabled                                                                            bool
	filesTarget, filesServerName, expectedFilesURI, filesCertificate, filesPrivateKey, filesRootCA string

	periodicRediscoveryEnabled bool

	serviceVersion              string
	environment                 string
	instanceID                  string
	httpAddress                 string
	gatewayInTarget             string
	gatewayInServerName         string
	expectedGatewayInURI        string
	internalTarget              string
	internalServerName          string
	expectedInternalURI         string
	tunnelCertificate           string
	tunnelPrivateKey            string
	tunnelRootCA                string
	internalCertificate         string
	internalPrivateKey          string
	internalRootCA              string
	connectTimeout              time.Duration
	callTimeout                 time.Duration
	shutdownTimeout             time.Duration
	healthTimeout               time.Duration
	logLevel                    string
	authBrowserEnabled          bool
	userSettingsBrowserEnabled  bool
	userAvatarBrowserEnabled    bool
	userAddressesBrowserEnabled bool
	userBrowserEnabled          bool
	authTarget                  string
	authServerName              string
	expectedAuthURI             string
	authCertificate             string
	authPrivateKey              string
	authRootCA                  string
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
	if result.periodicRediscoveryEnabled, err = env.Bool("TUNNEL_PERIODIC_REDISCOVERY_ENABLED", true); err != nil {
		return config{}, err
	}
	if !result.periodicRediscoveryEnabled && result.environment != "test" {
		return config{}, errors.New("TUNNEL_PERIODIC_REDISCOVERY_ENABLED=false requires ENVIRONMENT=test")
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

	if result.authBrowserEnabled, err = env.Bool("AUTH_BROWSER_ENABLED", false); err != nil {
		return config{}, err
	}
	if result.userBrowserEnabled, err = env.Bool("USER_BROWSER_ENABLED", false); err != nil {
		return config{}, err
	}
	if result.userAvatarBrowserEnabled, err = env.Bool("USER_AVATAR_BROWSER_ENABLED", false); err != nil {
		return config{}, err
	}
	if result.userAvatarBrowserEnabled && !result.userBrowserEnabled {
		return config{}, errors.New("USER_AVATAR_BROWSER_ENABLED requires USER_BROWSER_ENABLED")
	}
	if result.userSettingsBrowserEnabled, err = env.Bool("USER_SETTINGS_BROWSER_ENABLED", false); err != nil {
		return config{}, err
	}
	if result.userSettingsBrowserEnabled && !result.userBrowserEnabled {
		return config{}, errors.New("USER_SETTINGS_BROWSER_ENABLED requires USER_BROWSER_ENABLED")
	}
	if result.userAddressesBrowserEnabled, err = env.Bool("USER_ADDRESSES_BROWSER_ENABLED", false); err != nil {
		return config{}, err
	}
	if result.userAddressesBrowserEnabled && !result.userBrowserEnabled {
		return config{}, errors.New("USER_ADDRESSES_BROWSER_ENABLED requires USER_BROWSER_ENABLED")
	}
	if result.filesBrowserEnabled, err = env.Bool("FILES_BROWSER_ENABLED", false); err != nil {
		return config{}, err
	}
	if result.filesBrowserEnabled {
		for _, item := range []struct {
			name   string
			target *string
		}{{"FILES_TARGET", &result.filesTarget}, {"FILES_SERVER_NAME", &result.filesServerName}, {"EXPECTED_FILES_URI", &result.expectedFilesURI}, {"FILES_TLS_CERT_FILE", &result.filesCertificate}, {"FILES_TLS_KEY_FILE", &result.filesPrivateKey}, {"FILES_TLS_ROOT_CA_FILE", &result.filesRootCA}} {
			if *item.target, err = env.RequiredString(item.name); err != nil {
				return config{}, err
			}
		}
	}
	if result.authBrowserEnabled || result.userBrowserEnabled || result.filesBrowserEnabled {
		for _, item := range []struct {
			name  string
			value *string
		}{
			{"AUTH_TARGET", &result.authTarget}, {"AUTH_SERVER_NAME", &result.authServerName},
			{"EXPECTED_AUTH_URI", &result.expectedAuthURI}, {"AUTH_TLS_CERT_FILE", &result.authCertificate},
			{"AUTH_TLS_KEY_FILE", &result.authPrivateKey}, {"AUTH_TLS_ROOT_CA_FILE", &result.authRootCA},
		} {
			if *item.value, err = env.RequiredString(item.name); err != nil {
				return config{}, err
			}
		}
	}
	return result, nil
}
