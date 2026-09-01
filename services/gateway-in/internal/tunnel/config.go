package tunnel

import (
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	contractv1 "github.com/v0hmly/marketmesh/api/gen/go/tunnel/v1"
	protocolv1 "github.com/v0hmly/marketmesh/api/tunnel/v1"
	"go.opentelemetry.io/otel/metric"
	"go.opentelemetry.io/otel/trace"
)

const protocolVersion uint32 = 1

// QueueLimits bounds every independent outbound traffic lane.
type QueueLimits struct {
	TunnelControl int
	ControlAuth   int
	Regular       int
	Realtime      int
}

// RoutePolicy is the local deny-by-default policy for one finite RouteId.
type RoutePolicy struct {
	TrafficClass     contractv1.TrafficClass
	MaxRequestBytes  int
	MaxResponseBytes int
	MaxDeadline      time.Duration
	MaxInFlight      int
}

// PeerPolicy identifies the only gateway-out workloads allowed to open a
// tunnel. URIs are exact certificate URI SAN values; CommonName is ignored.
type PeerPolicy struct {
	AllowedURIs []string
}

// Config contains all resource and authorization limits for a tunnel server.
type Config struct {
	InstanceID             [protocolv1.InstanceIDBytes]byte
	Peer                   PeerPolicy
	Limits                 *contractv1.Limits
	Routes                 map[contractv1.RouteId]RoutePolicy
	Capabilities           []contractv1.Capability
	Queues                 QueueLimits
	MaxTunnels             int
	MaxTunnelsPerInstance  int
	MaxInFlightPerInstance int
	InitialResponseCredit  uint32
	HandshakeTimeout       time.Duration
	PingInterval           time.Duration
	PongTimeout            time.Duration
	Logger                 *slog.Logger
	MeterProvider          metric.MeterProvider
	TracerProvider         trace.TracerProvider
}

type settings struct {
	instanceID             [protocolv1.InstanceIDBytes]byte
	peer                   peerAuthorizer
	limits                 *contractv1.Limits
	routes                 map[contractv1.RouteId]RoutePolicy
	capabilities           map[contractv1.Capability]struct{}
	queues                 QueueLimits
	maxTunnels             int
	maxTunnelsPerInstance  int
	maxInFlightPerInstance int
	initialResponseCredit  uint32
	handshakeTimeout       time.Duration
	pingInterval           time.Duration
	pongTimeout            time.Duration
	log                    *slog.Logger
	instrumentation        *instrumentation
}

func newSettings(config Config) (*settings, error) {
	if config.Logger == nil {
		return nil, errors.New("tunnel: logger must not be nil")
	}
	if config.Limits == nil {
		return nil, errors.New("tunnel: limits must not be nil")
	}
	if config.MaxTunnels <= 0 || config.MaxTunnels > int(protocolv1.MaxInFlightRequests) {
		return nil, errors.New("tunnel: max tunnels is outside bounds")
	}
	if config.MaxTunnelsPerInstance <= 0 || config.MaxTunnelsPerInstance > config.MaxTunnels {
		return nil, errors.New("tunnel: max tunnels per instance is outside bounds")
	}
	if config.MaxInFlightPerInstance <= 0 ||
		config.MaxInFlightPerInstance > int(protocolv1.MaxInFlightRequests) {
		return nil, errors.New("tunnel: max in-flight per instance is outside bounds")
	}
	if config.InitialResponseCredit == 0 ||
		config.InitialResponseCredit > config.Limits.GetMaxCreditBytes() {
		return nil, errors.New("tunnel: initial response credit is outside bounds")
	}
	if config.HandshakeTimeout <= 0 || config.HandshakeTimeout > 30*time.Second {
		return nil, errors.New("tunnel: handshake timeout is outside bounds")
	}
	if config.PingInterval <= 0 || config.PongTimeout <= 0 || config.PongTimeout >= config.PingInterval {
		return nil, errors.New("tunnel: ping and pong intervals are invalid")
	}
	if err := validateQueueLimits(config.Queues); err != nil {
		return nil, err
	}

	peer, err := newPeerAuthorizer(config.Peer)
	if err != nil {
		return nil, err
	}
	limits := cloneLimits(config.Limits)
	capabilities, err := validateCapabilities(config.Capabilities)
	if err != nil {
		return nil, err
	}
	routes, err := validateRoutePolicies(config.Routes, limits, capabilities)
	if err != nil {
		return nil, err
	}
	if err := validateLocalHello(limits, routes, capabilities); err != nil {
		return nil, err
	}

	instrumentation, err := newInstrumentation(config.MeterProvider, config.TracerProvider)
	if err != nil {
		return nil, err
	}

	instanceID := config.InstanceID
	if instanceID == [protocolv1.InstanceIDBytes]byte{} {
		if _, err := rand.Read(instanceID[:]); err != nil {
			return nil, errors.New("tunnel: generating instance id")
		}
	}

	return &settings{
		instanceID:             instanceID,
		peer:                   peer,
		limits:                 limits,
		routes:                 routes,
		capabilities:           capabilities,
		queues:                 config.Queues,
		maxTunnels:             config.MaxTunnels,
		maxTunnelsPerInstance:  config.MaxTunnelsPerInstance,
		maxInFlightPerInstance: config.MaxInFlightPerInstance,
		initialResponseCredit:  config.InitialResponseCredit,
		handshakeTimeout:       config.HandshakeTimeout,
		pingInterval:           config.PingInterval,
		pongTimeout:            config.PongTimeout,
		log:                    config.Logger,
		instrumentation:        instrumentation,
	}, nil
}

func validateQueueLimits(limits QueueLimits) error {
	values := []int{
		limits.TunnelControl,
		limits.ControlAuth,
		limits.Regular,
		limits.Realtime,
	}
	for _, value := range values {
		if value < 2 || value > int(protocolv1.MaxInFlightRequests) {
			return errors.New("tunnel: queue depth is outside bounds")
		}
	}

	return nil
}

func validateCapabilities(
	values []contractv1.Capability,
) (map[contractv1.Capability]struct{}, error) {
	result := make(map[contractv1.Capability]struct{}, len(values))
	for _, capability := range values {
		switch capability {
		case contractv1.Capability_CAPABILITY_DRAIN,
			contractv1.Capability_CAPABILITY_REALTIME:
		case contractv1.Capability_CAPABILITY_SESSION_REVOCATION:
			return nil, errors.New("tunnel: session revocation is not configured")
		default:
			return nil, errors.New("tunnel: capability is unknown or unspecified")
		}
		if _, exists := result[capability]; exists {
			return nil, errors.New("tunnel: capability is duplicated")
		}
		result[capability] = struct{}{}
	}

	return result, nil
}

func validateRoutePolicies(
	values map[contractv1.RouteId]RoutePolicy,
	limits *contractv1.Limits,
	capabilities map[contractv1.Capability]struct{},
) (map[contractv1.RouteId]RoutePolicy, error) {
	if len(values) == 0 || len(values) > protocolv1.MaxAdvertisedRoutes {
		return nil, errors.New("tunnel: route policy count is outside bounds")
	}

	result := make(map[contractv1.RouteId]RoutePolicy, len(values))
	for route, policy := range values {
		expectedClass := routeTrafficClass(route)
		if expectedClass == contractv1.TrafficClass_TRAFFIC_CLASS_UNSPECIFIED {
			return nil, fmt.Errorf("tunnel: route %d is unknown", route)
		}
		if policy.TrafficClass != expectedClass {
			return nil, fmt.Errorf("tunnel: route %d has invalid traffic class", route)
		}
		if policy.MaxRequestBytes <= 0 ||
			policy.MaxRequestBytes > int(limits.GetMaxMessageBytes()) {
			return nil, fmt.Errorf("tunnel: route %d request limit is outside bounds", route)
		}
		if policy.MaxResponseBytes <= 0 ||
			policy.MaxResponseBytes > int(limits.GetMaxMessageBytes()) {
			return nil, fmt.Errorf("tunnel: route %d response limit is outside bounds", route)
		}
		if policy.MaxDeadline <= 0 {
			return nil, fmt.Errorf("tunnel: route %d deadline is outside bounds", route)
		}
		if policy.MaxInFlight <= 0 || policy.MaxInFlight > int(limits.GetMaxInFlightRequests()) {
			return nil, fmt.Errorf("tunnel: route %d in-flight limit is outside bounds", route)
		}
		if expectedClass == contractv1.TrafficClass_TRAFFIC_CLASS_REALTIME {
			if _, enabled := capabilities[contractv1.Capability_CAPABILITY_REALTIME]; !enabled {
				return nil, fmt.Errorf("tunnel: route %d requires realtime capability", route)
			}
		}
		result[route] = policy
	}

	return result, nil
}

func validateLocalHello(
	limits *contractv1.Limits,
	routes map[contractv1.RouteId]RoutePolicy,
	capabilities map[contractv1.Capability]struct{},
) error {
	routeIDs := make([]contractv1.RouteId, 0, len(routes))
	classes := map[contractv1.TrafficClass]struct{}{}
	for route, policy := range routes {
		routeIDs = append(routeIDs, route)
		classes[policy.TrafficClass] = struct{}{}
	}
	slices.Sort(routeIDs)

	trafficClasses := make([]contractv1.TrafficClass, 0, len(classes))
	for class := range classes {
		trafficClasses = append(trafficClasses, class)
	}
	slices.Sort(trafficClasses)

	capabilityValues := make([]contractv1.Capability, 0, len(capabilities))
	for capability := range capabilities {
		capabilityValues = append(capabilityValues, capability)
	}
	slices.Sort(capabilityValues)

	frame := &contractv1.ConnectResponse{
		Header: &contractv1.FrameHeader{
			TunnelId: make([]byte, protocolv1.TunnelIDBytes),
		},
		Payload: &contractv1.ConnectResponse_Hello{
			Hello: &contractv1.GatewayInHello{
				InstanceId:              make([]byte, protocolv1.InstanceIDBytes),
				SelectedProtocolVersion: protocolVersion,
				Capabilities:            capabilityValues,
				TrafficClasses:          trafficClasses,
				RouteIds:                routeIDs,
				Limits:                  cloneLimits(limits),
			},
		},
	}
	if err := protocolv1.ValidateGatewayInFrame(frame); err != nil {
		return fmt.Errorf("tunnel: local protocol policy is invalid: %w", err)
	}

	return nil
}

func cloneLimits(limits *contractv1.Limits) *contractv1.Limits {
	return &contractv1.Limits{
		MaxFrameBytes:         limits.GetMaxFrameBytes(),
		MaxDataBytes:          limits.GetMaxDataBytes(),
		MaxMessageBytes:       limits.GetMaxMessageBytes(),
		MaxInFlightRequests:   limits.GetMaxInFlightRequests(),
		MaxMetadataEntries:    limits.GetMaxMetadataEntries(),
		MaxMetadataValueBytes: limits.GetMaxMetadataValueBytes(),
		MaxCreditBytes:        limits.GetMaxCreditBytes(),
	}
}

func routeTrafficClass(route contractv1.RouteId) contractv1.TrafficClass {
	switch route {
	case contractv1.RouteId_ROUTE_ID_AUTH_REGISTER_CREDENTIALS,
		contractv1.RouteId_ROUTE_ID_AUTH_LOGIN,
		contractv1.RouteId_ROUTE_ID_AUTH_REFRESH_SESSION,
		contractv1.RouteId_ROUTE_ID_AUTH_REVOKE_SESSION,
		contractv1.RouteId_ROUTE_ID_AUTH_SESSION_ASSERTION:
		return contractv1.TrafficClass_TRAFFIC_CLASS_CONTROL_AUTH
	case contractv1.RouteId_ROUTE_ID_USER_GET_ME,
		contractv1.RouteId_ROUTE_ID_USER_UPDATE_ME:
		return contractv1.TrafficClass_TRAFFIC_CLASS_REGULAR
	case contractv1.RouteId_ROUTE_ID_REALTIME_CHAT,
		contractv1.RouteId_ROUTE_ID_REALTIME_NOTIFICATIONS:
		return contractv1.TrafficClass_TRAFFIC_CLASS_REALTIME
	default:
		return contractv1.TrafficClass_TRAFFIC_CLASS_UNSPECIFIED
	}
}

func routeLabel(route contractv1.RouteId) string {
	switch route {
	case contractv1.RouteId_ROUTE_ID_AUTH_REGISTER_CREDENTIALS:
		return "auth_register_credentials"
	case contractv1.RouteId_ROUTE_ID_AUTH_LOGIN:
		return "auth_login"
	case contractv1.RouteId_ROUTE_ID_AUTH_REFRESH_SESSION:
		return "auth_refresh_session"
	case contractv1.RouteId_ROUTE_ID_AUTH_REVOKE_SESSION:
		return "auth_revoke_session"
	case contractv1.RouteId_ROUTE_ID_AUTH_SESSION_ASSERTION:
		return "auth_session_assertion"
	case contractv1.RouteId_ROUTE_ID_USER_GET_ME:
		return "user_get_me"
	case contractv1.RouteId_ROUTE_ID_USER_UPDATE_ME:
		return "user_update_me"
	case contractv1.RouteId_ROUTE_ID_REALTIME_CHAT:
		return "realtime_chat"
	case contractv1.RouteId_ROUTE_ID_REALTIME_NOTIFICATIONS:
		return "realtime_notifications"
	default:
		return "unknown"
	}
}

func classLabel(class contractv1.TrafficClass) string {
	switch class {
	case contractv1.TrafficClass_TRAFFIC_CLASS_CONTROL_AUTH:
		return "control_auth"
	case contractv1.TrafficClass_TRAFFIC_CLASS_REGULAR:
		return "regular"
	case contractv1.TrafficClass_TRAFFIC_CLASS_REALTIME:
		return "realtime"
	default:
		return "tunnel_control"
	}
}
