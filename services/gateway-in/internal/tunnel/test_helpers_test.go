package tunnel

import (
	"io"
	"log/slog"
	"time"

	contractv1 "github.com/v0hmly/marketmesh/api/gen/go/tunnel/v1"
)

const testPeerURI = "spiffe://marketmesh.test/gateway-out/instance-1"

func testConfig() Config {
	return Config{
		Peer: PeerPolicy{AllowedURIs: []string{testPeerURI}},
		Limits: &contractv1.Limits{
			MaxFrameBytes:         4096,
			MaxDataBytes:          512,
			MaxMessageBytes:       4096,
			MaxInFlightRequests:   8,
			MaxMetadataEntries:    8,
			MaxMetadataValueBytes: 1024,
			MaxCreditBytes:        1024,
		},
		Routes: map[contractv1.RouteId]RoutePolicy{
			contractv1.RouteId_ROUTE_ID_AUTH_LOGIN: {
				TrafficClass:     contractv1.TrafficClass_TRAFFIC_CLASS_CONTROL_AUTH,
				MaxRequestBytes:  2048,
				MaxResponseBytes: 2048,
				MaxDeadline:      time.Second,
				MaxInFlight:      4,
			},
			contractv1.RouteId_ROUTE_ID_USER_GET_ME: {
				TrafficClass:     contractv1.TrafficClass_TRAFFIC_CLASS_REGULAR,
				MaxRequestBytes:  2048,
				MaxResponseBytes: 2048,
				MaxDeadline:      time.Second,
				MaxInFlight:      4,
			},
			contractv1.RouteId_ROUTE_ID_REALTIME_CHAT: {
				TrafficClass:     contractv1.TrafficClass_TRAFFIC_CLASS_REALTIME,
				MaxRequestBytes:  2048,
				MaxResponseBytes: 2048,
				MaxDeadline:      time.Second,
				MaxInFlight:      4,
			},
		},
		Capabilities: []contractv1.Capability{
			contractv1.Capability_CAPABILITY_DRAIN,
			contractv1.Capability_CAPABILITY_REALTIME,
		},
		Queues: QueueLimits{
			TunnelControl: 2,
			ControlAuth:   2,
			Regular:       2,
			Realtime:      2,
		},
		MaxTunnels:             4,
		MaxTunnelsPerInstance:  2,
		MaxInFlightPerInstance: 8,
		InitialResponseCredit:  1024,
		HandshakeTimeout:       time.Second,
		PingInterval:           time.Hour,
		PongTimeout:            time.Minute,
		Logger: slog.New(slog.NewJSONHandler(io.Discard, &slog.HandlerOptions{
			Level: slog.LevelDebug,
		})),
	}
}
