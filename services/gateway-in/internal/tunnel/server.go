package tunnel

import (
	"slices"
	"time"

	contractv1 "github.com/v0hmly/marketmesh/api/gen/go/tunnel/v1"
	protocolv1 "github.com/v0hmly/marketmesh/api/tunnel/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

// Server implements the gRPC reverse-tunnel endpoint in the DMZ.
type Server struct {
	contractv1.UnimplementedTunnelServiceServer

	settings *settings
	registry *Registry
}

// New creates a tunnel server without opening any network connection.
func New(config Config) (*Server, error) {
	settings, err := newSettings(config)
	if err != nil {
		return nil, err
	}
	registry := newRegistry(settings)

	return &Server{
		settings: settings,
		registry: registry,
	}, nil
}

// Registry returns the logical-call entry point used by gateway-in adapters.
func (s *Server) Registry() *Registry {
	if s == nil {
		return nil
	}

	return s.registry
}

// Connect authenticates gateway-out, negotiates v1 policy, and runs one
// bounded bidirectional tunnel until either side disconnects or drains.
func (s *Server) Connect(
	stream contractv1.TunnelService_ConnectServer,
) error {
	if s == nil || s.settings == nil || s.registry == nil {
		return status.Error(codes.Internal, "internal error")
	}
	dataCenter, err := s.settings.peer.authorize(stream.Context())
	if err != nil {
		s.settings.instrumentation.refusal(stream.Context(), "peer_unauthorized")
		return status.Error(codes.PermissionDenied, "peer is not authorized")
	}
	if !s.registry.beginHandshake() {
		s.settings.instrumentation.refusal(stream.Context(), "tunnel_capacity")
		return status.Error(codes.ResourceExhausted, "tunnel capacity exhausted")
	}
	handshakeHeld := true
	defer func() {
		if handshakeHeld {
			s.registry.releaseHandshake()
		}
	}()

	firstFrame, err := receiveHandshake(stream, s.settings.handshakeTimeout)
	if err != nil {
		return err
	}
	if proto.Size(firstFrame) > int(s.settings.limits.GetMaxFrameBytes()) {
		return status.Error(codes.ResourceExhausted, "tunnel handshake is too large")
	}
	if err := protocolv1.ValidateGatewayOutFrame(firstFrame); err != nil {
		return status.Error(codes.InvalidArgument, "invalid tunnel handshake")
	}
	helloPayload, isHello := firstFrame.GetPayload().(*contractv1.ConnectRequest_Hello)
	if !isHello || helloPayload.Hello == nil {
		return status.Error(codes.InvalidArgument, "invalid tunnel handshake")
	}

	negotiated, err := negotiate(s.settings, helloPayload.Hello)
	if err != nil {
		s.settings.instrumentation.refusal(stream.Context(), "handshake_policy")
		return status.Error(codes.PermissionDenied, "tunnel policy denied")
	}
	tunnelID, err := randomOpaqueID()
	if err != nil {
		return status.Error(codes.Internal, "internal error")
	}
	instanceID := [16]byte{}
	copy(instanceID[:], helloPayload.Hello.GetInstanceId())

	activeSession := newSession(sessionParams{
		settings:   s.settings,
		registry:   s.registry,
		stream:     stream,
		id:         tunnelID,
		instanceID: instanceID,
		dataCenter: dataCenter,
		negotiated: negotiated,
	})
	err = s.registry.registerFromHandshake(activeSession)
	handshakeHeld = false
	if err != nil {
		return status.Error(codes.ResourceExhausted, "tunnel capacity exhausted")
	}
	hello := negotiated.hello(tunnelID, s.settings.instanceID)
	if err := stream.Send(hello); err != nil {
		activeSession.abortBeforeRun()
		return status.Error(codes.Unavailable, "tunnel unavailable")
	}
	s.settings.instrumentation.recordFrame(
		stream.Context(),
		"gateway_in_to_gateway_out",
		contractv1.TrafficClass_TRAFFIC_CLASS_UNSPECIFIED,
		"hello",
	)

	return activeSession.run()
}

type handshakeResult struct {
	frame *contractv1.ConnectRequest
	err   error
}

func receiveHandshake(
	stream contractv1.TunnelService_ConnectServer,
	timeout time.Duration,
) (*contractv1.ConnectRequest, error) {
	result := make(chan handshakeResult, 1)
	go func() {
		frame, err := stream.Recv()
		result <- handshakeResult{frame: frame, err: err}
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case received := <-result:
		if received.err != nil {
			return nil, status.Error(codes.InvalidArgument, "invalid tunnel handshake")
		}
		return received.frame, nil
	case <-timer.C:
		return nil, status.Error(codes.DeadlineExceeded, "tunnel handshake timeout")
	case <-stream.Context().Done():
		return nil, status.Error(codes.Canceled, "tunnel handshake canceled")
	}
}

type negotiation struct {
	limits       *contractv1.Limits
	routes       map[contractv1.RouteId]struct{}
	classes      map[contractv1.TrafficClass]struct{}
	capabilities map[contractv1.Capability]struct{}
}

func negotiate(settings *settings, hello *contractv1.GatewayOutHello) (negotiation, error) {
	peerCapabilities := make(map[contractv1.Capability]struct{}, len(hello.GetCapabilities()))
	for _, capability := range hello.GetCapabilities() {
		peerCapabilities[capability] = struct{}{}
	}
	capabilities := map[contractv1.Capability]struct{}{}
	for capability := range settings.capabilities {
		if _, supported := peerCapabilities[capability]; supported {
			capabilities[capability] = struct{}{}
		}
	}

	peerClasses := make(map[contractv1.TrafficClass]struct{}, len(hello.GetTrafficClasses()))
	for _, class := range hello.GetTrafficClasses() {
		peerClasses[class] = struct{}{}
	}
	peerRoutes := make(map[contractv1.RouteId]struct{}, len(hello.GetRouteIds()))
	for _, route := range hello.GetRouteIds() {
		peerRoutes[route] = struct{}{}
	}

	routes := map[contractv1.RouteId]struct{}{}
	classes := map[contractv1.TrafficClass]struct{}{}
	for route, policy := range settings.routes {
		if _, supported := peerRoutes[route]; !supported {
			continue
		}
		if _, supported := peerClasses[policy.TrafficClass]; !supported {
			continue
		}
		if policy.TrafficClass == contractv1.TrafficClass_TRAFFIC_CLASS_REALTIME &&
			!negotiatedCapability(capabilities, contractv1.Capability_CAPABILITY_REALTIME) {
			continue
		}
		routes[route] = struct{}{}
		classes[policy.TrafficClass] = struct{}{}
	}
	if len(routes) == 0 || len(classes) == 0 {
		return negotiation{}, ErrRouteNotAllowed
	}

	return negotiation{
		limits:       minimumLimits(settings.limits, hello.GetLimits()),
		routes:       routes,
		classes:      classes,
		capabilities: capabilities,
	}, nil
}

func (n negotiation) hello(
	tunnelID [protocolv1.TunnelIDBytes]byte,
	instanceID [protocolv1.InstanceIDBytes]byte,
) *contractv1.ConnectResponse {
	routes := make([]contractv1.RouteId, 0, len(n.routes))
	for route := range n.routes {
		routes = append(routes, route)
	}
	slices.Sort(routes)
	classes := make([]contractv1.TrafficClass, 0, len(n.classes))
	for class := range n.classes {
		classes = append(classes, class)
	}
	slices.Sort(classes)
	capabilities := make([]contractv1.Capability, 0, len(n.capabilities))
	for capability := range n.capabilities {
		capabilities = append(capabilities, capability)
	}
	slices.Sort(capabilities)

	return &contractv1.ConnectResponse{
		Header: &contractv1.FrameHeader{
			TunnelId: slices.Clone(tunnelID[:]),
		},
		Payload: &contractv1.ConnectResponse_Hello{
			Hello: &contractv1.GatewayInHello{
				InstanceId:              slices.Clone(instanceID[:]),
				SelectedProtocolVersion: protocolVersion,
				Capabilities:            capabilities,
				TrafficClasses:          classes,
				RouteIds:                routes,
				Limits:                  cloneLimits(n.limits),
			},
		},
	}
}

func minimumLimits(local *contractv1.Limits, peer *contractv1.Limits) *contractv1.Limits {
	return &contractv1.Limits{
		MaxFrameBytes:         min(local.GetMaxFrameBytes(), peer.GetMaxFrameBytes()),
		MaxDataBytes:          min(local.GetMaxDataBytes(), peer.GetMaxDataBytes()),
		MaxMessageBytes:       min(local.GetMaxMessageBytes(), peer.GetMaxMessageBytes()),
		MaxInFlightRequests:   min(local.GetMaxInFlightRequests(), peer.GetMaxInFlightRequests()),
		MaxMetadataEntries:    min(local.GetMaxMetadataEntries(), peer.GetMaxMetadataEntries()),
		MaxMetadataValueBytes: min(local.GetMaxMetadataValueBytes(), peer.GetMaxMetadataValueBytes()),
		MaxCreditBytes:        min(local.GetMaxCreditBytes(), peer.GetMaxCreditBytes()),
	}
}
