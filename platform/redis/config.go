package redis

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"math"
	"net"
	"strconv"
	"strings"
	"time"

	serviceruntime "github.com/v0hmly/marketmesh/platform/runtime"
)

const maxRetryAttempts = 5

// Role обозначает независимую trust zone Redis.
type Role string

const (
	// RoleEdge предназначена для некритичного состояния DMZ.
	RoleEdge Role = "edge"

	// RoleAuth предназначена для чувствительного состояния Auth во внутренней сети.
	RoleAuth Role = "auth"
)

// Config задаёт один независимый Redis client. Для edge и auth должны
// создаваться отдельные экземпляры с отдельными Config.
type Config struct {
	Role           Role
	Address        serviceruntime.Secret
	Authentication AuthenticationConfig
	Database       int
	Transport      TransportConfig
	Pool           PoolConfig
	Timeouts       TimeoutConfig
	Retry          *RetryPolicy
}

// AuthenticationConfig задаёт Redis ACL credentials.
type AuthenticationConfig struct {
	Username serviceruntime.Secret
	Password serviceruntime.Secret
}

// TransportConfig требует либо TLS, либо документированное исключение для
// изолированной защищённой сети. Одновременно допустим только один вариант.
type TransportConfig struct {
	TLS                *TLSConfig
	PlaintextException *PlaintextException
}

// TLSConfig задаёт проверяемое TLS-соединение. Отключение проверки
// сертификата намеренно не поддерживается.
type TLSConfig struct {
	ServerName           serviceruntime.Secret
	RootCAs              *x509.CertPool
	MinVersion           uint16
	MaxVersion           uint16
	GetClientCertificate func(*tls.CertificateRequestInfo) (*tls.Certificate, error)
}

// PlaintextException фиксирует причину использования Redis без TLS внутри
// изолированной защищённой сети. Reason не попадает в telemetry.
type PlaintextException struct {
	Reason string
}

// PoolConfig задаёт конечные пределы пула соединений.
type PoolConfig struct {
	Size                  int
	MinIdleConns          int
	MaxIdleConns          int
	MaxActiveConns        int
	MaxConcurrentDials    int
	ConnMaxIdleTime       time.Duration
	ConnMaxLifetime       time.Duration
	ConnMaxLifetimeJitter time.Duration
}

// TimeoutConfig ограничивает все блокирующие стадии работы клиента.
type TimeoutConfig struct {
	Connect   time.Duration
	Command   time.Duration
	Pool      time.Duration
	Read      time.Duration
	Write     time.Duration
	Readiness time.Duration
	Shutdown  time.Duration
}

// RetryPolicy ограничивает повторы только явно идемпотентных операций.
type RetryPolicy struct {
	MaxAttempts       int
	InitialBackoff    time.Duration
	MaxBackoff        time.Duration
	BackoffMultiplier float64
}

type settings struct {
	role           Role
	address        serviceruntime.Secret
	authentication authenticationSettings
	database       int
	transport      transportSettings
	pool           PoolConfig
	timeouts       TimeoutConfig
	retry          retrySettings
}

type authenticationSettings struct {
	username serviceruntime.Secret
	password serviceruntime.Secret
}

type transportSettings struct {
	tls *tls.Config
}

type retrySettings struct {
	enabled           bool
	maxAttempts       int
	initialBackoff    time.Duration
	maxBackoff        time.Duration
	backoffMultiplier float64
}

func normalizeConfig(config Config) (settings, error) {
	if config.Role != RoleEdge && config.Role != RoleAuth {
		return settings{}, invalidConfig("role must be edge or auth")
	}
	if !config.Address.Present() {
		return settings{}, invalidConfig("address is required")
	}
	if err := validateAddress(config.Address); err != nil {
		return settings{}, err
	}
	if !config.Authentication.Password.Present() {
		return settings{}, invalidConfig("password is required")
	}
	if config.Database < 0 {
		return settings{}, invalidConfig("database must not be negative")
	}

	transport, err := normalizeTransport(config.Transport)
	if err != nil {
		return settings{}, err
	}
	if err := validatePool(config.Pool); err != nil {
		return settings{}, err
	}
	if err := validateTimeouts(config.Timeouts); err != nil {
		return settings{}, err
	}
	retry, err := normalizeRetryPolicy(config.Retry)
	if err != nil {
		return settings{}, err
	}

	return settings{
		role:    config.Role,
		address: config.Address,
		authentication: authenticationSettings{
			username: config.Authentication.Username,
			password: config.Authentication.Password,
		},
		database:  config.Database,
		transport: transport,
		pool:      config.Pool,
		timeouts:  config.Timeouts,
		retry:     retry,
	}, nil
}

func validateAddress(address serviceruntime.Secret) error {
	host, port, err := net.SplitHostPort(address.Reveal())
	if err != nil || strings.TrimSpace(host) == "" || strings.TrimSpace(port) == "" ||
		host != strings.TrimSpace(host) || port != strings.TrimSpace(port) {
		return invalidConfig("address must use host:port format")
	}
	if strings.ContainsAny(host, "\r\n\x00") || strings.ContainsAny(port, "\r\n\x00") {
		return invalidConfig("address is malformed")
	}
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return invalidConfig("address port must be between 1 and 65535")
	}

	return nil
}

func normalizeTransport(config TransportConfig) (transportSettings, error) {
	if (config.TLS == nil) == (config.PlaintextException == nil) {
		return transportSettings{}, invalidConfig(
			"exactly one of TLS or plaintext exception is required",
		)
	}
	if config.PlaintextException != nil {
		reason := strings.TrimSpace(config.PlaintextException.Reason)
		if reason == "" {
			return transportSettings{}, invalidConfig("plaintext exception reason is required")
		}
		if len(reason) > 256 || strings.ContainsAny(reason, "\r\n\x00") {
			return transportSettings{}, invalidConfig("plaintext exception reason is malformed")
		}

		return transportSettings{}, nil
	}

	configTLS := config.TLS
	if !configTLS.ServerName.Present() {
		return transportSettings{}, invalidConfig("TLS server name is required")
	}
	serverName := strings.TrimSpace(configTLS.ServerName.Reveal())
	if serverName == "" || strings.ContainsAny(serverName, "\r\n\x00") {
		return transportSettings{}, invalidConfig("TLS server name is malformed")
	}
	minVersion := configTLS.MinVersion
	if minVersion == 0 {
		minVersion = tls.VersionTLS12
	}
	if minVersion < tls.VersionTLS12 || minVersion > tls.VersionTLS13 {
		return transportSettings{}, invalidConfig("TLS minimum version must be TLS 1.2 or TLS 1.3")
	}
	if configTLS.MaxVersion != 0 &&
		(configTLS.MaxVersion < minVersion || configTLS.MaxVersion > tls.VersionTLS13) {
		return transportSettings{}, invalidConfig("TLS maximum version is invalid")
	}

	var rootCAs *x509.CertPool
	if configTLS.RootCAs != nil {
		rootCAs = configTLS.RootCAs.Clone()
	}

	return transportSettings{tls: &tls.Config{
		ServerName:           serverName,
		RootCAs:              rootCAs,
		MinVersion:           minVersion,
		MaxVersion:           configTLS.MaxVersion,
		GetClientCertificate: configTLS.GetClientCertificate,
	}}, nil
}

func validatePool(config PoolConfig) error {
	if config.Size <= 0 {
		return invalidConfig("pool size must be positive")
	}
	if config.MinIdleConns < 0 || config.MinIdleConns > config.Size {
		return invalidConfig("minimum idle connections must be between zero and pool size")
	}
	if config.MaxIdleConns <= 0 || config.MaxIdleConns > config.Size ||
		config.MinIdleConns > config.MaxIdleConns {
		return invalidConfig(
			"maximum idle connections must be between minimum idle connections and pool size",
		)
	}
	if config.MaxActiveConns < config.Size {
		return invalidConfig("maximum active connections must be at least pool size")
	}
	if config.MaxConcurrentDials <= 0 || config.MaxConcurrentDials > config.Size {
		return invalidConfig("maximum concurrent dials must be between one and pool size")
	}
	if config.ConnMaxIdleTime <= 0 || config.ConnMaxIdleTime > config.ConnMaxLifetime {
		return invalidConfig(
			"connection maximum idle time must be positive and not exceed maximum lifetime",
		)
	}
	if config.ConnMaxLifetime <= 0 {
		return invalidConfig("connection maximum lifetime must be positive")
	}
	if config.ConnMaxLifetimeJitter < 0 ||
		config.ConnMaxLifetimeJitter > config.ConnMaxLifetime {
		return invalidConfig(
			"connection maximum lifetime jitter must be between zero and maximum lifetime",
		)
	}

	return nil
}

func validateTimeouts(config TimeoutConfig) error {
	values := []struct {
		name  string
		value time.Duration
	}{
		{"connect", config.Connect},
		{"command", config.Command},
		{"pool", config.Pool},
		{"read", config.Read},
		{"write", config.Write},
		{"readiness", config.Readiness},
		{"shutdown", config.Shutdown},
	}
	for _, timeout := range values {
		if timeout.value <= 0 {
			return invalidConfig(timeout.name + " timeout must be positive")
		}
	}
	if config.Pool > config.Command || config.Read > config.Command || config.Write > config.Command {
		return invalidConfig("pool, read, and write timeouts must not exceed command timeout")
	}

	return nil
}

func normalizeRetryPolicy(policy *RetryPolicy) (retrySettings, error) {
	if policy == nil {
		return retrySettings{}, nil
	}
	if policy.MaxAttempts < 2 || policy.MaxAttempts > maxRetryAttempts {
		return retrySettings{}, invalidConfig(
			fmt.Sprintf("retry max attempts must be between 2 and %d", maxRetryAttempts),
		)
	}
	if policy.InitialBackoff <= 0 {
		return retrySettings{}, invalidConfig("retry initial backoff must be positive")
	}
	if policy.MaxBackoff < policy.InitialBackoff {
		return retrySettings{}, invalidConfig(
			"retry max backoff must not be less than initial backoff",
		)
	}
	if math.IsNaN(policy.BackoffMultiplier) ||
		math.IsInf(policy.BackoffMultiplier, 0) ||
		policy.BackoffMultiplier < 1 {
		return retrySettings{}, invalidConfig(
			"retry backoff multiplier must be finite and at least one",
		)
	}

	return retrySettings{
		enabled:           true,
		maxAttempts:       policy.MaxAttempts,
		initialBackoff:    policy.InitialBackoff,
		maxBackoff:        policy.MaxBackoff,
		backoffMultiplier: policy.BackoffMultiplier,
	}, nil
}

func invalidConfig(message string) error {
	return errors.Join(ErrInvalidConfig, errors.New("redis: "+message))
}
