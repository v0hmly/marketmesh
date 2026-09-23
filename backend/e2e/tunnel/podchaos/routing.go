package podchaos

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
)

const (
	routingSchemaVersion = "marketmesh.gateway-in.e2e.routing-snapshot/v1"
	maxRoutingDocument   = 64 * 1024
	maxRoutingJSONDepth  = 8
	maxRoutingJSONArray  = 128
)

var requiredRoutingRoutes = [...]string{
	"ROUTE_ID_USER_GET_ME",
	"ROUTE_ID_USER_UPDATE_ME",
}

// RoutingReader obtains one strict snapshot through a bounded port-forward to
// an exact, ownership-validated gateway-in pod.
type RoutingReader interface {
	ReadRoutingSnapshot(context.Context, PodRef) (RoutingSnapshot, error)
}

// RoutingSnapshot duplicates the versioned E2E wire DTO without adding a
// service-module dependency to the E2E runner.
type RoutingSnapshot struct {
	SchemaVersion     string                 `json:"schema_version"`
	GatewayInInstance string                 `json:"gateway_in_instance"`
	Routes            []RoutingRouteSnapshot `json:"routes"`
}

// RoutingRouteSnapshot describes one fixed E2E route in a gateway-in pod.
type RoutingRouteSnapshot struct {
	Route            string                  `json:"route"`
	RouteAllowed     bool                    `json:"route_allowed"`
	RegistryDraining bool                    `json:"registry_draining"`
	Tunnels          []RoutingTunnelSnapshot `json:"tunnels"`
}

// RoutingTunnelSnapshot intentionally omits authority-bearing and
// high-cardinality tunnel/request identifiers.
type RoutingTunnelSnapshot struct {
	InstanceID     string `json:"instance_id"`
	DataCenter     string `json:"data_center"`
	State          string `json:"state"`
	ActiveRequests int    `json:"active_requests"`
}

type routingCandidate struct {
	pod      PodRef
	activity int
}

// DecodeRoutingSnapshot reads one strict, bounded routing document. Unknown
// and duplicate fields, trailing documents and unbounded arrays fail closed.
func DecodeRoutingSnapshot(reader io.Reader) (RoutingSnapshot, error) {
	if reader == nil {
		return RoutingSnapshot{}, fmt.Errorf("%w: routing reader is required", ErrUnsafeState)
	}
	data, err := io.ReadAll(io.LimitReader(reader, maxRoutingDocument+1))
	if err != nil || len(data) == 0 || len(data) > maxRoutingDocument {
		return RoutingSnapshot{}, fmt.Errorf("%w: routing document size is invalid", ErrUnsafeState)
	}
	if err := inspectRoutingJSON(data); err != nil {
		return RoutingSnapshot{}, err
	}

	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var snapshot RoutingSnapshot
	if err := decoder.Decode(&snapshot); err != nil {
		return RoutingSnapshot{}, fmt.Errorf("%w: routing document is invalid", ErrUnsafeState)
	}
	if err := ensureRoutingJSONEOF(decoder); err != nil {
		return RoutingSnapshot{}, err
	}
	dc, err := routingSnapshotDC(snapshot)
	if err != nil {
		return RoutingSnapshot{}, err
	}
	if err := validateRoutingSnapshot(dc, snapshot); err != nil {
		return RoutingSnapshot{}, fmt.Errorf("%w: routing document is invalid", ErrUnsafeState)
	}

	return snapshot, nil
}

func resolveRole(
	step Step,
	pods []PodRef,
	snapshots []RoutingSnapshot,
) (PodRef, int, int, error) {
	if !validStep(step) || len(pods) < 2 {
		return PodRef{}, 0, 0, fmt.Errorf("%w: routing candidates are incomplete", ErrUnsafeState)
	}

	var (
		candidates []routingCandidate
		err        error
	)
	switch step.Component {
	case ComponentGatewayIn:
		candidates, err = gatewayInCandidates(step.DC, pods, snapshots)
	case ComponentGatewayOut:
		candidates, err = gatewayOutCandidates(step.DC, pods, snapshots)
	default:
		err = errors.New("unknown component")
	}
	if err != nil {
		return PodRef{}, 0, 0, errors.Join(
			ErrUnsafeState,
			fmt.Errorf("resolve routing candidates: %w", err),
		)
	}

	slices.SortFunc(candidates, func(left, right routingCandidate) int {
		if left.activity > right.activity {
			return -1
		}
		if left.activity < right.activity {
			return 1
		}
		return comparePodRef(left.pod, right.pod)
	})
	selected := candidates[0]
	if step.Role == RoleStandby {
		selected = candidates[len(candidates)-1]
	}

	return selected.pod, len(candidates), len(candidates) - 1, nil
}

func gatewayInCandidates(
	dc DC,
	pods []PodRef,
	snapshots []RoutingSnapshot,
) ([]routingCandidate, error) {
	byName := make(map[string]RoutingSnapshot, len(snapshots))
	for _, snapshot := range snapshots {
		if err := validateRoutingSnapshot(dc, snapshot); err != nil {
			return nil, err
		}
		if _, exists := byName[snapshot.GatewayInInstance]; exists {
			return nil, errors.New("duplicate gateway-in snapshot")
		}
		byName[snapshot.GatewayInInstance] = snapshot
	}

	candidates := make([]routingCandidate, 0, len(pods))
	for _, pod := range pods {
		snapshot, found := byName[pod.Name]
		if !found {
			return nil, errors.New("gateway-in snapshot coverage is incomplete")
		}
		activity, isEligible := snapshotActivity(snapshot)
		if isEligible {
			candidates = append(candidates, routingCandidate{pod: pod, activity: activity})
		}
	}
	if len(byName) != len(pods) || len(candidates) < 2 {
		return nil, errors.New("gateway-in eligible capacity is incomplete")
	}
	slices.SortFunc(candidates, func(left, right routingCandidate) int {
		return comparePodRef(left.pod, right.pod)
	})

	return candidates, nil
}

func gatewayOutCandidates(
	dc DC,
	pods []PodRef,
	snapshots []RoutingSnapshot,
) ([]routingCandidate, error) {
	if len(snapshots) < 2 {
		return nil, errors.New("gateway-in snapshot coverage is incomplete")
	}
	observed := make(map[string]map[string]RoutingTunnelSnapshot)
	for _, route := range requiredRoutingRoutes {
		observed[route] = map[string]RoutingTunnelSnapshot{}
	}
	seenGateways := make(map[string]struct{}, len(snapshots))
	for _, snapshot := range snapshots {
		if err := validateRoutingSnapshot(dc, snapshot); err != nil {
			return nil, err
		}
		if _, exists := seenGateways[snapshot.GatewayInInstance]; exists {
			return nil, errors.New("duplicate gateway-in snapshot")
		}
		seenGateways[snapshot.GatewayInInstance] = struct{}{}
		for _, route := range snapshot.Routes {
			for _, activeTunnel := range route.Tunnels {
				if _, exists := observed[route.Route][activeTunnel.InstanceID]; exists {
					return nil, errors.New("instance id is ambiguous")
				}
				observed[route.Route][activeTunnel.InstanceID] = activeTunnel
			}
		}
	}

	wantedIDs := make(map[string]string, len(pods)*gatewayOutSlots)
	candidates := make([]routingCandidate, 0, len(pods))
	for _, pod := range pods {
		instanceIDs, err := gatewayOutInstanceIDs(pod.Name)
		if err != nil {
			return nil, err
		}
		activity := 0
		isEligible := true
		for _, instanceID := range instanceIDs {
			if _, exists := wantedIDs[instanceID]; exists {
				return nil, errors.New("instance id maps to multiple pods")
			}
			wantedIDs[instanceID] = pod.Name
			for _, route := range requiredRoutingRoutes {
				activeTunnel, found := observed[route][instanceID]
				if !found || activeTunnel.State != "ready" {
					isEligible = false
					continue
				}
				activity += activeTunnel.ActiveRequests
			}
		}
		if isEligible {
			candidates = append(candidates, routingCandidate{pod: pod, activity: activity})
		}
	}
	for _, instances := range observed {
		for instanceID := range instances {
			if _, found := wantedIDs[instanceID]; !found {
				return nil, errors.New("snapshot contains a foreign instance id")
			}
		}
	}
	if len(candidates) < 2 {
		return nil, errors.New("gateway-out eligible capacity is incomplete")
	}
	slices.SortFunc(candidates, func(left, right routingCandidate) int {
		return comparePodRef(left.pod, right.pod)
	})

	return candidates, nil
}

func validateRoutingSnapshot(dc DC, snapshot RoutingSnapshot) error {
	if snapshot.SchemaVersion != routingSchemaVersion ||
		!isDNSSubdomain(snapshot.GatewayInInstance) ||
		len(snapshot.Routes) != len(requiredRoutingRoutes) {
		return errors.New("routing snapshot header is invalid")
	}
	for index, route := range snapshot.Routes {
		if route.Route != requiredRoutingRoutes[index] ||
			!route.RouteAllowed ||
			route.RegistryDraining ||
			len(route.Tunnels) == 0 ||
			len(route.Tunnels) > 32 {
			return errors.New("routing route is invalid")
		}
		previous := ""
		for _, activeTunnel := range route.Tunnels {
			if !isLowerHex(activeTunnel.InstanceID, 32) ||
				activeTunnel.DataCenter != string(dc) ||
				!validRoutingState(activeTunnel.State) ||
				activeTunnel.ActiveRequests < 0 ||
				activeTunnel.ActiveRequests > 65_536 ||
				(previous != "" && previous >= activeTunnel.InstanceID) {
				return errors.New("routing tunnel is invalid")
			}
			previous = activeTunnel.InstanceID
		}
	}

	return nil
}

func routingSnapshotDC(snapshot RoutingSnapshot) (DC, error) {
	if len(snapshot.Routes) == 0 || len(snapshot.Routes[0].Tunnels) == 0 {
		return DCUnknown, fmt.Errorf("%w: routing data center is missing", ErrUnsafeState)
	}
	dc := DC(snapshot.Routes[0].Tunnels[0].DataCenter)
	if dc != DCA && dc != DCB {
		return DCUnknown, fmt.Errorf("%w: routing data center is invalid", ErrUnsafeState)
	}
	return dc, nil
}

func inspectRoutingJSON(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := inspectRoutingJSONValue(decoder, 0); err != nil {
		return fmt.Errorf("%w: routing json is ambiguous", ErrUnsafeState)
	}
	return ensureRoutingJSONEOF(decoder)
}

func inspectRoutingJSONValue(decoder *json.Decoder, depth int) error {
	if depth > maxRoutingJSONDepth {
		return errors.New("routing json nesting exceeds limit")
	}
	token, err := decoder.Token()
	if err != nil {
		return errors.New("routing json is malformed")
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return nil
	}

	switch delimiter {
	case '{':
		keys := map[string]struct{}{}
		for decoder.More() {
			keyToken, keyErr := decoder.Token()
			key, isString := keyToken.(string)
			if keyErr != nil || !isString {
				return errors.New("routing json object key is invalid")
			}
			if _, exists := keys[key]; exists {
				return errors.New("routing json object field is duplicated")
			}
			keys[key] = struct{}{}
			if err := inspectRoutingJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		return expectRoutingDelimiter(decoder, '}')
	case '[':
		count := 0
		for decoder.More() {
			count++
			if count > maxRoutingJSONArray {
				return errors.New("routing json array exceeds limit")
			}
			if err := inspectRoutingJSONValue(decoder, depth+1); err != nil {
				return err
			}
		}
		return expectRoutingDelimiter(decoder, ']')
	default:
		return errors.New("routing json delimiter is invalid")
	}
}

func expectRoutingDelimiter(decoder *json.Decoder, wanted json.Delim) error {
	token, err := decoder.Token()
	delimiter, valid := token.(json.Delim)
	if err != nil || !valid || delimiter != wanted {
		return errors.New("routing json delimiter is invalid")
	}
	return nil
}

func ensureRoutingJSONEOF(decoder *json.Decoder) error {
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return fmt.Errorf("%w: routing json has trailing content", ErrUnsafeState)
	}
	return nil
}

func snapshotActivity(snapshot RoutingSnapshot) (int, bool) {
	activity := 0
	for _, route := range snapshot.Routes {
		hasReady := false
		for _, activeTunnel := range route.Tunnels {
			if activeTunnel.State == "ready" {
				hasReady = true
				activity += activeTunnel.ActiveRequests
			}
		}
		if !hasReady {
			return 0, false
		}
	}

	return activity, true
}

func validRoutingState(state string) bool {
	switch state {
	case "handshaking", "ready", "draining", "stale", "closed":
		return true
	default:
		return false
	}
}

func isLowerHex(value string, length int) bool {
	if len(value) != length {
		return false
	}
	for _, character := range []byte(value) {
		if (character < '0' || character > '9') &&
			(character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func comparePodRef(left, right PodRef) int {
	if left.Name < right.Name {
		return -1
	}
	if left.Name > right.Name {
		return 1
	}
	return 0
}
