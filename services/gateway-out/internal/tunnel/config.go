package tunnel

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"math"
	"math/big"
	"net"
	"net/url"
	"slices"
	"strings"
	"time"

	contractv1 "github.com/v0hmly/marketmesh/api/gen/go/tunnel/v1"
	protocolv1 "github.com/v0hmly/marketmesh/api/tunnel/v1"
	"github.com/v0hmly/marketmesh/platform/logger"
	"github.com/v0hmly/marketmesh/platform/telemetry"
)

const protocolVersion uint32 = 1

var (
	// ErrClientUsed означает повторный запуск одноразового tunnel client.
	ErrClientUsed = errors.New("gateway-out tunnel client has already been used")
	// ErrReconnectExhausted означает исчерпание ограниченного числа последовательных попыток.
	ErrReconnectExhausted = errors.New("gateway-out tunnel reconnect attempts exhausted")
	// ErrQueueFull означает заполнение ограниченной очереди класса трафика.
	ErrQueueFull = errors.New("gateway-out tunnel send queue is full")
)

// ContextDialer позволяет тестам заменить transport без открытия listener в gateway-out.
type ContextDialer func(ctx context.Context, address string) (net.Conn, error)

// ClassLimits задаёт независимые пределы одного класса трафика.
type ClassLimits struct {
	MaxInFlight        uint32
	SendQueueDepth     int
	ReceiveWindowBytes uint32
}

// ReceiveLimits — копируемое представление локальных MM-10 receive ceilings.
type ReceiveLimits struct {
	MaxFrameBytes         uint32
	MaxDataBytes          uint32
	MaxMessageBytes       uint32
	MaxInFlightRequests   uint32
	MaxMetadataEntries    uint32
	MaxMetadataValueBytes uint32
	MaxCreditBytes        uint32
}

// ReconnectPolicy ограничивает последовательные попытки и задержку между ними.
type ReconnectPolicy struct {
	MaxAttempts      int
	InitialBackoff   time.Duration
	MaxBackoff       time.Duration
	Multiplier       float64
	JitterRatio      float64
	StableResetAfter time.Duration
}

// Config задаёт исходящий tunnel transport и локальные пределы gateway-out.
type Config struct {
	Target                 string
	TLSConfig              *tls.Config
	ExpectedServerIdentity string
	InstanceID             [protocolv1.InstanceIDBytes]byte
	ConnectTimeout         time.Duration
	HandshakeTimeout       time.Duration
	KeepaliveTime          time.Duration
	KeepaliveTimeout       time.Duration
	PingInterval           time.Duration
	PingTimeout            time.Duration
	DrainTimeout           time.Duration
	Limits                 ReceiveLimits
	ClassLimits            map[contractv1.TrafficClass]ClassLimits
	Reconnect              ReconnectPolicy
	Logger                 *logger.Logger
	Telemetry              *telemetry.Telemetry
	Dialer                 ContextDialer
}

type settings struct {
	target                 string
	tlsConfig              *tls.Config
	expectedServerIdentity string
	instanceID             [protocolv1.InstanceIDBytes]byte
	connectTimeout         time.Duration
	handshakeTimeout       time.Duration
	keepaliveTime          time.Duration
	keepaliveTimeout       time.Duration
	pingInterval           time.Duration
	pingTimeout            time.Duration
	drainTimeout           time.Duration
	limits                 ReceiveLimits
	classLimits            map[contractv1.TrafficClass]ClassLimits
	reconnect              ReconnectPolicy
	logger                 *logger.Logger
	telemetry              *telemetry.Telemetry
	dialer                 ContextDialer
	now                    func() time.Time
	jitter                 func(time.Duration) time.Duration
}

func normalizeConfig(config Config) (settings, error) {
	result := settings{
		target:                 strings.TrimSpace(config.Target),
		expectedServerIdentity: strings.TrimSpace(config.ExpectedServerIdentity),
		instanceID:             config.InstanceID,
		connectTimeout:         config.ConnectTimeout,
		handshakeTimeout:       config.HandshakeTimeout,
		keepaliveTime:          config.KeepaliveTime,
		keepaliveTimeout:       config.KeepaliveTimeout,
		pingInterval:           config.PingInterval,
		pingTimeout:            config.PingTimeout,
		drainTimeout:           config.DrainTimeout,
		limits:                 config.Limits,
		classLimits:            make(map[contractv1.TrafficClass]ClassLimits, len(config.ClassLimits)),
		reconnect:              config.Reconnect,
		logger:                 config.Logger,
		telemetry:              config.Telemetry,
		dialer:                 config.Dialer,
		now:                    time.Now,
		jitter:                 cryptoJitter,
	}
	for class, limits := range config.ClassLimits {
		result.classLimits[class] = limits
	}
	if err := validateSettings(&result); err != nil {
		return settings{}, err
	}

	tlsConfig, err := secureClientTLSConfig(config.TLSConfig, result.expectedServerIdentity)
	if err != nil {
		return settings{}, err
	}
	result.tlsConfig = tlsConfig

	return result, nil
}

func validateSettings(settings *settings) error {
	if settings.target == "" {
		return errors.New("gateway-out tunnel target must not be empty")
	}
	if settings.logger == nil {
		return errors.New("gateway-out tunnel logger must not be nil")
	}
	if settings.telemetry == nil {
		return errors.New("gateway-out tunnel telemetry must not be nil")
	}
	if allZero(settings.instanceID[:]) {
		return errors.New("gateway-out tunnel instance ID must not be zero")
	}
	if settings.connectTimeout <= 0 || settings.handshakeTimeout <= 0 {
		return errors.New("gateway-out tunnel connect and handshake timeouts must be positive")
	}
	if settings.keepaliveTime <= 0 || settings.keepaliveTimeout <= 0 {
		return errors.New("gateway-out tunnel transport keepalive values must be positive")
	}
	if settings.pingInterval <= 0 || settings.pingTimeout <= 0 {
		return errors.New("gateway-out tunnel application keepalive values must be positive")
	}
	if settings.pingTimeout >= settings.pingInterval {
		return errors.New("gateway-out tunnel ping timeout must be less than ping interval")
	}
	if settings.drainTimeout <= 0 {
		return errors.New("gateway-out tunnel drain timeout must be positive")
	}
	if settings.now == nil || settings.jitter == nil {
		return errors.New("gateway-out tunnel clock and jitter must not be nil")
	}
	if err := validateReconnectPolicy(settings.reconnect); err != nil {
		return err
	}
	if err := validateOfferedLimits(settings.limits); err != nil {
		return err
	}
	if err := validateClassLimits(settings.classLimits); err != nil {
		return err
	}

	return nil
}

func validateReconnectPolicy(policy ReconnectPolicy) error {
	if policy.MaxAttempts <= 0 || policy.MaxAttempts > 100 {
		return errors.New("gateway-out tunnel reconnect attempts must be between 1 and 100")
	}
	if policy.InitialBackoff <= 0 || policy.MaxBackoff < policy.InitialBackoff {
		return errors.New("gateway-out tunnel reconnect backoff bounds are invalid")
	}
	if math.IsNaN(policy.Multiplier) || math.IsInf(policy.Multiplier, 0) || policy.Multiplier < 1 {
		return errors.New("gateway-out tunnel reconnect multiplier must be finite and at least one")
	}
	if math.IsNaN(policy.JitterRatio) || math.IsInf(policy.JitterRatio, 0) ||
		policy.JitterRatio < 0 || policy.JitterRatio > 1 {
		return errors.New("gateway-out tunnel reconnect jitter ratio must be between zero and one")
	}
	if policy.StableResetAfter <= 0 {
		return errors.New("gateway-out tunnel reconnect stable reset duration must be positive")
	}

	return nil
}

func validateOfferedLimits(limits ReceiveLimits) error {
	hello := &contractv1.ConnectRequest{
		Header: &contractv1.FrameHeader{},
		Payload: &contractv1.ConnectRequest_Hello{
			Hello: &contractv1.GatewayOutHello{
				InstanceId:                slices.Repeat([]byte{1}, protocolv1.InstanceIDBytes),
				SupportedProtocolVersions: []uint32{protocolVersion},
				TrafficClasses: []contractv1.TrafficClass{
					contractv1.TrafficClass_TRAFFIC_CLASS_CONTROL_AUTH,
				},
				RouteIds: []contractv1.RouteId{
					contractv1.RouteId_ROUTE_ID_AUTH_LOGIN,
				},
				Limits: limits.proto(),
			},
		},
	}
	if err := protocolv1.ValidateGatewayOutFrame(hello); err != nil {
		return fmt.Errorf("gateway-out tunnel receive limits are invalid: %w", err)
	}

	return nil
}

func (limits ReceiveLimits) proto() *contractv1.Limits {
	return &contractv1.Limits{
		MaxFrameBytes:         limits.MaxFrameBytes,
		MaxDataBytes:          limits.MaxDataBytes,
		MaxMessageBytes:       limits.MaxMessageBytes,
		MaxInFlightRequests:   limits.MaxInFlightRequests,
		MaxMetadataEntries:    limits.MaxMetadataEntries,
		MaxMetadataValueBytes: limits.MaxMetadataValueBytes,
		MaxCreditBytes:        limits.MaxCreditBytes,
	}
}

func validateClassLimits(limits map[contractv1.TrafficClass]ClassLimits) error {
	required := []contractv1.TrafficClass{
		contractv1.TrafficClass_TRAFFIC_CLASS_CONTROL_AUTH,
		contractv1.TrafficClass_TRAFFIC_CLASS_REGULAR,
		contractv1.TrafficClass_TRAFFIC_CLASS_REALTIME,
	}
	for _, class := range required {
		value, found := limits[class]
		if !found {
			return fmt.Errorf("gateway-out tunnel limits for class %s are required", class)
		}
		if value.MaxInFlight == 0 || value.MaxInFlight > protocolv1.MaxInFlightRequests {
			return fmt.Errorf("gateway-out tunnel max in-flight for class %s is invalid", class)
		}
		if value.SendQueueDepth <= 0 || value.SendQueueDepth > int(protocolv1.MaxInFlightRequests) {
			return fmt.Errorf("gateway-out tunnel queue depth for class %s is invalid", class)
		}
		if value.ReceiveWindowBytes == 0 || value.ReceiveWindowBytes > protocolv1.MaxCreditBytes {
			return fmt.Errorf("gateway-out tunnel receive window for class %s is invalid", class)
		}
	}
	if len(limits) != len(required) {
		return errors.New("gateway-out tunnel class limits contain an unknown class")
	}

	return nil
}

func secureClientTLSConfig(config *tls.Config, expectedIdentity string) (*tls.Config, error) {
	if config == nil {
		return nil, errors.New("gateway-out tunnel mTLS config is required")
	}
	if config.InsecureSkipVerify {
		return nil, errors.New("gateway-out tunnel TLS InsecureSkipVerify is forbidden")
	}
	if strings.TrimSpace(config.ServerName) == "" {
		return nil, errors.New("gateway-out tunnel TLS server name is required")
	}
	if config.RootCAs == nil {
		return nil, errors.New("gateway-out tunnel server CA pool is required")
	}
	if len(config.Certificates) == 0 && config.GetClientCertificate == nil {
		return nil, errors.New("gateway-out tunnel client certificate is required")
	}

	expectedURI, err := url.Parse(expectedIdentity)
	if err != nil || expectedURI.Scheme == "" || expectedURI.Host == "" || expectedURI.RawQuery != "" ||
		expectedURI.Fragment != "" || expectedURI.User != nil {
		return nil, errors.New("gateway-out tunnel expected server identity is invalid")
	}

	cloned := config.Clone()
	if cloned.MinVersion == 0 {
		cloned.MinVersion = tls.VersionTLS12
	}
	if cloned.MinVersion < tls.VersionTLS12 {
		return nil, errors.New("gateway-out tunnel minimum TLS version must be TLS 1.2 or newer")
	}

	callerVerify := cloned.VerifyConnection
	cloned.VerifyConnection = func(state tls.ConnectionState) error {
		if !hasExactURIIdentity(state.VerifiedChains, expectedURI) {
			return errors.New("gateway-out tunnel server workload identity mismatch")
		}
		if callerVerify != nil {
			if err := callerVerify(state); err != nil {
				return errors.New("gateway-out tunnel additional server verification failed")
			}
		}

		return nil
	}

	return cloned, nil
}

func hasExactURIIdentity(chains [][]*x509.Certificate, expected *url.URL) bool {
	if len(chains) == 0 || len(chains[0]) == 0 {
		return false
	}

	identities := chains[0][0].URIs
	if len(identities) != 1 {
		return false
	}

	return identities[0].String() == expected.String()
}

func cryptoJitter(maximum time.Duration) time.Duration {
	if maximum <= 0 {
		return 0
	}

	value, err := rand.Int(rand.Reader, big.NewInt(int64(maximum)+1))
	if err != nil {
		return 0
	}

	return time.Duration(value.Int64())
}

func allZero(value []byte) bool {
	for _, item := range value {
		if item != 0 {
			return false
		}
	}

	return true
}
