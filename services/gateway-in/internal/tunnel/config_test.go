package tunnel

import (
	"testing"
	"time"

	contractv1 "github.com/v0hmly/marketmesh/api/gen/go/tunnel/v1"
)

func TestNew_RejectsUnsafeConfiguration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{
			name: "empty peer allowlist",
			mutate: func(config *Config) {
				config.Peer.AllowedURIs = []string{}
			},
		},
		{
			name: "unknown peer uri",
			mutate: func(config *Config) {
				config.Peer.AllowedURIs = []string{"relative-identity"}
			},
		},
		{
			name: "realtime route without capability",
			mutate: func(config *Config) {
				config.Capabilities = []contractv1.Capability{
					contractv1.Capability_CAPABILITY_DRAIN,
				}
			},
		},
		{
			name: "route class mismatch",
			mutate: func(config *Config) {
				policy := config.Routes[contractv1.RouteId_ROUTE_ID_AUTH_LOGIN]
				policy.TrafficClass = contractv1.TrafficClass_TRAFFIC_CLASS_REALTIME
				config.Routes[contractv1.RouteId_ROUTE_ID_AUTH_LOGIN] = policy
			},
		},
		{
			name: "one-slot queue",
			mutate: func(config *Config) {
				config.Queues.Realtime = 1
			},
		},
		{
			name: "initial credit above negotiated limit",
			mutate: func(config *Config) {
				config.InitialResponseCredit = config.Limits.GetMaxCreditBytes() + 1
			},
		},
		{
			name: "missing handshake timeout",
			mutate: func(config *Config) {
				config.HandshakeTimeout = 0
			},
		},
		{
			name: "unbounded handshake timeout",
			mutate: func(config *Config) {
				config.HandshakeTimeout = 31 * time.Second
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			config := testConfig()
			test.mutate(&config)
			if _, err := New(config); err == nil {
				t.Fatal("New() error = nil, want unsafe configuration rejection")
			}
		})
	}
}

func TestRegistry_HandshakeCapacityIsGloballyBounded(t *testing.T) {
	t.Parallel()

	server, err := New(testConfig())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	registry := server.Registry()
	for range testConfig().MaxTunnels {
		if !registry.beginHandshake() {
			t.Fatal("beginHandshake() rejected capacity before global limit")
		}
	}
	if registry.beginHandshake() {
		t.Fatal("beginHandshake() accepted capacity above global limit")
	}
	registry.releaseHandshake()
	if !registry.beginHandshake() {
		t.Fatal("beginHandshake() did not restore released capacity")
	}
}

func TestRegistry_RoutePolicyIsStatic(t *testing.T) {
	t.Parallel()

	server, err := New(testConfig())
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	policy, found := server.Registry().RoutePolicy(
		contractv1.RouteId_ROUTE_ID_AUTH_LOGIN,
	)
	if !found {
		t.Fatal("RoutePolicy() found = false, want true")
	}
	if policy.TrafficClass != contractv1.TrafficClass_TRAFFIC_CLASS_CONTROL_AUTH {
		t.Fatalf("RoutePolicy() class = %s, want control/auth", policy.TrafficClass)
	}
	if _, found := server.Registry().RoutePolicy(
		contractv1.RouteId_ROUTE_ID_AUTH_REGISTER_CREDENTIALS,
	); found {
		t.Fatal("RoutePolicy() found unconfigured route")
	}
}
