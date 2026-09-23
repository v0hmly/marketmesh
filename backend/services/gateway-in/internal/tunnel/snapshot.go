package tunnel

import (
	"bytes"
	"slices"
	"time"

	contractv1 "github.com/v0hmly/marketmesh/api/gen/go/tunnel/v1"
)

// TunnelState is a finite routing state for diagnostics and internal bounded
// health adapters. Opaque identities from a snapshot must not be used as metric
// labels, log fields, or span attributes.
type TunnelState string

const (
	TunnelStateUnknown     TunnelState = ""
	TunnelStateHandshaking TunnelState = "handshaking"
	TunnelStateReady       TunnelState = "ready"
	TunnelStateDraining    TunnelState = "draining"
	TunnelStateStale       TunnelState = "stale"
	TunnelStateClosed      TunnelState = "closed"
)

// TunnelSnapshot describes one bounded tunnel entry for a configured route.
// IDs are opaque correlation values for disposable operational tooling only.
type TunnelSnapshot struct {
	TunnelID       [16]byte
	InstanceID     [16]byte
	DataCenter     string
	State          TunnelState
	ActiveRequests int
}

// RoutingSnapshot is a deterministic defensive copy of local registry state.
// It contains at most Config.MaxTunnels entries and never advances selection.
type RoutingSnapshot struct {
	Route            contractv1.RouteId
	RouteAllowed     bool
	RegistryDraining bool
	Tunnels          []TunnelSnapshot
}

// RoutingSnapshot returns local health and activity for one finite route. It
// does not expose a durable active/standby role because selection is performed
// independently for every request.
func (r *Registry) RoutingSnapshot(route contractv1.RouteId) RoutingSnapshot {
	result := RoutingSnapshot{
		Route:   route,
		Tunnels: []TunnelSnapshot{},
	}
	if r == nil || r.settings == nil {
		return result
	}
	_, result.RouteAllowed = r.settings.routes[route]
	if !result.RouteAllowed {
		return result
	}

	r.mu.Lock()
	sessions := make([]*session, 0, len(r.sessions))
	for _, activeSession := range r.sessions {
		sessions = append(sessions, activeSession)
	}
	result.RegistryDraining = r.isDraining
	r.mu.Unlock()

	now := r.settings.now()
	for _, activeSession := range sessions {
		tunnelSnapshot, supportsRoute := activeSession.routingSnapshot(route, now)
		if supportsRoute {
			result.Tunnels = append(result.Tunnels, tunnelSnapshot)
		}
	}
	slices.SortFunc(result.Tunnels, compareTunnelSnapshots)

	return result
}

func (s *session) routingSnapshot(
	route contractv1.RouteId,
	now time.Time,
) (TunnelSnapshot, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, supported := s.routes[route]; !supported {
		return TunnelSnapshot{}, false
	}

	state := TunnelStateHandshaking
	switch {
	case s.failureReason == "stale":
		state = TunnelStateStale
	case s.isClosed:
		state = TunnelStateClosed
	case s.isDraining:
		state = TunnelStateDraining
	case !s.isReady:
	case now.Sub(s.lastActivity) >= s.settings.staleAfter:
		state = TunnelStateStale
	default:
		state = TunnelStateReady
	}

	return TunnelSnapshot{
		TunnelID:       s.id,
		InstanceID:     s.instanceID,
		DataCenter:     s.dataCenter,
		State:          state,
		ActiveRequests: s.routeActive[route],
	}, true
}

func compareTunnelSnapshots(left TunnelSnapshot, right TunnelSnapshot) int {
	if left.DataCenter < right.DataCenter {
		return -1
	}
	if left.DataCenter > right.DataCenter {
		return 1
	}
	if comparison := bytes.Compare(left.InstanceID[:], right.InstanceID[:]); comparison != 0 {
		return comparison
	}

	return bytes.Compare(left.TunnelID[:], right.TunnelID[:])
}
