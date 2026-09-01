package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	contractv1 "github.com/v0hmly/marketmesh/api/gen/go/tunnel/v1"
	"github.com/v0hmly/marketmesh/services/gateway-in/e2esnapshot"
	"github.com/v0hmly/marketmesh/services/gateway-in/internal/tunnel"
)

func TestConvertRoutingSnapshotProducesStrictE2EDocument(t *testing.T) {
	t.Parallel()

	instanceID := [16]byte{0x12, 0x34}
	routes := []tunnel.RoutingSnapshot{
		{
			Route:        contractv1.RouteId_ROUTE_ID_USER_GET_ME,
			RouteAllowed: true,
			Tunnels: []tunnel.TunnelSnapshot{
				{
					TunnelID: [16]byte{0xff}, InstanceID: instanceID,
					DataCenter: "dc-a", State: tunnel.TunnelStateReady, ActiveRequests: 2,
				},
			},
		},
		{
			Route:        contractv1.RouteId_ROUTE_ID_USER_UPDATE_ME,
			RouteAllowed: true,
			Tunnels: []tunnel.TunnelSnapshot{
				{
					TunnelID: [16]byte{0xee}, InstanceID: instanceID,
					DataCenter: "dc-a", State: tunnel.TunnelStateDraining, ActiveRequests: 1,
				},
			},
		},
	}

	snapshot, err := convertRoutingSnapshot(config{
		instanceID: "mm29-gateway-in-abcde",
	}, routes)
	if err != nil {
		t.Fatalf("convertRoutingSnapshot() error = %v", err)
	}
	if err := snapshot.Validate(); err != nil {
		t.Fatalf("snapshot.Validate() error = %v", err)
	}
	if snapshot.Routes[0].Tunnels[0].InstanceID != "12340000000000000000000000000000" ||
		snapshot.Routes[0].Tunnels[0].DataCenter != "dc-a" ||
		snapshot.Routes[1].Tunnels[0].State != e2esnapshot.TunnelStateDraining {
		t.Fatalf("snapshot = %#v", snapshot)
	}
}

func TestConvertRoutingSnapshotRejectsUnknownState(t *testing.T) {
	t.Parallel()

	_, err := convertRoutingSnapshot(config{
		instanceID: "mm29-gateway-in-abcde",
	}, []tunnel.RoutingSnapshot{{
		Route:        contractv1.RouteId_ROUTE_ID_USER_GET_ME,
		RouteAllowed: true,
		Tunnels: []tunnel.TunnelSnapshot{{
			InstanceID: [16]byte{0x01}, DataCenter: "dc-a",
			State: tunnel.TunnelState("unknown"),
		}},
	}})
	if err == nil {
		t.Fatal("convertRoutingSnapshot() error = nil")
	}
}

func TestE2ETunnelStateCoversMM30FiniteContract(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input tunnel.TunnelState
		want  e2esnapshot.TunnelState
	}{
		{input: tunnel.TunnelStateHandshaking, want: e2esnapshot.TunnelStateHandshaking},
		{input: tunnel.TunnelStateReady, want: e2esnapshot.TunnelStateReady},
		{input: tunnel.TunnelStateDraining, want: e2esnapshot.TunnelStateDraining},
		{input: tunnel.TunnelStateStale, want: e2esnapshot.TunnelStateStale},
		{input: tunnel.TunnelStateClosed, want: e2esnapshot.TunnelStateClosed},
	}
	for _, test := range tests {
		got, err := e2eTunnelState(test.input)
		if err != nil {
			t.Fatalf("e2eTunnelState(%q) error = %v", test.input, err)
		}
		if got != test.want {
			t.Fatalf("e2eTunnelState(%q) = %q, want %q", test.input, got, test.want)
		}
	}
}

func TestRegisterE2ERoutingSnapshotIsExplicit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		enabled    bool
		wantStatus int
	}{
		{name: "disabled", wantStatus: http.StatusNotFound},
		{name: "enabled", enabled: true, wantStatus: http.StatusServiceUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			mux := http.NewServeMux()
			if err := registerE2ERoutingSnapshot(mux, config{
				e2eRoutingSnapshot: test.enabled,
			}, nil); err != nil {
				t.Fatalf("registerE2ERoutingSnapshot() error = %v", err)
			}
			request := httptest.NewRequest(http.MethodGet, e2esnapshot.Path, http.NoBody)
			request.RemoteAddr = "127.0.0.1:12345"
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, request)
			if response.Code != test.wantStatus {
				t.Fatalf("status = %d, want %d", response.Code, test.wantStatus)
			}
		})
	}
}

func TestRoutingSnapshotHonorsCanceledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := routingSnapshot(ctx, config{}, nil); err == nil {
		t.Fatal("routingSnapshot() error = nil")
	}
}
